package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestLaunchTeams checks that agent teams reach both places Claude reads them
// from, the environment and the settings file, for the configuration and for
// the per-launch override, and nowhere when neither asks.
func TestLaunchTeams(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		override bool
		want     bool
	}{
		{name: "off", want: false},
		{name: "per-launch override", override: true, want: true},
		{name: "configuration", config: "[claude]\nteams = true\n", want: true},
		{name: "both", config: "[claude]\nteams = true\n", override: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCreateEnv(t)
			if tc.config != "" {
				e.writeConfig(t, tc.config)
			}
			s := openServer(t, e.testHost)
			lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{Teams: tc.override})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := layout.Builtin(layout.Solo, layout.Options{})
			if err != nil {
				t.Fatal(err)
			}
			procs, err := lp.paneProcs(e.Host, plan, e.project, "api")
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(procs[0].Env, session.EnvClaudeTeams+"=1"); got != tc.want {
				t.Fatalf("teams environment = %v, want %v: %q", got, tc.want, procs[0].Env)
			}
			if got := settingsTeammateMode(t, lp.settings) == "tmux"; got != tc.want {
				t.Fatalf("teammateMode = %q, want tmux %v", settingsTeammateMode(t, lp.settings), tc.want)
			}
			// A team of ours opens its teammates through our launcher.
			if got := launcherIn(procs[0].Env) != ""; got != tc.want {
				t.Fatalf("teammate launcher = %v, want %v: %q", got, tc.want, procs[0].Env)
			}
		})
	}
}

// TestLaunchTeammateMode checks where a teammate opens: the settings file
// carries the backend Claude Code knows, and only the workspace's own mode
// puts a launcher in the agent's environment.
func TestLaunchTeammateMode(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		want     string
		launcher bool
	}{
		{name: "a pane of the workspace", mode: "lmux", want: "tmux", launcher: true},
		{name: "no mode is a pane of the workspace", mode: "", want: "tmux", launcher: true},
		{name: "the agent decides", mode: "auto", want: "auto"},
		{name: "inside the lead", mode: "in-process", want: "in-process"},
		{name: "terminal splits", mode: "iterm2", want: "iterm2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCreateEnv(t)
			s := openServer(t, e.testHost)
			s.Config.Claude.Teams = true
			s.Config.Claude.TeammateMode = tc.mode
			s.Config.UI.Theme = "nord"
			s.Config.UI.Icons = "nerd"
			lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := settingsTeammateMode(t, lp.settings); got != tc.want {
				t.Fatalf("teammateMode = %q, want %q", got, tc.want)
			}
			plan, err := layout.Builtin(layout.Solo, layout.Options{})
			if err != nil {
				t.Fatal(err)
			}
			procs, err := lp.paneProcs(e.Host, plan, e.project, "api")
			if err != nil {
				t.Fatal(err)
			}
			path := launcherIn(procs[0].Env)
			if (path != "") != tc.launcher {
				t.Fatalf("teammate launcher = %q, want one %v", path, tc.launcher)
			}
			if !tc.launcher {
				return
			}
			if dir := filepath.Dir(path); dir != s.Paths.LaunchersDir() {
				t.Fatalf("launcher written to %s, want %s", dir, s.Paths.LaunchersDir())
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("launcher mode %v", info.Mode().Perm())
			}
			script, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{e.Exe, lp.base.ClaudePath} {
				if !strings.Contains(string(script), want) {
					t.Fatalf("launcher does not run %s: %s", want, script)
				}
			}
			// A teammate draws the way the lead does, so the look of the
			// workspace travels with the launcher: nothing of the lead's own
			// environment reaches a pane the tmux server starts.
			for _, want := range []string{
				"export " + session.EnvTheme + "=nord\n",
				"export " + session.EnvIcons + "=nerd\n",
				"export " + session.EnvClaudeTmuxTruecolor + "=1\n",
			} {
				if !strings.Contains(string(script), want) {
					t.Fatalf("launcher does not carry %s: %s", want, script)
				}
			}
		})
	}
}

