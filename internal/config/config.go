// Package config loads, validates and writes the user configuration file
// (config.toml). There is deliberately no per-project configuration: a
// repository must never be able to change how lyna-tmux launches Claude.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// MaxFileSize bounds the configuration file read.
const MaxFileSize = 1 << 20

// Config is the whole configuration file.
type Config struct {
	UI        UI                `toml:"ui"`
	Workspace Workspace         `toml:"workspace"`
	Claude    Claude            `toml:"claude"`
	Sandbox   Sandbox           `toml:"sandbox"`
	Popup     Popup             `toml:"popup"`
	Review    Review            `toml:"review"`
	Layouts   map[string]Layout `toml:"layouts,omitempty"`
}

// UI controls the look and the input model of the tmux workspace.
type UI struct {
	Theme            string `toml:"theme"`
	Icons            string `toml:"icons"`
	Color            string `toml:"color"`
	StatusPosition   string `toml:"status_position"`
	Clock            bool   `toml:"clock"`
	AltKeys          bool   `toml:"alt_keys"`
	Mouse            bool   `toml:"mouse"`
	AllowPassthrough bool   `toml:"allow_passthrough"`
	FocusEvents      bool   `toml:"focus_events"`
	AgentsSidebar    string `toml:"agents_sidebar"`
}

// Workspace controls sessions and panes.
type Workspace struct {
	Layout       string `toml:"layout"`
	SplitRatio   int    `toml:"split_ratio"`
	AgentPanes   int    `toml:"agent_panes"`
	HistoryLimit int    `toml:"history_limit"`
	Prefix       string `toml:"prefix"`
	Shell        string `toml:"shell"`
}

// Claude controls how Claude Code is launched in managed panes.
type Claude struct {
	Command        string   `toml:"command"`
	Args           []string `toml:"args,omitempty"`
	Model          string   `toml:"model"`
	Effort         string   `toml:"effort"`
	PermissionMode string   `toml:"permission_mode"`
	Statusline     string   `toml:"statusline"`
	Fullscreen     bool     `toml:"fullscreen"`
	Teams          bool     `toml:"teams"`
	TeammateMode   string   `toml:"teammate_mode"`
	WorktreeBase   string   `toml:"worktree_base"`
	WorkflowSize   string   `toml:"workflow_size"`
	Bell           bool     `toml:"bell"`
	AddDirs        []string `toml:"add_dirs,omitempty"`
	MCPConfig      []string `toml:"mcp_config,omitempty"`
	PluginDirs     []string `toml:"plugin_dirs,omitempty"`
}

// Sandbox selects the sandbox profile, the isolation level and user additions
// merged into the generated Claude settings.
type Sandbox struct {
	Profile          string   `toml:"profile"`
	Isolation        string   `toml:"isolation"`
	AllowWrite       []string `toml:"allow_write,omitempty"`
	DenyRead         []string `toml:"deny_read,omitempty"`
	AllowedDomains   []string `toml:"allowed_domains,omitempty"`
	ExcludedCommands []string `toml:"excluded_commands,omitempty"`
}

// Popup controls the per-directory popup sessions.
type Popup struct {
	Width         string `toml:"width"`
	Height        string `toml:"height"`
	SessionPrefix string `toml:"session_prefix"`
}

// Review controls the live code review of the changes in a project.
type Review struct {
	// Editor is isolated (a private Neovim with a pinned, verified
	// codediff.nvim) or user (your own Neovim and plugin setup).
	Editor string `toml:"editor"`
	// Layout is how diffs are drawn: default, inline or side-by-side.
	Layout string `toml:"layout"`
}

// Layout is a user-defined pane arrangement.
type Layout struct {
	Panes []Pane `toml:"panes"`
}

