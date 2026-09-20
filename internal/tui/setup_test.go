package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
)

// Key sequences through the wizard from the default configuration.
var (
	setupLookDefaults      = []string{"enter", "enter", "enter", "enter"}
	setupWorkspaceDefaults = []string{"enter", "y", "y"}
	setupClaudeDefaults    = []string{"enter", "enter", "enter"}
	setupSandboxDefaults   = []string{"enter", "enter"}
)

func seq(parts ...[]string) []tea.Msg {
	var msgs []tea.Msg
	for _, p := range parts {
		msgs = append(msgs, keys(p...)...)
	}
	return msgs
}

func newSetup(t *testing.T, cfg config.Config, width, height int) *SetupModel {
	t.Helper()
	return NewSetup(SetupOptions{Styles: goldenStyles(t), Config: cfg, Width: width, Height: height})
}

// startSetup builds the wizard, runs Init and reports the terminal size, as
// the program does before the first key.
func startSetup(t *testing.T, cfg config.Config, width, height int, msgs ...tea.Msg) (*SetupModel, run) {
	t.Helper()
	m := newSetup(t, cfg, width, height)
	r := drive(t, m, m.Init(), append([]tea.Msg{resize(width, height)}, msgs...)...)
	return m, r
}

func TestSetupFrames(t *testing.T) {
	t.Parallel()
	changed := seq(
		[]string{"down", "down", "enter", "down", "down", "down", "enter", "down", "enter", "down", "down", "enter"},
		[]string{"up", "enter", "n", "y"},
	)
	states := []struct {
		name string
		msgs []tea.Msg
	}{
		{name: "look"},
		{name: "look-changed", msgs: keys("down", "down", "enter", "down", "down", "down")},
		{name: "workspace", msgs: seq(setupLookDefaults)},
		{name: "claude", msgs: seq(setupLookDefaults, setupWorkspaceDefaults)},
		{name: "invalid-model", msgs: append(append(seq(setupLookDefaults, setupWorkspaceDefaults), typeText("bad model!")...), press("enter"))},
		{name: "sandbox", msgs: seq(setupLookDefaults, setupWorkspaceDefaults, setupClaudeDefaults)},
		{name: "review", msgs: append(changed, append(typeText("opus"), seq([]string{"enter", "down", "down", "down", "enter", "enter"}, []string{"down", "enter", "enter"})...)...)},
		{name: "review-unchanged", msgs: seq(setupLookDefaults, setupWorkspaceDefaults, setupClaudeDefaults, setupSandboxDefaults)},
		{name: "saved", msgs: append(changed, seq([]string{"enter", "enter", "enter"}, setupSandboxDefaults, []string{"y"})...)},
		{name: "unchanged", msgs: seq(setupLookDefaults, setupWorkspaceDefaults, setupClaudeDefaults, setupSandboxDefaults, []string{"y"})},
		{name: "canceled", msgs: keys("down", "ctrl+c")},
	}
	for _, size := range sizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				m, _ := startSetup(t, config.Default(), size.width, size.height, st.msgs...)
				assertFrame(t, "setup/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestSetupResults(t *testing.T) {
	t.Parallel()
	custom := config.Default()
	custom.Workspace.Layout = "review"
	custom.Claude.Model = "sonnet"
	custom.Layouts = map[string]config.Layout{"pair": {Panes: []config.Pane{{Role: "claude"}, {Role: "claude", Split: "right"}}}}
	cases := []struct {
		name    string
		cfg     config.Config
		msgs    []tea.Msg
		done    bool
		saved   bool
		quit    bool
		changes []ConfigChange
		check   func(t *testing.T, c config.Config)
	}{
		{
			name: "defaults save nothing", cfg: config.Default(),
			msgs: seq(setupLookDefaults, setupWorkspaceDefaults, setupClaudeDefaults, setupSandboxDefaults, []string{"y"}),
			done: true, saved: true, quit: true,
		},
		{
			name: "every step edits", cfg: config.Default(),
			msgs: append(append(seq(
				[]string{"down", "down", "enter", "down", "down", "down", "enter", "down", "enter", "down", "down", "enter"},
				[]string{"up", "enter", "n", "n"},
			), typeText("  opus ")...), seq(
				[]string{"enter", "down", "down", "down", "enter", "down", "down", "enter"},
				[]string{"down", "enter", "down", "enter"},
				[]string{"y"},
			)...),
			done: true, saved: true, quit: true,
			changes: []ConfigChange{
				{Key: "ui.theme", From: "monokai", To: "light"},
				{Key: "ui.icons", From: "auto", To: "ascii"},
				{Key: "ui.status_style", From: "auto", To: "powerline"},
				{Key: "ui.color", From: "auto", To: "256"},
				{Key: "workspace.layout", From: "auto", To: "git"},
				{Key: "ui.alt_keys", From: "true", To: "false"},
				{Key: "ui.mouse", From: "true", To: "false"},
				{Key: "claude.model", From: "", To: "opus"},
				{Key: "claude.effort", From: "", To: "high"},
				{Key: "claude.statusline", From: "auto", To: "off"},
				{Key: "sandbox.profile", From: "standard", To: "strict"},
				{Key: "sandbox.isolation", From: "bash", To: "process"},
			},
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if err := c.Validate(); err != nil {
					t.Errorf("saved config is invalid: %v", err)
				}
				if c.Claude.Model != "opus" || c.UI.StatusPosition != "bottom" || c.Workspace.SplitRatio != 62 {
					t.Errorf("untouched values changed or model not trimmed: %+v", c)
				}
			},
		},
		{
			name: "review cancel writes nothing", cfg: config.Default(),
			msgs: seq([]string{"down", "down", "enter", "enter", "enter", "enter"}, setupWorkspaceDefaults, setupClaudeDefaults, setupSandboxDefaults, []string{"n"}),
			done: true, quit: true,
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if c.UI.Theme != "monokai" {
					t.Errorf("canceled wizard returned edits: theme %q", c.UI.Theme)
				}
			},
		},
		{name: "ctrl+c cancels", cfg: config.Default(), msgs: keys("down", "enter", "ctrl+c"), done: true, quit: true},
		{name: "invalid model blocks the step", cfg: config.Default(), msgs: append(seq(setupLookDefaults, setupWorkspaceDefaults), append(typeText("no spaces"), press("enter"), press("enter"))...)},
		{
			name: "fixing the model continues", cfg: config.Default(),
			msgs: append(append(seq(setupLookDefaults, setupWorkspaceDefaults), append(typeText("a b"), press("enter"), press("backspace"), press("backspace"))...),
				seq(setupClaudeDefaults, setupSandboxDefaults, []string{"y"})...),
			done: true, saved: true, quit: true,
			changes: []ConfigChange{{Key: "claude.model", From: "", To: "a"}},
		},
		{
			name: "shift+tab goes back", cfg: config.Default(),
			msgs: seq([]string{"enter", "shift+tab", "down", "down", "enter", "enter", "enter", "enter"}, setupWorkspaceDefaults, setupClaudeDefaults, setupSandboxDefaults, []string{"y"}),
			done: true, saved: true, quit: true,
			changes: []ConfigChange{{Key: "ui.theme", From: "monokai", To: "light"}},
		},
		{
			name: "keeps the review layout and custom values", cfg: custom,
			msgs: seq(setupLookDefaults, setupWorkspaceDefaults, setupClaudeDefaults, setupSandboxDefaults, []string{"y"}),
			done: true, saved: true, quit: true,
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if c.Workspace.Layout != "review" || c.Claude.Model != "sonnet" || len(c.Layouts) != 1 {
					t.Errorf("custom values lost: layout %q model %q layouts %d", c.Workspace.Layout, c.Claude.Model, len(c.Layouts))
				}
			},
		},
		{
			name: "offers custom layouts", cfg: custom,
			msgs: seq(setupLookDefaults, []string{"end", "enter", "y", "y"}, setupClaudeDefaults, setupSandboxDefaults, []string{"y"}),
			done: true, saved: true, quit: true,
			changes: []ConfigChange{{Key: "workspace.layout", From: "review", To: "pair"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, r := startSetup(t, tc.cfg, 100, 30, tc.msgs...)
			res, done := m.Result()
			if done != tc.done || res.Saved != tc.saved || r.quit != tc.quit {
				t.Fatalf("done %v saved %v quit %v, want %v %v %v\n%s", done, res.Saved, r.quit, tc.done, tc.saved, tc.quit, ansi.Strip(m.View().Content))
			}
			if !slices.Equal(res.Changes, tc.changes) {
				t.Errorf("changes %+v, want %+v", res.Changes, tc.changes)
			}
			if tc.check != nil {
				tc.check(t, res.Config)
			}
			if !done {
				return
			}
			if !tc.saved && !configEqual(res.Config, tc.cfg) {
				t.Error("canceled wizard returned a modified configuration")
			}
			// A finished wizard ignores further input.
			if _, cmd := m.Update(press("enter")); cmd != nil {
				t.Error("finished wizard returned a command")
			}
		})
	}
}

