package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// ErrNotInTmux reports a plugin mode command run outside tmux.
var ErrNotInTmux = errors.New("not running inside tmux")

// pluginTmuxStateDir holds what plugin mode writes for a server the user runs.
// Claude settings files live in a settings directory below it, apart from the
// lyna-tmux server's: that server removes settings files none of its own
// panes reference, and it cannot see the panes of another server.
const pluginTmuxStateDir = "plugin"

// PluginConfName is the file `plugin tmux --write` writes in the state
// directory.
const PluginConfName = "plugin.tmux.conf"

// Default plugin mode keys, in the prefix table.
const (
	DefaultPluginLaunchKey = "y"
	DefaultPluginListKey   = "u"
)

// OpenPluginServer loads the configuration and returns the tmux server the
// process runs in, from $TMUX. It neither writes a tmux configuration nor
// starts a server.
func OpenPluginServer(ctx context.Context, h Host) (*Server, error) {
	socket, ok := tmux.SocketFromEnv(h.Getenv("TMUX"))
	if !ok {
		return nil, fmt.Errorf("%w: run it from a tmux key binding or a tmux pane ($TMUX is not set)", ErrNotInTmux)
	}
	paths, cfg, err := LoadConfig(h)
	if err != nil {
		return nil, err
	}
	env := ServerEnviron(h.Environ)
	v, err := tmux.New(tmux.Options{Bin: h.TmuxBin, Env: env}).Version(ctx)
	if err != nil {
		return nil, err
	}
	if !v.Supported() {
		return nil, fmt.Errorf("%w: found %s, lyna-tmux needs %d.%d or newer", ErrTmuxTooOld, v, tmux.MinMajor, tmux.MinMinor)
	}
	paths.State = filepath.Join(paths.State, pluginTmuxStateDir)
	return &Server{
		Client:  tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: env}),
		Paths:   paths,
		Config:  cfg,
		Version: v,
	}, nil
}

// PluginRequest holds the command-line choices for plugin mode. A nil key
// takes the server's @claude_* option, or the default when that is unset; an
// empty key leaves the prefix key alone.
type PluginRequest struct {
	LaunchKey, ListKey *string
	NoBell             bool
}

// PluginOptionsFor resolves plugin mode options from the command line, the
// @claude_* options of the server (so a configuration written for the
// upstream plugin keeps working) and the defaults. Bells are forwarded unless
// --no-bell is given or @claude_forward_bell is set to anything but "on".
func PluginOptionsFor(bin string, user tmux.PluginUserOptions, req PluginRequest) (tmux.PluginOptions, error) {
	opts := tmux.PluginOptions{
		Bin:         bin,
		LaunchKey:   pluginTmuxKey(req.LaunchKey, user.LaunchKey, DefaultPluginLaunchKey),
		ListKey:     pluginTmuxKey(req.ListKey, user.ListKey, DefaultPluginListKey),
		ForwardBell: !req.NoBell && (user.ForwardBell == "" || user.ForwardBell == "on"),
	}
	if !filepath.IsAbs(bin) {
		return tmux.PluginOptions{}, fmt.Errorf("lyna-tmux path %q is not absolute", bin)
	}
	for _, k := range []struct{ what, key string }{{"launch key", opts.LaunchKey}, {"list key", opts.ListKey}} {
		if len(k.key) > 32 || strings.ContainsFunc(k.key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return tmux.PluginOptions{}, fmt.Errorf("%s %q is not a tmux key name", k.what, k.key)
		}
	}
	if opts.LaunchKey != "" && opts.LaunchKey == opts.ListKey {
		return tmux.PluginOptions{}, fmt.Errorf("the launch and list keys are both %q", opts.LaunchKey)
	}
	return opts, nil
}

func pluginTmuxKey(flag *string, option, fallback string) string {
	switch {
	case flag != nil:
		return *flag
	case option != "":
		return option
	}
	return fallback
}

// ApplyPlugin installs plugin mode on the server: prefix key bindings and the
// bell forwarding hook. Applying it again replaces the same bindings and hook
// slot, so it is idempotent.
func (s *Server) ApplyPlugin(ctx context.Context, h Host, req PluginRequest) (tmux.PluginOptions, error) {
	user, err := s.Client.ReadPluginOptions(ctx)
	if err != nil {
		return tmux.PluginOptions{}, err
	}
	opts, err := PluginOptionsFor(h.Exe, user, req)
	if err != nil {
		return tmux.PluginOptions{}, err
	}
	_, err = s.Client.Batch(ctx, tmux.PluginSeq(opts)...)
	return opts, err
}

// WritePluginConf writes the plugin mode configuration into the private state
// directory and returns its path.
func WritePluginConf(h Host, opts tmux.PluginOptions) (string, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return "", err
	}
	if err := fsx.EnsurePrivateDir(paths.State); err != nil {
		return "", err
	}
	path := filepath.Join(paths.State, PluginConfName)
	return path, fsx.WriteFileAtomic(path, []byte(tmux.PluginConf(opts)), fsx.PrivateFile)
}

// PluginSourceLine is the tmux configuration line that loads the file at path.
// source-file expands its argument as a glob, so the path is glob-escaped
// before it is quoted.
func PluginSourceLine(path string) string {
	return "source-file " + tmux.ConfToken(tmux.GlobEscape(path))
}
