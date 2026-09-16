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

// scriptedTmux answers display-message with the next scripted session name
// and records every command it receives.
type scriptedTmux struct {
	names []string
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
				name := s.names[0]
				s.names = s.names[1:]
				return tmux.Result{Stdout: []byte(name + "\n")}, nil
			}
			return tmux.Result{}, nil
		})})
}

func TestTmuxPaneSignalCommands(t *testing.T) {
	display := []string{"-S", "/tmp/lt-test.sock", "-u", "display-message", "-p", "-t", "%3", "#{session_name}"}
	wait := func(session string) []string {
		return []string{"-S", "/tmp/lt-test.sock", "-u", "wait-for", "lt-changes-" + session}
	}
	cases := []struct {
		name      string
		script    scriptedTmux
		waits     int
		wantCalls [][]string
		wantErr   error
		errHas    string
	}{
		{
			name:      "waits on the channel of the pane's session",
			script:    scriptedTmux{names: []string{"api"}},
			waits:     1,
			wantCalls: [][]string{display, wait("api")},
		},
		{
			name:      "reads the session name again before every wait",
			script:    scriptedTmux{names: []string{"api", "backend"}},
			waits:     2,
			wantCalls: [][]string{display, wait("api"), display, wait("backend")},
		},
		{
			name:      "a pane without a session is an error and no wait starts",
			script:    scriptedTmux{names: []string{""}},
			waits:     1,
			wantCalls: [][]string{display},
			wantErr:   ErrNoSession,
		},
		{
			name:      "a missing pane is an error and no wait starts",
			script:    scriptedTmux{fail: errors.New("gone")},
			waits:     1,
			wantCalls: [][]string{display},
			errHas:    "can't find pane",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.script
			signal := TmuxPaneSignal(s.client(), "%3")
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
			if !slices.EqualFunc(s.calls, tc.wantCalls, slices.Equal) {
				t.Fatalf("tmux calls = %q, want %q", s.calls, tc.wantCalls)
			}
		})
	}
}

// TestIntegrationTmuxPaneSignalFollowsRename checks on a real server that
// every wait reads the session name of the pane at that moment: after a
// rename, only a signal on the new name's channel ends the next wait.
func TestIntegrationTmuxPaneSignalFollowsRename(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	pane, err := srv.Client.Display(ctx, tmux.ExactSession("base"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	signal := TmuxPaneSignal(srv.Client, pane)
	steps := []struct {
		name string
		send []tmux.Command
	}{
		{name: "hook signal on the current name", send: []tmux.Command{{"wait-for", "-S", "lt-changes-base"}}},
		{name: "hook signal on the new name after a rename", send: []tmux.Command{{"rename-session", "-t", tmux.ExactSession("base"), "renamed"}, {"wait-for", "-S", "lt-changes-renamed"}}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			done := make(chan error, 1)
			// The signal is sent first: tmux latches it until the wait starts.
			if _, err := srv.Client.Batch(ctx, st.send...); err != nil {
				t.Fatal(err)
			}
			go func() { done <- signal(ctx) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("signal returned %v", err)
				}
			case <-ctx.Done():
				t.Fatal("the wait did not return for the signal sent to the pane's session")
			}
		})
	}
}
