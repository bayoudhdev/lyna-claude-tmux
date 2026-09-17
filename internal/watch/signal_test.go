package watch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// scriptedTmux answers display-message with the next scripted session id
// and records every command it receives.
type scriptedTmux struct {
	ids   []string
	fail  error
	calls [][]string
}

func (s *scriptedTmux) client() *tmux.Client {
	return tmux.New(tmux.Options{Bin: "tmux", Socket: tmux.Socket{Path: "/tmp/lt-test.sock"}, Executor: tmux.ExecutorFunc(
		func(_ context.Context, _ string, args []string) (tmux.Result, error) {
			s.calls = append(s.calls, args)
			if slices.Contains(args, "display-message") {
				if s.fail != nil {
					return tmux.Result{ExitCode: 1, Stderr: []byte("can't find pane: %9")}, nil
				}
				id := s.ids[0]
				s.ids = s.ids[1:]
				return tmux.Result{Stdout: []byte(id + "\n")}, nil
			}
			return tmux.Result{}, nil
		})})
}

// TestTmuxSignalCommands pins the tmux calls of both signal sources: each
// wait first resolves its target (a pane, or an exact session name) to the
// session id, then waits on that id's channel.
func TestTmuxSignalCommands(t *testing.T) {
	display := func(target string) []string {
		return []string{"-S", "/tmp/lt-test.sock", "-u", "display-message", "-p", "-t", target, "#{session_id}"}
	}
	wait := func(id string) []string {
		return []string{"-S", "/tmp/lt-test.sock", "-u", "wait-for", "lt-changes-" + id}
	}
	sources := []struct {
		name   string
		build  func(*tmux.Client) func(context.Context) error
		target string
	}{
		{name: "pane", build: func(c *tmux.Client) func(context.Context) error { return TmuxPaneSignal(c, "%3") }, target: "%3"},
		{name: "session", build: func(c *tmux.Client) func(context.Context) error { return TmuxSignal(c, "api") }, target: "=api:"},
	}
	cases := []struct {
		name    string
		script  scriptedTmux
		waits   int
		want    func(display []string) [][]string
		wantErr error
		errHas  string
	}{
		{
			name:   "waits on the channel of the resolved session id",
			script: scriptedTmux{ids: []string{"$1"}},
			waits:  1,
			want:   func(display []string) [][]string { return [][]string{display, wait("$1")} },
		},
		{
			name:   "resolves the id again before every wait",
			script: scriptedTmux{ids: []string{"$1", "$7"}},
			waits:  2,
			want:   func(display []string) [][]string { return [][]string{display, wait("$1"), display, wait("$7")} },
		},
		{
			name:    "an empty id is an error and no wait starts",
			script:  scriptedTmux{ids: []string{""}},
			waits:   1,
			want:    func(display []string) [][]string { return [][]string{display} },
			wantErr: ErrNoSession,
		},
		{
			name:   "a missing target is an error and no wait starts",
			script: scriptedTmux{fail: errors.New("gone")},
			waits:  1,
			want:   func(display []string) [][]string { return [][]string{display} },
			errHas: "can't find pane",
		},
	}
	for _, src := range sources {
		for _, tc := range cases {
			t.Run(src.name+"/"+tc.name, func(t *testing.T) {
				s := tc.script
				signal := src.build(s.client())
				var err error
				for range tc.waits {
					err = signal(t.Context())
				}
				switch {
				case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				case tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)):
					t.Fatalf("err = %v, want it to contain %q", err, tc.errHas)
				case tc.wantErr == nil && tc.errHas == "" && err != nil:
					t.Fatalf("err = %v", err)
				}
				if want := tc.want(display(src.target)); !slices.EqualFunc(s.calls, want, slices.Equal) {
					t.Fatalf("tmux calls = %q, want %q", s.calls, want)
				}
			})
		}
	}
}

// expectWake sends the commands, then checks that one wait of signal returns.
// The commands go first: tmux latches the signal until the wait starts.
func expectWake(t *testing.T, srv *tmuxtest.Server, signal func(context.Context) error, send ...tmux.Command) {
	t.Helper()
	ctx := tmuxtest.Context(t)
	if _, err := srv.Client.Batch(ctx, send...); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- signal(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("signal returned %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the wait did not return for the signal sent to the session")
	}
}

func sessionID(t *testing.T, srv *tmuxtest.Server, target string) string {
	t.Helper()
	id, err := srv.Client.Display(tmuxtest.Context(t), target, "#{session_id}")
	if err != nil || id == "" {
		t.Fatalf("session id of %s: %q, %v", target, id, err)
	}
	return id
}

// TestIntegrationTmuxPaneSignalFollowsRename checks on a real server that a
// pane's signal source keeps working across a rename: the session id, and
// so the channel, is the same before and after, and the hook's signal on it
// ends the wait either way.
func TestIntegrationTmuxPaneSignalFollowsRename(t *testing.T) {
	srv := tmuxtest.Start(t)
	pane, err := srv.Client.Display(tmuxtest.Context(t), tmux.ExactSession("base"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	signal := TmuxPaneSignal(srv.Client, pane)
	channel := tmux.ChangesChannel(sessionID(t, srv, pane))
	steps := []struct {
		name string
		send []tmux.Command
	}{
		{name: "hook signal on the current session", send: []tmux.Command{{"wait-for", "-S", channel}}},
		{name: "hook signal on the same channel after a rename", send: []tmux.Command{{"rename-session", "-t", tmux.ExactSession("base"), "renamed"}, {"wait-for", "-S", channel}}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			expectWake(t, srv, signal, st.send...)
			if got := tmux.ChangesChannel(sessionID(t, srv, pane)); got != channel {
				t.Fatalf("channel became %q, want %q", got, channel)
			}
		})
	}
}

// TestIntegrationTmuxSignalFollowsRecreatedSession checks on a real server
// that the source built from a workspace name resolves the name before every
// wait: a workspace killed and started again under the same name has a new
// id, and a signal on that id's channel ends the next wait.
func TestIntegrationTmuxSignalFollowsRecreatedSession(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	signal := TmuxSignal(srv.Client, "base")
	first := sessionID(t, srv, tmux.ExactSession("base"))
	expectWake(t, srv, signal, tmux.Command{"wait-for", "-S", tmux.ChangesChannel(first)})

	// Another session keeps the server alive while base is gone.
	if _, err := srv.Client.Batch(ctx,
		tmux.Command{"new-session", "-d", "-s", "holder", "sleep 3600"},
		tmux.Command{"kill-session", "-t", tmux.ExactSession("base")},
		tmux.Command{"new-session", "-d", "-s", "base", "sleep 3600"},
	); err != nil {
		t.Fatal(err)
	}
	second := sessionID(t, srv, tmux.ExactSession("base"))
	if second == first {
		t.Fatalf("the recreated session kept id %q", first)
	}
	expectWake(t, srv, signal, tmux.Command{"wait-for", "-S", tmux.ChangesChannel(second)})

	if _, err := srv.Client.Run(ctx, "kill-session", "-t", tmux.ExactSession("base")); err != nil {
		t.Fatal(err)
	}
	if err := signal(ctx); err == nil {
		t.Fatal("a wait for a killed workspace returned nil")
	}
}
