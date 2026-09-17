// Package xdg resolves where lyna-tmux keeps its configuration, data, state and cache.
//
// The XDG base directory variables are honored on every platform so a single
// dotfiles layout works on macOS and Linux alike. LYNA_TMUX_HOME overrides all
// four roots at once, which isolates test runs and demo recordings.
package xdg

import (
	"errors"
	"path/filepath"
)

// AppName is the directory name used under each XDG root.
const AppName = "lyna-tmux"

// Names of the command itself, which are not the directory name: the
// directories keep the name of 1.0.0, so an installation from then is found
// where it already is, without a migration.
const (
	// Command is the name the CLI is installed and invoked under.
	Command = "lmux"
	// CommandWas is the name the command had in 1.0.0. The installers still
	// put it next to Command as a link, so a script or a shell alias written
	// then keeps working.
	CommandWas = "lyna-tmux"
)

// Paths are the absolute directories lyna-tmux owns.
type Paths struct {
	Home   string
	Config string
	Data   string
	State  string
	Cache  string
}

// ErrNoHome is returned when no home directory can be determined.
var ErrNoHome = errors.New("xdg: home directory is unknown")

// Resolve computes the paths from an environment lookup and the user home.
// Relative XDG values are ignored, as the specification requires.
func Resolve(getenv func(string) string, home string) (Paths, error) {
	if root := getenv("LYNA_TMUX_HOME"); root != "" && filepath.IsAbs(root) {
		root = filepath.Clean(root)
		return Paths{
			Home:   pick(home, root),
			Config: filepath.Join(root, "config"),
			Data:   filepath.Join(root, "data"),
			State:  filepath.Join(root, "state"),
			Cache:  filepath.Join(root, "cache"),
		}, nil
	}
	if home == "" || !filepath.IsAbs(home) {
		return Paths{}, ErrNoHome
	}
	home = filepath.Clean(home)
	return Paths{
		Home:   home,
		Config: filepath.Join(base(getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), AppName),
		Data:   filepath.Join(base(getenv("XDG_DATA_HOME"), filepath.Join(home, ".local", "share")), AppName),
		State:  filepath.Join(base(getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state")), AppName),
		Cache:  filepath.Join(base(getenv("XDG_CACHE_HOME"), filepath.Join(home, ".cache")), AppName),
	}, nil
}

func base(value, fallback string) string {
	if value != "" && filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return fallback
}

func pick(home, fallback string) string {
	if home != "" && filepath.IsAbs(home) {
		return filepath.Clean(home)
	}
	return fallback
}

// ConfigFile is the user configuration, config.toml.
func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }

// LocalTmuxConf is the optional user tmux fragment sourced after the generated one.
func (p Paths) LocalTmuxConf() string { return filepath.Join(p.Config, "tmux.local.conf") }

// TmuxConf is the generated tmux configuration.
func (p Paths) TmuxConf() string { return filepath.Join(p.State, "tmux.conf") }

// SettingsDir holds the content-addressed per-launch Claude Code settings files.
func (p Paths) SettingsDir() string { return filepath.Join(p.State, "settings") }

// LogFile is the size-capped diagnostic log written by hooks and background commands.
func (p Paths) LogFile() string { return filepath.Join(p.State, "lyna-tmux.log") }

// ReviewDir holds the isolated review editor setup: the pinned diff plugin and
// its generated init file. It lives under data because reinstalling it needs the network.
func (p Paths) ReviewDir() string { return filepath.Join(p.Data, "review") }

// AgentsCache is the last agent snapshot, used to paint the picker instantly.
func (p Paths) AgentsCache() string { return filepath.Join(p.Cache, "agents.json") }

// ClaudeHome is the Claude Code configuration directory, honoring CLAUDE_CONFIG_DIR.
func ClaudeHome(getenv func(string) string, home string) string {
	if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(home, ".claude")
}
