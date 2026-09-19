package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// spawnArgs is the line Claude Code appends to a teammate's launcher.
var spawnArgs = []string{
	"--agent-id", "review-api@session-8f3c1d2a", "--agent-name", "review-api",
	"--team-name", "session-8f3c1d2a", "--agent-color", "cyan",
	"--agent-type", "api-developer", "--permission-mode", "acceptEdits",
}

// teammateScene is a workspace with one pane opened the way Claude Code opens
// a teammate's: the lead, that pane, the window they share, and the
// arrangement the window had before the pane was split into it.
type teammateScene struct {
	lead, pane, window, remembered string
}

// openTeammateScene builds that scene at the given size on a real server, and
// points the host at the new pane the way the pane's own environment points at
// it, which is what the launcher reads.
func openTeammateScene(t *testing.T, h *testHost, s *Server, width, height int) teammateScene {
	t.Helper()
	ctx := tmuxtest.Context(t)
	plan, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	built, err := s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: "api", Project: h.root, Width: width, Height: height,
		Window: tmux.WindowSpec{Name: "claude", Dir: h.root, Plan: plan, Procs: []tmux.PaneProcess{
			{Argv: []string{"/bin/sh", "-c", "exec sleep 3600"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scene := teammateScene{lead: built.Panes[0], window: built.WindowID}
	// The arrangement the user is working in, stored the way a hook stores it.
	if _, err := s.Client.Batch(ctx, tmux.RememberLayout(scene.lead)...); err != nil {
		t.Fatal(err)
	}
	if scene.remembered, err = s.Client.Display(ctx, scene.window, "#{window_layout}"); err != nil {
		t.Fatal(err)
	}
	// Claude Code splits the teammate's pane in, styles it and tiles the whole
	// window for itself, all before it starts anything in that pane.
	out, err := s.Client.Run(ctx, "split-window", "-d", "-t", scene.lead, "-h", "-l", "70%", "-P", "-F", "#{pane_id}", "sleep 3600")
	if err != nil {
		t.Fatal(err)
	}
	scene.pane = strings.TrimSpace(out)
	if _, err := s.Client.Batch(ctx,
		tmux.Command{"set-option", "-p", "-t", scene.pane, "pane-border-format", " claude code "},
		tmux.Command{"set-option", "-p", "-t", scene.pane, "window-style", "bg=#101010"},
		tmux.Command{"select-layout", "-t", scene.window, "main-vertical"},
	); err != nil {
		t.Fatal(err)
	}
	h.env["TMUX"] = SocketPath(h.Getenv, s.SocketName) + ",1,0"
	h.env["TMUX_PANE"] = scene.pane
	h.refreshEnviron()
	return scene
}

// TestTeammateAdoptsItsPane runs the launcher in the pane Claude Code opened
// on a real tmux server, and reads back what that pane became: a labeled pane
// of ours, running the agent it was told to run with the arguments it was
// given.
func TestTeammateAdoptsItsPane(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	pane := openTeammateScene(t, h, s, 240, 60).pane

	log := filepath.Join(t.TempDir(), "lyna-tmux.log")
	run, err := Teammate(ctx, h.Host, TeammateRequest{ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: 3, LogPath: log})
	if err != nil {
		t.Fatalf("Teammate: %v", err)
	}
	assertTeammateLog(t, log, hook.LogTeammate, "review-api opened in workspace api")
	if run.Path != "/bin/sh" || !slices.Equal(run.Argv, append([]string{"/bin/sh"}, spawnArgs...)) {
		t.Fatalf("the agent is %s %q", run.Path, run.Argv)
	}
	for _, want := range []string{
		session.EnvManaged + "=1",
		session.EnvSocket + "=" + SocketPath(h.Getenv, s.SocketName),
		session.EnvSession + "=api",
	} {
		if !slices.Contains(run.Env, want) {
			t.Fatalf("the teammate environment has no %s: %q", want, run.Env)
		}
	}
	options := []struct{ name, want string }{
		{tmux.OptRole, tmux.RoleTeammate},
		{tmux.OptAgent, "review-api"},
		{tmux.OptAgentType, "api-developer"},
		{tmux.OptTeam, "session-8f3c1d2a"},
		{tmux.OptState, "busy"},
		{"remain-on-exit", "failed"},
		{tmux.OptPassthrough, "on"},
	}
	for _, o := range options {
		got, err := s.Client.ShowOption(ctx, "-p", pane, o.name)
		if err != nil || got != o.want {
			t.Fatalf("%s = %q, %v; want %q", o.name, got, err, o.want)
		}
	}
	// The styles Claude Code set are gone, so the pane draws with the look of
	// the window it is in.
	for _, name := range []string{"pane-border-format", "window-style"} {
		got, err := s.Client.ShowOption(ctx, "-p", pane, name)
		if err != nil || got != "" {
			t.Fatalf("pane option %s = %q, %v; want the workspace's own", name, got, err)
		}
	}
}

// TestTeammatePlacesItsPane drives the pane policy on a real server: a
// teammate the window has room for stays beside the lead with the window tiled
// for the two of them, and one it has no room for moves to a window of its own
// and leaves the arrangement the user was working in behind it.
func TestTeammatePlacesItsPane(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		panes         int
		// opening is a pane Claude Code has just split in for the next teammate,
		// which has not taken it over yet.
		opening bool
		// elsewhere is a window of the user's own elsewhere in the workspace,
		// which the window the teammate opened in is not read from.
		elsewhere bool
		// rail opens the agents rail in the window the teammate opened in.
		rail          bool
		wantHere      bool
		wantLeadWidth string
	}{
		{
			name: "a teammate the window has room for", width: 240, height: 60, panes: 3,
			wantHere: true, wantLeadWidth: "96",
		},
		{
			name: "a teammate opened next to a pane that is still opening", width: 240, height: 60, panes: 3,
			opening: true, wantHere: true, wantLeadWidth: "96",
		},
		{
			name: "a shell of the user's in another window", width: 240, height: 60, panes: 3,
			elsewhere: true, wantHere: true, wantLeadWidth: "96",
		},
		{
			name: "a teammate in a window that carries the rail", width: 240, height: 60, panes: 3,
			rail: true, wantHere: true, wantLeadWidth: "211",
		},
		{name: "one teammate more than the window shares", width: 240, height: 60, panes: 0},
		{name: "a window too narrow to read two agents in", width: 100, height: 60, panes: 3},
		{name: "a window too short to read two agents in", width: 240, height: 13, panes: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tmuxtest.Context(t)
			h := newTestHost(t)
			s := openServer(t, h)
			scene := openTeammateScene(t, h, s, tc.width, tc.height)
			rail := ""
			if tc.opening {
				if _, err := s.Client.Run(ctx, "split-window", "-d", "-t", scene.pane, "-l", "50%", "sleep 3600"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rail {
				out, err := s.Client.Run(ctx, "split-window", "-b", "-h", "-d", "-t", scene.lead,
					"-l", strconv.Itoa(layout.RailWidth), "-P", "-F", "#{pane_id}", "sleep 3600")
				if err != nil {
					t.Fatal(err)
				}
				rail = strings.TrimSpace(out)
				if _, err := s.Client.Run(ctx, "set-option", "-p", "-t", rail, tmux.OptRole, tmux.RoleAgents); err != nil {
					t.Fatal(err)
				}
			}
			if tc.elsewhere {
				out, err := s.Client.Run(ctx, "new-window", "-d", "-t", tmux.ExactSession("api"), "-P", "-F", "#{pane_id}", "sleep 3600")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Client.Run(ctx, "set-option", "-p", "-t", strings.TrimSpace(out), tmux.OptRole, tmux.RoleShell); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := Teammate(ctx, h.Host, TeammateRequest{
				ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: tc.panes,
			}); err != nil {
				t.Fatalf("Teammate: %v", err)
			}

			panes, err := s.Client.ListPanes(ctx, scene.window)
			if err != nil {
				t.Fatal(err)
			}
			here := false
			for _, p := range panes {
				if p.ID == scene.pane && p.WindowID == scene.window {
					here = true
				}
			}
			if here != tc.wantHere {
				t.Fatalf("the teammate is in the lead's window: %v, want %v", here, tc.wantHere)
			}
			if tc.wantHere {
				width, err := s.Client.Display(ctx, scene.lead, "#{pane_width}")
				if err != nil {
					t.Fatal(err)
				}
				if width != tc.wantLeadWidth {
					t.Fatalf("lead width %s, want %s of a %d cell window", width, tc.wantLeadWidth, tc.width)
				}
				if rail != "" {
					// The rail keeps the column it had: the agents were tiled
					// beside it, not over it.
					got, err := s.Client.Display(ctx, rail, "#{pane_width}")
					if err != nil {
						t.Fatal(err)
					}
					if got != strconv.Itoa(layout.RailWidth) {
						t.Fatalf("the rail is %s cells wide, want %d", got, layout.RailWidth)
					}
				}
				return
			}
			// The window the teammate left is the one the user was working in.
			got, err := s.Client.Display(ctx, scene.window, "#{window_layout}")
			if err != nil {
				t.Fatal(err)
			}
			if got != scene.remembered {
				t.Fatalf("window arrangement %q, want the stored %q", got, scene.remembered)
			}
			windows, err := s.Client.Run(ctx, "list-windows", "-t", tmux.ExactSession("api"), "-F", "#{window_name}")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(strings.Fields(windows), "review-api") {
				t.Fatalf("windows %q, want one named after the teammate", windows)
			}
			// The user is left where they were working.
			current, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{window_id}")
			if err != nil {
				t.Fatal(err)
			}
			if current != scene.window {
				t.Fatalf("current window %s, want %s", current, scene.window)
			}
		})
	}
}

// TestTeammateRefusesWithoutAnAgent covers the one case that cannot be
// recovered from: a launcher that names no agent to run is not one of ours.
func TestTeammateRefusesWithoutAnAgent(t *testing.T) {
	cases := []struct {
		name, path string
	}{
		{name: "no agent at all", path: ""},
		{name: "an agent looked up on PATH", path: "claude"},
		{name: "a relative agent", path: "./claude"},
		{name: "an agent with a newline in it", path: "/usr/bin/claude\nrm -rf /"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Host{Getenv: func(string) string { return "" }}
			_, err := Teammate(context.Background(), h, TeammateRequest{ClaudePath: tc.path})
			if !errors.Is(err, ErrTeammateAgent) {
				t.Fatalf("Teammate error = %v", err)
			}
		})
	}
}

// TestTeammateStartsAnyway drives every way the pane cannot be adopted, and
// asserts the teammate starts regardless: the agent is returned with its own
// arguments, no environment of ours is claimed, the reason is logged, and no
// command reaches a server we did not create.
func TestTeammateStartsAnyway(t *testing.T) {
	cases := []struct {
		name     string
		tmuxEnv  string
		pane     string
		reply    string
		wantCmds int
		wantLog  string
	}{
		{name: "a teammate outside tmux", wantLog: "no pane of a workspace"},
		{name: "no pane in the environment", tmuxEnv: "/tmp/tmux-501/lyna-tmux,1,0", wantLog: "no pane of a workspace"},
		{
			name: "a pane named instead of identified", tmuxEnv: "/tmp/tmux-501/lyna-tmux,1,0",
			pane: "review-api", wantLog: "no pane of a workspace",
		},
		{
			name: "the server the agent built for itself", tmuxEnv: "/tmp/tmux-501/claude-swarm-4242,1,0",
			pane: "%3", wantLog: "the agent's own server",
		},
		{
			name: "a pane of a tmux that is not a workspace", tmuxEnv: "/tmp/tmux-501/default,1,0",
			pane: "%3", reply: "\x1fwork", wantCmds: 1, wantLog: "not in a workspace of ours",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runs [][]string
			h := Host{
				Getenv: func(k string) string {
					switch k {
					case "TMUX":
						return tc.tmuxEnv
					case "TMUX_PANE":
						return tc.pane
					}
					return ""
				},
				Environ: []string{"PATH=/usr/bin", session.EnvManaged + "=0"},
			}
			log := filepath.Join(t.TempDir(), "lyna-tmux.log")
			req := TeammateRequest{
				ClaudePath: "/opt/claude", Args: spawnArgs, LogPath: log,
				Tmux: func(sock tmux.Socket) *tmux.Client {
					return tmux.New(tmux.Options{Socket: sock, Executor: tmux.ExecutorFunc(
						func(_ context.Context, _ string, args []string) (tmux.Result, error) {
							runs = append(runs, args)
							return tmux.Result{Stdout: []byte(tc.reply)}, nil
						})})
				},
			}
			run, err := Teammate(context.Background(), h, req)
			if err != nil {
				t.Fatalf("Teammate: %v", err)
			}
			if run.Path != "/opt/claude" || !slices.Equal(run.Argv, append([]string{"/opt/claude"}, spawnArgs...)) {
				t.Fatalf("agent %s %q", run.Path, run.Argv)
			}
			// A pane that is not ours is not claimed as one.
			if !slices.Equal(run.Env, h.Environ) {
				t.Fatalf("environment %q, want the one the launcher was given", run.Env)
			}
			if len(runs) != tc.wantCmds {
				t.Fatalf("%d tmux invocations, want %d: %q", len(runs), tc.wantCmds, runs)
			}
			for _, args := range runs {
				if slices.Contains(args, "set-option") || strings.Contains(strings.Join(args, " "), "claude-swarm") {
					t.Fatalf("a pane that is not ours was written to: %q", args)
				}
			}
			assertTeammateLog(t, log, hook.LogTeammateFallback, "review-api opens the way the agent opens it: ", tc.wantLog)
		})
	}
}

// TestTeammateKeepsAPaneItCannotPlace covers a teammate labeled as a pane of
// the workspace whose window cannot be arranged: the pane stays ours, with
// the workspace's environment, and the log says it stays where it opened.
func TestTeammateKeepsAPaneItCannotPlace(t *testing.T) {
	h := Host{
		Getenv: func(k string) string {
			return map[string]string{"TMUX": "/tmp/tmux-501/lyna-tmux,1,0", "TMUX_PANE": "%3"}[k]
		},
		Environ: []string{"PATH=/usr/bin"},
	}
	log := filepath.Join(t.TempDir(), "lyna-tmux.log")
	req := TeammateRequest{
		ClaudePath: "/opt/claude", Args: spawnArgs, LogPath: log, AgentPanes: 3,
		Tmux: func(sock tmux.Socket) *tmux.Client {
			return tmux.New(tmux.Options{Socket: sock, Executor: tmux.ExecutorFunc(
				func(_ context.Context, _ string, args []string) (tmux.Result, error) {
					switch cmd := strings.Join(args, " "); {
					case strings.Contains(cmd, "display-message"):
						return tmux.Result{Stdout: []byte(tmux.FieldSep("1", "api", "@1", "200", "50", ""))}, nil
					case strings.Contains(cmd, "list-panes"):
						return tmux.Result{Stderr: []byte("can't find window: @1"), ExitCode: 1}, nil
					}
					return tmux.Result{}, nil
				})})
		},
	}
	run, err := Teammate(context.Background(), h, req)
	if err != nil {
		t.Fatalf("Teammate: %v", err)
	}
	if !slices.Contains(run.Env, session.EnvSession+"=api") {
		t.Fatalf("the pane was not claimed for the workspace: %q", run.Env)
	}
	assertTeammateLog(t, log, hook.LogTeammateUnplaced,
		"review-api opened in workspace api and stays where the agent put it: ", "can't find window")
}

// assertTeammateLog reads the last line the launcher logged about a teammate
// and checks how it says the teammate opened.
func assertTeammateLog(t *testing.T, path, name string, parts ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := hook.LastLogEntry(data, hook.LogTeammate, hook.LogTeammateUnplaced, hook.LogTeammateFallback)
	if !ok || got.Name != name {
		t.Fatalf("last teammate line %+v, %v; want one logged as %s in\n%s", got, ok, name, data)
	}
	if !strings.HasPrefix(got.Message, parts[0]) {
		t.Fatalf("message %q, want it to start with %q", got.Message, parts[0])
	}
	for _, part := range parts[1:] {
		if !strings.Contains(got.Message, part) {
			t.Fatalf("message %q, want it to name %q", got.Message, part)
		}
	}
}

// TestCells reads the window sizes tmux prints, and the answers that are not a
// size: the policy decides nothing from a window nobody measured.
func TestCells(t *testing.T) {
	cases := []struct {
		name, in string
		want     int
	}{
		{name: "a window of its own width", in: "240", want: 240},
		{name: "a window with no width yet", in: "0", want: 0},
		{name: "a format that expanded to nothing", in: "", want: 0},
		{name: "a format tmux did not know", in: "#{window_width}", want: 0},
		{name: "a width with a space in it", in: " 240", want: 0},
		{name: "a negative width", in: "-240", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cells(tc.in); got != tc.want {
				t.Fatalf("cells(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestWithEnv covers what the teammate's environment is given: a variable of
// ours that is not there is added, one that is there is replaced where it
// stands, and nothing else moves.
func TestWithEnv(t *testing.T) {
	cases := []struct {
		name        string
		environ     []string
		assignments []string
		want        []string
	}{
		{
			name:        "a variable the agent was not started with",
			environ:     []string{"PATH=/bin", "TERM=xterm"},
			assignments: []string{session.EnvManaged + "=1"},
			want:        []string{"PATH=/bin", "TERM=xterm", session.EnvManaged + "=1"},
		},
		{
			name:        "a variable the agent inherited from another workspace",
			environ:     []string{session.EnvSession + "=web", "PATH=/bin"},
			assignments: []string{session.EnvSession + "=api"},
			want:        []string{session.EnvSession + "=api", "PATH=/bin"},
		},
		{
			name:        "a variable given twice",
			environ:     []string{"PATH=/bin"},
			assignments: []string{session.EnvManaged + "=1", session.EnvManaged + "=1"},
			want:        []string{"PATH=/bin", session.EnvManaged + "=1"},
		},
		{
			name:        "a name another variable starts with",
			environ:     []string{session.EnvSocketName + "=lyna-tmux", "PATH=/bin"},
			assignments: []string{session.EnvSocket + "=/tmp/tmux-501/lyna-tmux"},
			want: []string{
				session.EnvSocketName + "=lyna-tmux", "PATH=/bin",
				session.EnvSocket + "=/tmp/tmux-501/lyna-tmux",
			},
		},
		{
			name:    "nothing to give",
			environ: []string{"PATH=/bin"},
			want:    []string{"PATH=/bin"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withEnv(tc.environ, tc.assignments...)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("withEnv:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// writeExeStub puts a program at the host's own path that stays running, so a
// pane the launcher opens for the rail is a pane that lives long enough to be
// read back.
func writeExeStub(t *testing.T, h *testHost) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.Exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Exe, []byte("#!/bin/sh\nexec sleep 3600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// railPanes returns the agents rails of a window.
func railPanes(t *testing.T, s *Server, window string) []tmux.Pane {
	t.Helper()
	panes, err := s.Client.ListPanes(tmuxtest.Context(t), window)
	if err != nil {
		t.Fatal(err)
	}
	var rails []tmux.Pane
	for _, p := range panes {
		if p.WindowID == window && p.Role == tmux.RoleAgents {
			rails = append(rails, p)
		}
	}
	return rails
}

// TestTeammateOpensTheRail drives the auto mode on a real server: the first
// teammate of a workspace that asks for the rail opens it beside the leftmost
// pane of the window, a window that carries one already is given no second
// one, and every other mode leaves the window as it was.
func TestTeammateOpensTheRail(t *testing.T) {
	cases := []struct {
		name, sidebar string
		// existing carries a rail into the window before the teammate arrives.
		existing bool
		// wantOpened expects a rail opened by the launcher itself.
		wantOpened bool
		wantRails  int
	}{
		{name: "auto opens it with the first teammate", sidebar: config.SidebarAuto, wantOpened: true, wantRails: 1},
		{name: "a window that carries one keeps the one it has", sidebar: config.SidebarAuto, existing: true, wantRails: 1},
		{name: "the key mode waits to be asked", sidebar: config.SidebarKey},
		{name: "off opens none", sidebar: config.SidebarOff},
		{name: "always is opened with the workspace, not here", sidebar: config.SidebarAlways},
		{name: "a workspace with no configuration read opens none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tmuxtest.Context(t)
			h := newTestHost(t)
			writeExeStub(t, h)
			s := openServer(t, h)
			scene := openTeammateScene(t, h, s, 240, 60)
			existing := ""
			if tc.existing {
				out, err := s.Client.Run(ctx, "split-window", "-b", "-h", "-d", "-t", scene.lead,
					"-l", strconv.Itoa(layout.RailWidth), "-P", "-F", "#{pane_id}", "sleep 3600")
				if err != nil {
					t.Fatal(err)
				}
				existing = strings.TrimSpace(out)
				if _, err := s.Client.Run(ctx, "set-option", "-p", "-t", existing, tmux.OptRole, tmux.RoleAgents); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := Teammate(ctx, h.Host, TeammateRequest{
				ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: 3, Sidebar: tc.sidebar,
			}); err != nil {
				t.Fatalf("Teammate: %v", err)
			}

			rails := railPanes(t, s, scene.window)
			if len(rails) != tc.wantRails {
				t.Fatalf("the window carries %d rails, want %d", len(rails), tc.wantRails)
			}
			if tc.wantRails == 0 {
				return
			}
			rail := rails[0]
			if tc.existing {
				if rail.ID != existing {
					t.Fatalf("the rail is %s, want the one the window had (%s)", rail.ID, existing)
				}
				return
			}
			if !tc.wantOpened {
				t.Fatalf("a rail was opened for a workspace that asks for none: %+v", rail)
			}
			// tmux numbers the panes of a window by where they are on the
			// screen, so the rail being the first of them is the rail being the
			// leftmost column.
			panes, err := s.Client.ListPanes(ctx, scene.window)
			if err != nil {
				t.Fatal(err)
			}
			if len(panes) == 0 || panes[0].ID != rail.ID {
				t.Fatalf("the window reads %+v, want the rail first", panes)
			}
			if rail.Width != layout.RailWidth {
				t.Fatalf("the rail is %d cells wide, want %d", rail.Width, layout.RailWidth)
			}
			if rail.Active {
				t.Fatal("the rail took the cursor with it")
			}
			started, err := s.Client.Display(ctx, rail.ID, "#{pane_start_command}")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{h.Exe, "agents", "--rail", "--auto"} {
				if !strings.Contains(started, want) {
					t.Fatalf("the rail runs %q, which lacks %q", started, want)
				}
			}
		})
	}
}

// TestRailProcess covers what each sidebar mode asks of the launcher.
func TestRailProcess(t *testing.T) {
	cases := []struct {
		name, exe, sidebar string
		want               []string
	}{
		{name: "auto", exe: "/opt/lmux", sidebar: config.SidebarAuto, want: []string{"/opt/lmux", "agents", "--rail", "--auto"}},
		{name: "always", exe: "/opt/lmux", sidebar: config.SidebarAlways},
		{name: "key", exe: "/opt/lmux", sidebar: config.SidebarKey},
		{name: "off", exe: "/opt/lmux", sidebar: config.SidebarOff},
		{name: "no mode", exe: "/opt/lmux"},
		{name: "a binary that cannot be named", sidebar: config.SidebarAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := railProcess(tc.exe, tc.sidebar)
			if !slices.Equal(got.Argv, tc.want) {
				t.Fatalf("railProcess(%q, %q) = %q, want %q", tc.exe, tc.sidebar, got.Argv, tc.want)
			}
		})
	}
}
