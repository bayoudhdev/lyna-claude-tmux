package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Manual uninstall steps that lyna-tmux cannot do for the user.
const (
	// UninstallClaudePlugin is the Claude Code command that removes the companion plugin.
	UninstallClaudePlugin = "/plugin uninstall lyna-tmux@lyna-tmux"
	// UninstallTPMLine is the tmux plugin manager entry of plugin mode.
	UninstallTPMLine = "set -g @plugin 'bayoudhdev/lyna-claude-tmux'"
)

// ErrUninstallInside reports an uninstall started from a pane of the server it
// would stop.
var ErrUninstallInside = errors.New("uninstall runs from a pane of the lyna-tmux server it stops")

// UninstallPlan is what uninstall removes.
type UninstallPlan struct {
	// SocketName is the tmux -L name of the server that is stopped.
	SocketName string
	// Dirs are the existing lyna-tmux directories removed, in order.
	Dirs []string
	// Themes are the Claude Code theme files lyna-tmux generated and nobody changed since.
	Themes []string
	// KeptConfig is the configuration directory left in place without purge,
	// empty when it is removed or absent.
	KeptConfig string
	// Binary is the lyna-tmux executable, which the user removes.
	Binary string
}

// UninstallPlanFor lists what uninstall removes. purge adds the configuration
// directory. Nothing is changed.
func UninstallPlanFor(h Host, purge bool) (UninstallPlan, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return UninstallPlan{}, err
	}
	p := UninstallPlan{SocketName: tmux.DefaultSocketName, Binary: h.Exe}
	if name := h.Getenv(session.EnvSocketName); name != "" {
		if err := session.Validate(name); err != nil {
			return UninstallPlan{}, fmt.Errorf("%s: %w", session.EnvSocketName, err)
		}
		p.SocketName = name
	}
	dirs := []string{paths.State, paths.Cache, paths.Data}
	if purge {
		dirs = append(dirs, paths.Config)
	}
	for _, dir := range dirs {
		if err := uninstallCheckDir(h, dir); err != nil {
			return UninstallPlan{}, err
		}
		if _, err := os.Lstat(dir); err == nil {
			p.Dirs = append(p.Dirs, dir)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return UninstallPlan{}, err
		}
	}
	if _, err := os.Lstat(paths.Config); err == nil && !purge {
		p.KeptConfig = paths.Config
	}
	p.Themes, err = uninstallThemes(xdg.ClaudeHome(h.Getenv, h.Home))
	if err != nil {
		return UninstallPlan{}, err
	}
	return p, nil
}

// uninstallOwned lists, for each directory of the layout, the entries
// lyna-tmux creates there. The names come from the path helpers, so a renamed
// file cannot silently drop out of the list.
func uninstallOwned(paths xdg.Paths) map[string][]string {
	owned := map[string][]string{}
	add := func(dir string, names ...string) {
		clean := filepath.Clean(dir)
		owned[clean] = append(owned[clean], names...)
	}
	log := filepath.Base(paths.LogFile())
	add(paths.State, filepath.Base(paths.TmuxConf()), filepath.Base(paths.SettingsDir()), log, log+".1", pluginTmuxStateDir)
	add(paths.Cache, filepath.Base(paths.AgentsCache()))
	add(paths.Data, filepath.Base(paths.ReviewDir()))
	add(paths.Config, filepath.Base(paths.ConfigFile()), filepath.Base(paths.LocalTmuxConf()))
	return owned
}

// uninstallCheckDir refuses to remove a directory lyna-tmux cannot show it
// owns. Being one of the four resolved directories is not enough on its own:
// LYNA_TMUX_HOME names all four at once, so with it set to a home directory
// they are plain names such as "config" and "state" that the user may have
// been using for years. A directory of the XDG layout carries the application
// name and nothing else is in it; anywhere else every entry must be one
// lyna-tmux writes.
func uninstallCheckDir(h Host, dir string) error {
	clean := filepath.Clean(dir)
	refuse := func(why string) error { return fmt.Errorf("refusing to remove %s: %s", dir, why) }
	if !filepath.IsAbs(clean) || clean == filepath.Dir(clean) || clean == filepath.Clean(h.Home) {
		return refuse("it is not a lyna-tmux directory")
	}
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return err
	}
	owned, known := uninstallOwned(paths)[clean]
	if !known {
		return refuse("it is not a lyna-tmux directory")
	}
	if filepath.Base(clean) == xdg.AppName {
		return nil
	}
	entries, err := os.ReadDir(clean)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !slices.Contains(owned, e.Name()) {
			return refuse(filepath.Join(dir, e.Name()) + " is not something lyna-tmux created")
		}
	}
	return nil
}

// uninstallThemes lists the theme files in the Claude Code themes directory
// that lyna-tmux generated and that are unchanged. A themes directory that is
// a link is not looked into.
func uninstallThemes(claudeHome string) ([]string, error) {
	dir := filepath.Join(claudeHome, claudetheme.DirName)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") && uninstallGeneratedTheme(path) {
			out = append(out, path)
		}
	}
	return out, nil
}

// uninstallGeneratedTheme reports whether path is a regular theme file that
// claudetheme recognizes as generated and unchanged.
func uninstallGeneratedTheme(path string) bool {
	data, err := fsx.ReadFileNoFollow(path, claudetheme.MaxFileBytes)
	return err == nil && claudetheme.Recognize(data)
}

// UninstallResult reports what Apply did.
type UninstallResult struct {
	// ServerStopped reports that a running lyna-tmux server was stopped.
	ServerStopped bool
	Removed       []string
}

// Apply stops the lyna-tmux server of the plan's socket name, then removes the
// review installation, the listed directories and the theme files that are
// still generated and unchanged. It refuses to run from a pane of that server,
// which would stop it midway. Other tmux servers are never contacted.
func (p UninstallPlan) Apply(ctx context.Context, h Host) (UninstallResult, error) {
	var res UninstallResult
	socket := SocketPath(h.Getenv, p.SocketName)
	if current, _, ok := strings.Cut(h.Getenv("TMUX"), ","); ok && filepath.Clean(current) == socket {
		return res, fmt.Errorf("%w; run lyna-tmux uninstall from a terminal outside lyna-tmux", ErrUninstallInside)
	}
	client := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: tmux.Socket{Name: p.SocketName}, Env: ServerEnviron(h.Environ)})
	_, err := client.Run(ctx, "kill-server")
	switch {
	case err == nil:
		res.ServerStopped = true
	case errors.Is(err, tmux.ErrNoServer), errors.Is(err, tmux.ErrNotInstalled):
	default:
		return res, fmt.Errorf("stop the lyna-tmux server: %w", err)
	}
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return res, err
	}
	if err := review.Uninstall(paths); err != nil {
		return res, err
	}
	for _, dir := range p.Dirs {
		if err := uninstallCheckDir(h, dir); err != nil {
			return res, err
		}
		if err := os.RemoveAll(dir); err != nil {
			return res, fmt.Errorf("remove %s: %w", dir, err)
		}
		res.Removed = append(res.Removed, dir)
	}
	for _, theme := range p.Themes {
		// The file is checked again: it may have been edited since the plan.
		if !uninstallGeneratedTheme(theme) {
			continue
		}
		if err := os.Remove(theme); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("remove %s: %w", theme, err)
		}
		res.Removed = append(res.Removed, theme)
	}
	return res, nil
}