func configEqual(a, b config.Config) bool {
	x, errA := config.Marshal(a)
	y, errB := config.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

func TestSetupInvalidModelMessage(t *testing.T) {
	t.Parallel()
	m, _ := startSetup(t, config.Default(), 100, 30, append(append(seq(setupLookDefaults, setupWorkspaceDefaults), typeText("bad model!")...), press("enter"))...)
	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "must be a model alias or id") {
		t.Errorf("frame lacks the validation message:\n%s", frame)
	}
	if _, done := m.Result(); done {
		t.Error("invalid model finished the wizard")
	}
}

func TestSetupReviewSummary(t *testing.T) {
	t.Parallel()
	m, _ := startSetup(t, config.Default(), 100, 30, seq(
		[]string{"down", "down", "enter", "enter", "enter", "enter"}, setupWorkspaceDefaults, setupClaudeDefaults, []string{"down", "down", "enter", "enter"},
	)...)
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"ui.theme: monokai -> light", "sandbox.profile: standard -> off"} {
		if !strings.Contains(frame, want) {
			t.Errorf("review lacks %q:\n%s", want, frame)
		}
	}
	if got := newSetup(t, config.Default(), 80, 24).summary(); got != "no changes" {
		t.Errorf("summary without edits = %q", got)
	}
}

