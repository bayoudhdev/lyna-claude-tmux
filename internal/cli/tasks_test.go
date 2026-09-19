package cli

import (
	"strings"
	"testing"
)

// TestTasksCLI runs the shared task list through the command tree, on a real
// workspace, the way tmux runs it in a popup.
func TestTasksCLI(t *testing.T) {
	e := newCLIEnv(t)
	e.start(t, "api", e.host.Home)

	cases := []struct {
		name        string
		env         map[string]string
		nonTerminal bool
		args        []string
		// keys drives a running list; nil runs the command to completion.
		keys     func(t *testing.T, term *watchTerm)
		wantCode int
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{
			name: "a popup outside tmux draws the workspace and closes on q",
			env:  map[string]string{"TMUX": ""}, args: []string{"tasks", "--session", "api", "--popup"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "tasks")
				term.waitScreen(t, "no shared task list yet")
				term.send(t, "q")
			},
		},
		{
			name: "a list in a pane ignores q and closes on ctrl+c",
			env:  map[string]string{"TMUX": ""}, args: []string{"tasks", "--session", "api"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "no shared task list yet")
				term.send(t, "q")
				term.waitScreen(t, "tasks")
				term.send(t, "\x03")
			},
		},
		{
			name: "no terminal", nonTerminal: true, args: []string{"tasks", "--session", "api"},
			wantCode: 1, errHas: []string{"draws a live view and needs a terminal"},
		},
		{
			name: "outside tmux without a workspace", env: map[string]string{"TMUX": ""}, args: []string{"tasks"},
			wantCode: 1, errHas: []string{"task list follows a workspace", "pass --session"},
		},
		{
			name: "a workspace that is not running", env: map[string]string{"TMUX": ""},
			args: []string{"tasks", "--session", "nope"}, wantCode: 1, errHas: []string{"such workspace: nope"},
		},
		{
			name: "the flag of a popup is not offered", args: []string{"tasks", "--help"},
			outHas: []string{"--session", "Show every task of the team"}, outLacks: []string{"--popup"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := map[string]string{}
			for k, v := range tc.env {
				saved[k] = e.env[k]
				e.setenv(k, v)
			}
			e.cwd, e.term.Interactive = e.host.Home, !tc.nonTerminal
			t.Cleanup(func() {
				for k, v := range saved {
					e.setenv(k, v)
				}
			})
			// Every case runs on the bounded harness, so a command that drew a
			// view where it should have refused fails instead of hanging.
			term := newWatchTerm(t)
			wait := watchRunCLI(t, e, term, tc.args...)
			if tc.keys != nil {
				tc.keys(t, term)
			}
			code := wait()
			stdout, stderr := term.out.String(), term.errOut.String()
			if code != tc.wantCode {
				t.Fatalf("exit code %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout has no %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.outLacks {
				if strings.Contains(stdout, unwanted) {
					t.Fatalf("stdout offers %q:\n%s", unwanted, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr has no %q:\n%s", want, stderr)
				}
			}
		})
	}
}
