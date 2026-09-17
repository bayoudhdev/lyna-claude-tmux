package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

func TestSetupCLI(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, e *cliEnv)
		keys     string
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
		// wantConfig is the configuration file content afterwards; "-" means
		// no file.
		wantConfig string
	}{
		{
			name: "not a terminal", args: []string{"setup"}, wantCode: 1,
			setup:      func(t *testing.T, e *cliEnv) { t.Helper(); e.term.Interactive = false },
			errHas:     []string{"setup needs a terminal", "lmux config edit", "lmux config init"},
			wantConfig: "-",
		},
		{name: "extra argument", args: []string{"setup", "now"}, wantCode: 1, errHas: []string{`unknown command "now"`}, wantConfig: "-"},
		{
			name: "cancel writes nothing", args: []string{"setup"}, keys: "\x03",
			outHas: []string{"Setup canceled; nothing was written"}, wantConfig: "-",
		},
		{
			name: "cancel keeps an existing file", args: []string{"setup"}, keys: "\x03",
			setup:      func(t *testing.T, e *cliEnv) { t.Helper(); setupWriteConfig(t, e, "[ui]\ntheme = \"ansi\"\n") },
			outHas:     []string{"Setup canceled; nothing was written"},
			wantConfig: "[ui]\ntheme = \"ansi\"\n",
		},
		{
			name: "linked configuration is refused before the wizard", args: []string{"setup"}, keys: "\x03", wantCode: 1,
			setup: func(t *testing.T, e *cliEnv) {
				t.Helper()
				target := filepath.Join(e.host.Home, "dotfiles.toml")
				if err := os.WriteFile(target, []byte("[ui]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(e.host.Home, "config"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, setupConfigPath(e)); err != nil {
					t.Fatal(err)
				}
			},
			errHas:     []string{"symbolic link", "config edit"},
			wantConfig: "[ui]\n",
		},
		{
			name: "invalid configuration file", args: []string{"setup"}, wantCode: 1,
			setup:      func(t *testing.T, e *cliEnv) { t.Helper(); setupWriteConfig(t, e, "[ui]\ntheme = \"neon\"\n") },
			errHas:     []string{"neon"},
			wantConfig: "[ui]\ntheme = \"neon\"\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			if tc.setup != nil {
				tc.setup(t, e)
			}
			code, stdout, stderr := agentsCLI(t, e, strings.NewReader(tc.keys), tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %q\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%q", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if tc.wantCode != 0 && strings.Contains(stdout, "Color depth") {
				t.Fatalf("the wizard ran before the refusal:\n%q", stdout)
			}
			data, err := os.ReadFile(setupConfigPath(e))
			switch {
			case tc.wantConfig == "-":
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("configuration file written: %q, %v", data, err)
				}
			case err != nil || string(data) != tc.wantConfig:
				t.Fatalf("configuration file %q (%v), want %q", data, err, tc.wantConfig)
			}
			if _, err := os.Lstat(setupConfigPath(e) + ".bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backup written: %v", err)
			}
		})
	}
}

func setupConfigPath(e *cliEnv) string { return filepath.Join(e.host.Home, "config", "config.toml") }