func TestSetupPreviewFollowsSelection(t *testing.T) {
	t.Parallel()
	m, _ := startSetup(t, config.Default(), 100, 30)
	before := m.preview()
	drive(t, m, nil, keys("down", "down", "enter", "down", "down", "down")...)
	after := m.preview()
	if before == after {
		t.Error("preview did not change with the theme and icons")
	}
	if !strings.Contains(ansi.Strip(after), "* ! o @ # ~") {
		t.Errorf("ascii icons not previewed: %q", ansi.Strip(after))
	}
	m.opts.Getenv = func(string) string { return "C" }
	m.values.Icons = "auto"
	if !strings.Contains(ansi.Strip(m.preview()), "* ! o") {
		t.Error("auto icons ignore the locale")
	}
	m.values.Theme, m.values.Icons = "missing", "missing"
	if got := ansi.Strip(m.preview()); strings.TrimSpace(got) != "theme" {
		t.Errorf("unknown theme and icons preview = %q", got)
	}
}

func TestDiffConfig(t *testing.T) {
	t.Parallel()
	base := config.Default()
	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   []ConfigChange
	}{
		{name: "equal", mutate: func(*config.Config) {}},
		{name: "booleans", mutate: func(c *config.Config) { c.UI.Mouse = false }, want: []ConfigChange{{Key: "ui.mouse", From: "true", To: "false"}}},
		{name: "ignores keys the wizard does not edit", mutate: func(c *config.Config) { c.UI.Clock = false; c.Workspace.SplitRatio = 50 }},
		{
			name: "wizard order", mutate: func(c *config.Config) { c.Sandbox.Isolation = "container"; c.UI.Theme = "ansi" },
			want: []ConfigChange{{Key: "ui.theme", From: "monokai", To: "ansi"}, {Key: "sandbox.isolation", From: "bash", To: "container"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			after := base
			tc.mutate(&after)
			if got := diffConfig(base, after); !slices.Equal(got, tc.want) {
				t.Errorf("diffConfig = %+v, want %+v", got, tc.want)
			}
		})
	}
	if display("") != "(default)" || display("x") != "x" {
		t.Error("display")
	}
}

func TestOptionLabel(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, help, want string }{
		{name: "solo", help: "Claude only", want: "solo   Claude only"},
		{name: "review", help: "x", want: "review x"},
		{name: "pairing", help: "custom", want: "pairing custom"},
		{name: "a-very-long-layout", help: "custom", want: "a-very-long-layout custom"},
	}
	for _, tc := range cases {
		if got := optionLabel(tc.name, tc.help); got != tc.want {
			t.Errorf("optionLabel(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSetupResize(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ w, h int }{{100, 30}, {60, 20}, {40, 12}, {20, 6}, {1, 1}} {
		t.Run(strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h), func(t *testing.T) {
			t.Parallel()
			m, _ := startSetup(t, config.Default(), 100, 30, append(seq(setupLookDefaults), resize(size.w, size.h))...)
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size.h {
				t.Fatalf("%d lines, want %d", len(lines), size.h)
			}
			if !m.View().AltScreen {
				t.Error("setup does not use the alternate screen")
			}
		})
	}
}
