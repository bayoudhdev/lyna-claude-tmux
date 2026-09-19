package cli

import (
	"strings"
	"testing"
)

// TestAgentBarCLI runs the agents rail through the command tree, on a real
// workspace, the way tmux runs it in a pane or a popup.
func TestAgentBarCLI(t *testing.T) {
	e := newCLIEnv(t)
	e.start(t, "api", e.host.Home)

	cases := []struct {
		name        string
		env         map[string]string
		nonTerminal bool
		args        []string
		// keys drives a running rail; nil runs the command to completion.
		keys     func(t *testing.T, term *watchTerm)
		wantCode int
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{
			name: "a popup outside tmux draws the workspace and closes on q",
			env:  map[string]string{"TMUX": ""}, args: []string{"agents", "--rail", "--session", "api", "--popup"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "lead")
				term.waitScreen(t, "api")
				term.send(t, "q")
			},
		},
		{
			name: "a rail in a pane ignores q and closes on ctrl+c",
			env:  map[string]string{"TMUX": ""}, args: []string{"agents", "--rail", "--session", "api"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "lead")
				term.send(t, "q")
				term.waitScreen(t, "api")
				term.send(t, "\x03")
			},
		},
		{
			name: "no terminal", nonTerminal: true, args: []string{"agents", "--rail", "--session", "api"},
			wantCode: 1, errHas: []string{"rail draws a live view and needs a terminal"},
		},
		{
			name: "outside tmux without a workspace", env: map[string]string{"TMUX": ""}, args: []string{"agents", "--rail"},
			wantCode: 1, errHas: []string{"rail follows a workspace", "pass --session"},
		},
		{
			name: "a workspace that is not running", env: map[string]string{"TMUX": ""},
			args: []string{"agents", "--rail", "--session", "nope"}, wantCode: 1, errHas: []string{"such workspace: nope"},
		},
		{
			name: "the rail draws nothing to print", env: map[string]string{"TMUX": ""},
			args: []string{"agents", "--rail", "--json"}, wantCode: 1, errHas: []string{"none of the others can be; [json rail] were all set"},
		},
		{
			name: "the flags of a pane are not offered", args: []string{"agents", "--help"},
			outHas: []string{"--json"}, outLacks: []string{"--rail", "--session"},
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