func setupWriteConfig(t *testing.T, e *cliEnv, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(setupConfigPath(e)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setupConfigPath(e), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setupDeps are what the steps after the wizard need: the working directory
// they look for a project in, PATH lookups, a terminal to ask on and a runner.
// The terminal is not interactive, so a step that asks is skipped unless the
// case passes --yes.
func setupDeps(e *cliEnv) Deps {
	return Deps{
		Host: func() (app.Host, error) { return e.host, nil },
		// Nothing the steps look for is on this PATH, so a case reports the
		// missing tool rather than whatever the machine happens to have.
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		Getwd:    func() (string, error) { return e.cwd, nil },
		Terminal: func() Terminal { return Terminal{} },
		Run:      func(context.Context, []string, Streams) error { return nil },
		Now:      time.Now,
	}
}

// setupWith returns a copy of cfg with edit applied, for a case that differs
// from the saved configuration in one field.
func setupWith(cfg config.Config, edit func(*config.Config)) config.Config {
	edit(&cfg)
	return cfg
}

// setupFinished is a wizard that has already finished with a result.
type setupFinished struct {
	res  tui.SetupResult
	done bool
}

func (m setupFinished) Init() tea.Cmd                       { return tea.Quit }
func (m setupFinished) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m setupFinished) View() tea.View                      { return tea.NewView("") }
func (m setupFinished) Result() (tui.SetupResult, bool)     { return m.res, m.done }

// TestSetupWizardSaves runs a finished wizard through a real program and
// checks what is written, applied and printed.
func TestSetupWizardSaves(t *testing.T) {
	edited := config.Default()
	edited.UI.Theme = "light"
	edited.UI.StatusPosition = "top"
	changes := []tui.ConfigChange{{Key: "ui.theme", From: "lyna", To: "light"}, {Key: "claude.model", From: "", To: "opus"}}
	cases := []struct {
		name       string
		model      setupFinished
		running    bool
		existing   string
		noTmux     bool
		outHas     []string
		errHas     []string
		wantTheme  string
		wantBackup bool
		// yes carries out what the saved settings need without asking, and
		// wantFile is a path that must exist afterwards.
		yes      bool
		wantFile []string
	}{
		{
			name:  "saved to a running server",
			model: setupFinished{res: tui.SetupResult{Config: edited, Changes: changes, Saved: true}, done: true}, running: true, existing: "[ui]\ntheme = \"ansi\"\n",
			outHas:    []string{"Saved the previous file to", "Wrote ", "ui.theme: lyna -> light", "claude.model: (default) -> opus", "Applied to the running lyna-tmux server", "Shell completion for zsh", "Alt keys"},
			wantTheme: "light", wantBackup: true,
		},
		{
			name:      "saved while the server is stopped",
			model:     setupFinished{res: tui.SetupResult{Config: edited, Saved: true}, done: true},
			outHas:    []string{"Wrote ", "the next workspace starts with these settings"},
			wantTheme: "light",
		},
		{
			name:      "saved without tmux warns",
			model:     setupFinished{res: tui.SetupResult{Config: edited, Saved: true}, done: true},
			noTmux:    true,
			outHas:    []string{"Wrote "},
			errHas:    []string{"Warning: could not apply the settings", "not installed"},
			wantTheme: "light",
		},
		{
			name:   "not saved",
			model:  setupFinished{res: tui.SetupResult{Config: edited}, done: true},
			outHas: []string{"Setup canceled; nothing was written"},
		},
		{
			name:   "program ended before the wizard finished",
			model:  setupFinished{res: tui.SetupResult{Config: edited, Saved: true}},
			outHas: []string{"Setup canceled; nothing was written"},
		},
		{
			name:  "completion is written for the login shell",
			model: setupFinished{res: tui.SetupResult{Config: setupWith(edited, func(c *config.Config) { c.Review.Editor = "user" }), Saved: true}, done: true},
			yes:   true,
			// The line the shell startup file still needs stays the user's.
			outHas:    []string{"Wrote ", "_lmux", "One line is still yours to add"},
			wantFile:  []string{".zfunc", "_lmux"},
			wantTheme: "light",
		},
		{
			name:      "a declined completion writes nothing",
			model:     setupFinished{res: tui.SetupResult{Config: setupWith(edited, func(c *config.Config) { c.Review.Editor = "user" }), Saved: true}, done: true},
			outHas:    []string{"Skipped; the commands are above."},
			wantTheme: "light",
		},
		{
			name: "process isolation names the sandbox runtime it never installs",
			model: setupFinished{res: tui.SetupResult{Config: setupWith(edited, func(c *config.Config) {
				c.Review.Editor, c.Sandbox.Isolation = "user", "process"
			}), Saved: true}, done: true},
			outHas:    []string{"srt", "npm install -g @anthropic-ai/sandbox-runtime"},
			wantTheme: "light",
		},
		{
			name: "container isolation says how to give a project one",
			model: setupFinished{res: tui.SetupResult{Config: setupWith(edited, func(c *config.Config) {
				c.Review.Editor, c.Sandbox.Isolation = "user", "container"
			}), Saved: true}, done: true},
			outHas:    []string{"lmux sandbox devcontainer init"},
			wantTheme: "light",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			e.setenv("SHELL", "/bin/zsh")
			if tc.existing != "" {
				setupWriteConfig(t, e, tc.existing)
			}
			if tc.running {
				e.start(t, "api", "/src/api")
			}
			if tc.noTmux {
				e.host.TmuxBin = filepath.Join(e.host.Home, "missing-tmux")
			}
			term := newAgentsTerm(t)
			ui := rootUI{host: e.host, config: config.Default(), term: e.term}
			d := setupDeps(e)
			wait := term.start(t, func(cmd *cobra.Command) error {
				return d.setupWizard(cmd, ui, tc.model, app.ReviewPlugin{}, tc.yes)
			})
			if err := wait(); err != nil {
				t.Fatal(err)
			}
			out, errOut := term.out.String(), term.errOut.String()
			for _, s := range tc.outHas {
				if !strings.Contains(out, s) {
					t.Fatalf("stdout missing %q:\n%s", s, out)
				}
			}
			for _, s := range tc.errHas {
				if !strings.Contains(errOut, s) {
					t.Fatalf("stderr missing %q:\n%s", s, errOut)
				}
			}
			cfg, found, err := config.Load(setupConfigPath(e))
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.wantTheme == "" && found && tc.existing == "":
				t.Fatal("configuration written for an unsaved wizard")
			case tc.wantTheme != "" && cfg.UI.Theme != tc.wantTheme:
				t.Fatalf("saved theme %q, want %q", cfg.UI.Theme, tc.wantTheme)
			}
			if _, err := os.Stat(setupConfigPath(e) + ".bak"); (err == nil) != tc.wantBackup {
				t.Fatalf("backup present = %v, want %v", err == nil, tc.wantBackup)
			}
			if len(tc.wantFile) > 0 {
				path := filepath.Join(append([]string{e.host.Home}, tc.wantFile...)...)
				data, err := os.ReadFile(path)
				if err != nil || len(data) == 0 {
					t.Fatalf("%s: %v (%d bytes)\n%s", path, err, len(data), out)
				}
			}
			if tc.running {
				got, err := agentsServer(e).ShowOption(tmuxtest.Context(t), "-g", "", "status-position")
				if err != nil || got != "top" {
					t.Fatalf("running server status-position %q (%v), want top", got, err)
				}
			}
		})
	}
}

