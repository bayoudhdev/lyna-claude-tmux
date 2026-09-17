package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// spawnArgs is the line Claude Code appends to a teammate's launcher.
var spawnArgs = []string{
	"--agent-id", "review-api@session-8f3c1d2a", "--agent-name", "review-api",
	"--team-name", "session-8f3c1d2a", "--agent-color", "cyan",
	"--agent-type", "api-developer", "--permission-mode", "acceptEdits",
}

// TestTeammateAdoptsItsPane runs the launcher in a pane of a real workspace on
// a real tmux server, and reads back what the pane became: a labeled pane of
// ours, with the agent it was told to run and the arguments it was given.
func TestTeammateAdoptsItsPane(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	startWorkspace(t, s, "api")
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession("api"))
	if err != nil || len(panes) == 0 {
		t.Fatalf("list panes: %v %v", panes, err)
	}
	pane := panes[0].ID
	// Claude Code styles the pane it opened before it starts the agent in it.
	if _, err := s.Client.Batch(ctx,
		tmux.Command{"set-option", "-p", "-t", pane, "pane-border-format", " claude code "},
		tmux.Command{"set-option", "-p", "-t", pane, "window-style", "bg=#101010"},
	); err != nil {
		t.Fatal(err)
	}
	h.env["TMUX"] = SocketPath(h.Getenv, s.SocketName) + ",1,0"
	h.env["TMUX_PANE"] = pane
	h.refreshEnviron()

	run, err := Teammate(ctx, h.Host, TeammateRequest{ClaudePath: "/bin/sh", Args: spawnArgs})
	if err != nil {
		t.Fatalf("Teammate: %v", err)
	}
	if run.Path != "/bin/sh" || !slices.Equal(run.Argv, append([]string{"/bin/sh"}, spawnArgs...)) {
		t.Fatalf("agent %s %q", run.Path, run.Argv)
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
	}
	for _, o := range options {
		got, err := s.Client.ShowOption(ctx, "-p", pane, o.name)
		if err != nil || got != o.want {
			t.Fatalf("%s = %q, %v; want %q", o.name, got, err, o.want)
		}
	}
	for _, o := range []struct{ name, want string }{
		{"remain-on-exit", "failed"},
		{tmux.OptPassthrough, "on"},
	} {
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
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tc.wantLog) {
				t.Fatalf("log %q, want it to name %q", data, tc.wantLog)
			}
		})
	}
}