// Pane is one pane of a custom layout. The first pane is the window itself;
// every later pane splits an earlier one.
type Pane struct {
	// Role is claude, shell, changes, review or command.
	Role string `toml:"role"`
	// Split is right or down; required for every pane but the first.
	Split string `toml:"split,omitempty"`
	// Size is the new pane's share of the split pane in percent (10-90);
	// 0 splits in half.
	Size int `toml:"size,omitempty"`
	// Parent is the 1-based index of the earlier pane to split; 0 means the
	// previous pane.
	Parent int `toml:"parent,omitempty"`
	// Command is the shell command of a command pane.
	Command string `toml:"command,omitempty"`
	// Worktree runs a claude pane in its own git worktree.
	Worktree bool `toml:"worktree,omitempty"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		UI: UI{
			Theme:          "lyna",
			Icons:          "auto",
			Color:          "auto",
			StatusPosition: "bottom",
			Clock:          true,
			AltKeys:        true,
			Mouse:          true,
			AgentsSidebar:  SidebarAuto,
		},
		Workspace: Workspace{
			Layout:       "auto",
			SplitRatio:   62,
			AgentPanes:   layout.DefaultAgentPanes,
			HistoryLimit: 100000,
			Prefix:       "C-b",
		},
		Claude: Claude{
			Statusline:   "auto",
			TeammateMode: claudecfg.TeammateLmux,
			Bell:         true,
		},
		Sandbox: Sandbox{
			Profile:   "standard",
			Isolation: "bash",
		},
		Popup: Popup{
			Width:         "90%",
			Height:        "90%",
			SessionPrefix: "claude-",
		},
		Review: Review{
			Editor: string(review.EditorIsolated),
			Layout: "default",
		},
	}
}

// Decode parses a configuration document on top of the defaults: keys that
// are absent keep their default value, unknown keys are an error, and the
// result is validated.
func Decode(data []byte) (Config, error) {
	cfg := Default()
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, decodeError(err)
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Load reads and decodes the configuration file. A missing file yields the
// defaults with found set to false. The file may be a symbolic link so it can
// live in a dotfiles repository.
func Load(path string) (cfg Config, found bool, err error) {
	data, err := fsx.ReadFileLimited(path, MaxFileSize)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err = Decode(data)
	if err != nil {
		return Config{}, true, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

// Marshal encodes a configuration as TOML. It does not keep comments; use
// Template for a documented starting file.
func Marshal(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf).SetIndentTables(false)
	if err := enc.Encode(cfg); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return buf.Bytes(), nil
}

// normalize turns empty lists into nil so decoded and default values compare
// equal and Marshal omits them.
func (c *Config) normalize() {
	for _, list := range []*[]string{
		&c.Claude.Args, &c.Claude.AddDirs, &c.Claude.MCPConfig, &c.Claude.PluginDirs,
		&c.Sandbox.AllowWrite, &c.Sandbox.DenyRead, &c.Sandbox.AllowedDomains, &c.Sandbox.ExcludedCommands,
	} {
		if len(*list) == 0 {
			*list = nil
		}
	}
	if len(c.Layouts) == 0 {
		c.Layouts = nil
	}
}

// DecodeProblem is one syntax or unknown-key error with its position.
type DecodeProblem struct {
	Line    int
	Column  int
	Key     string
	Message string
}

func (p DecodeProblem) String() string {
	switch {
	case p.Key != "" && p.Line > 0:
		return fmt.Sprintf("line %d: %s: %s", p.Line, p.Key, p.Message)
	case p.Line > 0:
		return fmt.Sprintf("line %d, column %d: %s", p.Line, p.Column, p.Message)
	}
	return p.Message
}

// DecodeErrors lists every decoding problem of a document.
type DecodeErrors []DecodeProblem

func (d DecodeErrors) Error() string {
	lines := make([]string, len(d))
	for i, p := range d {
		lines[i] = p.String()
	}
	return "invalid config: " + strings.Join(lines, "; ")
}

func decodeError(err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		out := make(DecodeErrors, 0, len(strict.Errors))
		for i := range strict.Errors {
			e := &strict.Errors[i]
			line, col := e.Position()
			out = append(out, DecodeProblem{Line: line, Column: col, Key: strings.Join(e.Key(), "."), Message: "unknown key"})
		}
		return out
	}
	var derr *toml.DecodeError
	if errors.As(err, &derr) {
		line, col := derr.Position()
		return DecodeErrors{{Line: line, Column: col, Key: strings.Join(derr.Key(), "."), Message: strings.TrimPrefix(derr.Error(), "toml: ")}}
	}
	return DecodeErrors{{Message: err.Error()}}
}

// Choices returns the accepted values of an enumerated key, for completions
// and the setup wizard. It returns nil for keys that are not enumerations.
func Choices(key string) []string {
	if c, ok := choices[key]; ok {
		return slices.Clone(c)
	}
	return nil
}

// When the agents rail is on screen (ui.agents_sidebar).
const (
	// SidebarAuto opens the rail with the first agent that joins the workspace
	// and takes it away with the last one.
	SidebarAuto = "auto"
	// SidebarAlways opens the rail with the workspace and keeps it.
	SidebarAlways = "always"
	// SidebarKey opens it only when the key asks for it.
	SidebarKey = "key"
	// SidebarOff never opens it; the key still does.
	SidebarOff = "off"
)
