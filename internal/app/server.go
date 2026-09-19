package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Host is what the use cases need from the running process.
type Host struct {
	Getenv  func(string) string
	Environ []string
	Home    string
	// Exe is the absolute path of the lyna-tmux binary tmux runs for popups,
	// menus and hooks.
	Exe string
	// TmuxBin is the tmux binary; empty resolves "tmux" on PATH.
	TmuxBin string
	// LookPath resolves programs such as claude; nil uses exec.LookPath.
	LookPath func(name string) (string, error)
}

// Server is the dedicated lyna-tmux tmux server with its generated
// configuration.
type Server struct {
	Client *tmux.Client
	// SocketName is the tmux -L name of the server.
	SocketName string
	Paths      xdg.Paths
	Config     config.Config
	Version    tmux.Version
	// Fingerprint identifies the generated configuration file content.
	Fingerprint string
}

// ErrTmuxTooOld reports a tmux older than the minimum supported version.
var ErrTmuxTooOld = errors.New("tmux is too old")

// maxConfSize bounds the read of an existing generated configuration.
const maxConfSize = 1 << 20

// OpenServer loads the configuration, checks the tmux version, and writes the
// generated tmux configuration when its content changed. It does not start or
// contact the server.
func OpenServer(ctx context.Context, h Host) (*Server, error) {
	paths, cfg, err := LoadConfig(h)
	if err != nil {
		return nil, err
	}
	env := ServerEnviron(h.Environ)
	v, err := tmux.New(tmux.Options{Bin: h.TmuxBin, Env: env}).Version(ctx)
	if errors.Is(err, tmux.ErrNotInstalled) {
		return nil, fmt.Errorf("%w: install tmux %d.%d or newer (run `lmux doctor` for the exact command)", err, tmux.MinMajor, tmux.MinMinor)
	}
	if err != nil {
		return nil, err
	}
	if !v.Supported() {
		return nil, fmt.Errorf("%w: found %s, lyna-tmux needs %d.%d or newer", ErrTmuxTooOld, v, tmux.MinMajor, tmux.MinMinor)
	}
	socket, err := serverSocketName(h)
	if err != nil {
		return nil, err
	}
	opts, err := confOptions(cfg, v, h, paths)
	if err != nil {
		return nil, err
	}
	conf := tmux.GenerateConf(opts)
	if err := writeIfChanged(paths.State, paths.TmuxConf(), []byte(conf)); err != nil {
		return nil, err
	}
	return &Server{
		Client:      tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: tmux.Socket{Name: socket}, Config: paths.TmuxConf(), Env: env}),
		SocketName:  socket,
		Paths:       paths,
		Config:      cfg,
		Version:     v,
		Fingerprint: tmux.ConfFingerprint(conf),
	}, nil
}

// serverSocketName is the -L name of the lyna-tmux server: the one the
// environment names, or the default.
func serverSocketName(h Host) (string, error) {
	socket := h.Getenv(session.EnvSocketName)
	if socket == "" {
		return tmux.DefaultSocketName, nil
	}
	if err := session.Validate(socket); err != nil {
		return "", fmt.Errorf("%s: %w", session.EnvSocketName, err)
	}
	return socket, nil
}

// LoadConfig resolves the lyna-tmux directories and loads the configuration
// file, or the defaults when there is none.
func LoadConfig(h Host) (xdg.Paths, config.Config, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return xdg.Paths{}, config.Config{}, err
	}
	cfg, _, err := config.Load(paths.ConfigFile())
	if err != nil {
		return xdg.Paths{}, config.Config{}, err
	}
	return paths, cfg, nil
}

// confOptions resolves the configuration into generator options for the
// terminal lyna-tmux runs in.
func confOptions(cfg config.Config, v tmux.Version, h Host, paths xdg.Paths) (tmux.ConfOptions, error) {
	palette, err := theme.Get(cfg.UI.Theme)
	if err != nil {
		return tmux.ConfOptions{}, err
	}
	depth := theme.DetectDepth(h.Getenv)
	if cfg.UI.Color != "auto" {
		if depth, err = theme.ParseDepth(cfg.UI.Color); err != nil {
			return tmux.ConfOptions{}, err
		}
	}
	icons, err := theme.GetIcons(theme.ResolveIcons(cfg.UI.Icons, h.Getenv))
	if err != nil {
		return tmux.ConfOptions{}, err
	}
	return tmux.ConfOptions{
		Version: v,
		Look: tmux.Look{
			Palette: palette,
			Depth:   depth,
			Icons:   icons,
			Clock:   cfg.UI.Clock,
			Buttons: cfg.UI.Mouse && v.Has(tmux.FeatureUserRanges),
		},
		Env: tmux.Env{
			Bin:          h.Exe,
			ConfPath:     paths.TmuxConf(),
			PopupWidth:   cfg.Popup.Width,
			PopupHeight:  cfg.Popup.Height,
			RailWidth:    cfg.UI.SidebarWidth,
			NoAgentsRail: !cfg.UI.RailKey(),
			Bindings: keys.Defaults(keys.Options{
				AltKeys:      cfg.UI.AltKeys,
				Prefix:       cfg.Workspace.Prefix,
				NoAgentsRail: !cfg.UI.RailKey(),
			}),
		},
		Prefix:           cfg.Workspace.Prefix,
		Mouse:            cfg.UI.Mouse,
		AllowPassthrough: cfg.UI.AllowPassthrough,
		FocusEvents:      cfg.UI.FocusEvents,
		Bell:             cfg.Claude.Bell,
		StatusPosition:   cfg.UI.StatusPosition,
		HistoryLimit:     cfg.Workspace.HistoryLimit,
		Shell:            cfg.Workspace.Shell,
		LocalConf:        paths.LocalTmuxConf(),
	}, nil
}

// writeIfChanged writes data to path in the private state directory unless
// the file already holds exactly data, so an unchanged configuration keeps
// its modification time.
func writeIfChanged(dir, path string, data []byte) error {
	if err := fsx.EnsurePrivateDir(dir); err != nil {
		return err
	}
	old, err := fsx.ReadFileNoFollow(path, maxConfSize)
	if err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fsx.ErrTooLarge) {
		return err
	}
	return fsx.WriteFileAtomic(path, data, 0o600)
}

// Sync makes a running server use the current configuration: when the
// fingerprint the server recorded differs, the file is sourced again. It
// reports whether a server is running; a stopped server loads the file when it
// starts, after which MarkLoaded records the fingerprint.
func (s *Server) Sync(ctx context.Context) (running bool, err error) {
	loaded, err := s.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash)
	if errors.Is(err, tmux.ErrNoServer) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if loaded == s.Fingerprint {
		return true, nil
	}
	_, err = s.Client.Batch(ctx,
		tmux.Command{"source-file", tmux.GlobEscape(s.Paths.TmuxConf())},
		tmux.Command{"set-option", "-g", tmux.OptConfHash, s.Fingerprint},
	)
	return true, err
}

// MarkLoaded records that the server started from the current configuration.
func (s *Server) MarkLoaded(ctx context.Context) error {
	_, err := s.Client.Run(ctx, "set-option", "-g", tmux.OptConfHash, s.Fingerprint)
	return err
}