// TestLaunchTeammateLauncherFallback drives the one thing a launcher must
// never do, which is to stop a workspace from opening: the directory it is
// written to is taken by a file, and the launch goes on with the agent's own
// backend and a line in the diagnostic log.
func TestLaunchTeammateLauncherFallback(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	s.Config.Claude.Teams = true
	if err := os.MkdirAll(filepath.Dir(s.Paths.LaunchersDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Paths.LaunchersDir(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{})
	if err != nil {
		t.Fatalf("a launcher that cannot be written stopped the launch: %v", err)
	}
	plan, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	procs, err := lp.paneProcs(e.Host, plan, e.project, "api")
	if err != nil {
		t.Fatal(err)
	}
	if path := launcherIn(procs[0].Env); path != "" {
		t.Fatalf("teammate launcher = %q, want none", path)
	}
	if !slices.Contains(procs[0].Env, session.EnvClaudeTeams+"=1") {
		t.Fatalf("the team was turned off with the launcher: %q", procs[0].Env)
	}
	if got := settingsTeammateMode(t, lp.settings); got != "tmux" {
		t.Fatalf("teammateMode = %q, want the teammates still in this window", got)
	}
	log, err := os.ReadFile(s.Paths.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "teammate: no launcher") {
		t.Fatalf("the reason was not logged: %s", log)
	}
}

// launcherIn returns the teammate launcher an agent environment carries.
func launcherIn(env []string) string {
	for _, e := range env {
		if after, ok := strings.CutPrefix(e, session.EnvClaudeTeammateCommand+"="); ok {
			return after
		}
	}
	return ""
}

func settingsTeammateMode(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		TeammateMode string `json:"teammateMode"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.TeammateMode
}

func TestWsSocketPath(t *testing.T) {
	getenv := func(string) string { return "" }
	h := Host{Getenv: getenv}
	cases := []struct {
		name string
		s    *Server
		want string
	}{
		{name: "socket name", s: &Server{SocketName: "lyna-tmux", Client: tmux.New(tmux.Options{Socket: tmux.Socket{Name: "lyna-tmux"}})}, want: SocketPath(getenv, "lyna-tmux")},
		{name: "socket path of a server the user runs", s: &Server{Client: tmux.New(tmux.Options{Socket: tmux.Socket{Path: "/private/tmp/tmux-501/default"}})}, want: "/private/tmp/tmux-501/default"},
		{name: "no client", s: &Server{SocketName: "x"}, want: SocketPath(getenv, "x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.wsSocketPath(h); got != tc.want {
				t.Fatalf("wsSocketPath = %q, want %q", got, tc.want)
			}
		})
	}
	// The path reaches the Claude environment through the launch plan.
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	s.Client = s.Client.WithSocket(tmux.Socket{Path: "/private/tmp/tmux-501/default"})
	lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lp.base.SocketPath != "/private/tmp/tmux-501/default" {
		t.Fatalf("launch socket %q", lp.base.SocketPath)
	}
}

// TestLaunchPaneProcs checks what the panes of a layout are started with: the
// views of the workspace are our own binary, told which workspace they belong
// to, and a shell pane is started with nothing at all.
func TestLaunchPaneProcs(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan := layout.Plan{Name: "probe", Panes: []layout.Pane{
		{Role: layout.RoleClaude},
		{Role: layout.RoleAgents, Split: layout.SplitRight, Size: 20},
		{Role: layout.RoleChanges, Split: layout.SplitDown, Size: 50, Parent: 1},
		{Role: layout.RoleReview, Split: layout.SplitDown, Size: 50, Parent: 2},
		{Role: layout.RoleShell, Split: layout.SplitDown, Size: 50, Parent: 3},
		{Role: layout.RoleCommand, Split: layout.SplitDown, Size: 50, Parent: 4, Command: "htop"},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	procs, err := lp.paneProcs(e.Host, plan, e.project, "api")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pane      int
		wantArgv  []string
		wantShell string
	}{
		{pane: 1, wantArgv: []string{e.Exe, "agents", "--rail", "--session", "api"}},
		{pane: 2, wantArgv: []string{e.Exe, "watch", "--session", "api", "--dir", e.project}},
		{pane: 3, wantArgv: []string{e.Exe, "review", "--dir", e.project}},
		{pane: 4},
		{pane: 5, wantShell: "htop"},
	}
	for _, tc := range cases {
		t.Run(string(plan.Panes[tc.pane].Role), func(t *testing.T) {
			got := procs[tc.pane]
			if !slices.Equal(got.Argv, tc.wantArgv) {
				t.Fatalf("pane %d runs %q, want %q", tc.pane, got.Argv, tc.wantArgv)
			}
			if got.Shell != tc.wantShell {
				t.Fatalf("pane %d runs the shell command %q, want %q", tc.pane, got.Shell, tc.wantShell)
			}
		})
	}
	// The agent is the one pane a plan starts with an environment of its own.
	if len(procs[0].Env) == 0 || len(procs[1].Env) != 0 {
		t.Fatalf("the rail was started with %q", procs[1].Env)
	}
}

// TestLaunchPaneProcsUnknownRole refuses a plan no layout can build.
func TestLaunchPaneProcsUnknownRole(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan := layout.Plan{Name: "probe", Panes: []layout.Pane{{Role: "telemetry"}}}
	if _, err := lp.paneProcs(e.Host, plan, e.project, "api"); err == nil {
		t.Fatal("a pane with no role of ours was given a process")
	}
}