func TestSetupGuidance(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		goos    string
		altKeys bool
		has     []string
		lacks   []string
	}{
		{
			name: "zsh on macOS in Ghostty", env: map[string]string{"SHELL": "/bin/zsh", "TERM_PROGRAM": "ghostty"}, goos: "darwin", altKeys: true,
			has:   []string{"Shell completion for zsh", "lmux completion zsh > ~/.zfunc/_lmux", "fpath=(~/.zfunc $fpath)", "Alt keys need Option sent as Meta in Ghostty", "macos-option-as-alt = true"},
			lacks: []string{"completion bash", "completion fish"},
		},
		{
			name: "bash on Linux", env: map[string]string{"SHELL": "/usr/bin/bash", "TERM_PROGRAM": "WezTerm"}, goos: "linux", altKeys: true,
			has:   []string{"lmux completion bash > ~/.local/share/bash-completion/completions/lmux", "Alt keys in WezTerm", "Alt sends Meta by default"},
			lacks: []string{"completion zsh", "need Option"},
		},
		{
			name: "fish in Apple Terminal", env: map[string]string{"SHELL": "/opt/homebrew/bin/fish", "TERM_PROGRAM": "Apple_Terminal"}, goos: "darwin", altKeys: true,
			has: []string{"lmux completion fish > ~/.config/fish/completions/lmux.fish", `enable "Use Option as Meta Key"`},
		},
		{
			name: "WezTerm on macOS already sends Meta", env: map[string]string{"SHELL": "/bin/zsh", "TERM_PROGRAM": "WezTerm"}, goos: "darwin", altKeys: true,
			has: []string{"Alt keys in WezTerm", "send_composed_key_when_left_alt_is_pressed"}, lacks: []string{"need Option"},
		},
		{
			name: "unknown shell lists every shell", env: map[string]string{"SHELL": "/bin/tcsh"}, goos: "darwin", altKeys: true,
			has: []string{"Shell completion for bash", "Shell completion for zsh", "Shell completion for fish", "unknown terminal"},
		},
		{
			name: "alt keys off", env: map[string]string{"SHELL": "/bin/zsh", "TERM_PROGRAM": "ghostty"}, goos: "darwin",
			has: []string{"Shell completion for zsh"}, lacks: []string{"Alt keys", "macos-option-as-alt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := setupGuidance(func(k string) string { return tc.env[k] }, tc.goos, tc.altKeys)
			for _, s := range tc.has {
				if !strings.Contains(got, s) {
					t.Errorf("guidance missing %q:\n%s", s, got)
				}
			}
			for _, s := range tc.lacks {
				if strings.Contains(got, s) {
					t.Errorf("guidance has %q:\n%s", s, got)
				}
			}
		})
	}
}

// TestSetupStartsFromLoadedConfig checks the command hands the loaded file to
// the wizard: its first frame selects the configured theme.
func TestSetupStartsFromLoadedConfig(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		selected string
	}{
		{name: "defaults without a file", selected: "> lyna"},
		{name: "configured theme", config: "[ui]\ntheme = \"ansi\"\n", selected: "> ansi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			if tc.config != "" {
				setupWriteConfig(t, e, tc.config)
			}
			term := newAgentsTerm(t)
			wait := term.runCLI(t, e, "setup")
			tmuxtest.WaitFor(t, "wizard drawn", func() bool { return strings.Contains(ansi.Strip(term.out.String()), "Color depth") })
			frame := ansi.Strip(term.out.String())
			term.send(t, "\x03")
			if code := wait(); code != 0 {
				t.Fatalf("exit %d: %s", code, term.errOut.String())
			}
			if !strings.Contains(frame, tc.selected) {
				t.Fatalf("first frame does not select %q:\n%s", tc.selected, frame)
			}
			if !strings.Contains(term.out.String(), "Setup canceled; nothing was written") {
				t.Fatalf("output:\n%s", term.out.String())
			}
		})
	}
}
