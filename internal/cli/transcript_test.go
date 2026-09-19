package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// transcriptScene is the workspace a transcript is read in: the pane of its
// agent, one pane with no transcript at all, and the file the first one names.
type transcriptScene struct {
	pane, bare, path string
}

// openTranscriptScene starts a workspace, writes a transcript under the
// isolated Claude configuration directory and stores it on the first pane, the
// way the SessionStart hook stores it.
func openTranscriptScene(t *testing.T, e *cliEnv) transcriptScene {
	t.Helper()
	ctx := tmuxtest.Context(t)
	e.start(t, "api", e.host.Home)
	s, err := app.OpenServer(ctx, e.host)
	if err != nil {
		t.Fatal(err)
	}
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession("api"))
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) < 2 {
		t.Fatalf("the workspace has %d panes", len(panes))
	}
	dir := filepath.Join(e.host.Home, ".claude", "projects", "-work-api")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-8f3c.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"read the pane options"},"timestamp":"2026-09-15T12:00:00.000Z"}` + "\n" +
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"reading them now"}],` +
		`"usage":{"input_tokens":1,"output_tokens":2}},"timestamp":"2026-09-15T12:00:01.000Z"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client.Batch(ctx, tmux.Command{"set-option", "-p", "-t", panes[0].ID, tmux.OptTranscript, path}); err != nil {
		t.Fatal(err)
	}
	return transcriptScene{pane: panes[0].ID, bare: panes[1].ID, path: path}
}

// TestTranscriptCLI runs the transcript reader through the command tree, on a
// real workspace, the way the rail opens it in a popup.
func TestTranscriptCLI(t *testing.T) {
	e := newCLIEnv(t)
	scene := openTranscriptScene(t, e)
	socket := app.SocketPath(e.host.Getenv, e.env["LYNA_TMUX_SOCKET_NAME"])

	cases := []struct {
		name        string
		env         map[string]string
		nonTerminal bool
		// args are the arguments, with "PANE" and "BARE" standing for the panes
		// of the scene.
		args []string
		// keys drives a running reader; nil runs the command to completion.
		keys     func(t *testing.T, term *watchTerm)
		wantCode int
		outHas   []string
		errHas   []string
	}{
		{
			name: "the reader draws the transcript of a pane and closes on q",
			env:  map[string]string{"TMUX": ""},
			args: []string{"transcript", "--session", "api", "--to", "PANE"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "read the pane options")
				term.waitScreen(t, "reading them now")
				term.send(t, "q")
			},
		},
		{
			name: "a reader inside the workspace follows the pane it was given",
			env:  map[string]string{"TMUX": socket + ",1,0", "TMUX_PANE": scene.pane},
			args: []string{"transcript", "--to", "PANE"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "transcript")
				term.waitScreen(t, "read the pane options")
				term.send(t, "\x1b")
			},
		},
		{
			name: "a subagent of that pane that has written nothing yet",
			env:  map[string]string{"TMUX": ""},
			args: []string{"transcript", "--session", "api", "--to", "PANE", "--agent", "a3f2e1d0"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "a3f2e1d0")
				term.waitScreen(t, "nothing written yet")
				term.send(t, "q")
			},
		},
		{
			name: "no terminal", nonTerminal: true, env: map[string]string{"TMUX": ""},
			args:     []string{"transcript", "--session", "api", "--to", "PANE"},
			wantCode: 1, errHas: []string{"live view and needs a terminal"},
		},
		{
			name: "no pane named", env: map[string]string{"TMUX": ""},
			args: []string{"transcript", "--session", "api"}, wantCode: 1, errHas: []string{"required flag", "to"},
		},
		{
			name: "a pane id that is not one", env: map[string]string{"TMUX": ""},
			args:     []string{"transcript", "--session", "api", "--to", "nope"},
			wantCode: 1, errHas: []string{"follows an agent of this workspace", "pass --to"},
		},
		{
			name: "a pane no session has started in", env: map[string]string{"TMUX": ""},
			args:     []string{"transcript", "--session", "api", "--to", "BARE"},
			wantCode: 1, errHas: []string{"no transcript"},
		},
		{
			name: "outside tmux with no workspace named", env: map[string]string{"TMUX": ""},
			args: []string{"transcript", "--to", "PANE"}, wantCode: 1, errHas: []string{"pass --session"},
		},
		{
			name: "a workspace that is not running", env: map[string]string{"TMUX": ""},
			args: []string{"transcript", "--session", "nope", "--to", "PANE"}, wantCode: 1, errHas: []string{"such workspace: nope"},
		},
		{
			name: "the flags it takes", args: []string{"transcript", "--help"},
			outHas: []string{"--to", "--session", "--agent"},
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
			args := make([]string, len(tc.args))
			for i, a := range tc.args {
				switch a {
				case "PANE":
					args[i] = scene.pane
				case "BARE":
					args[i] = scene.bare
				default:
					args[i] = a
				}
			}
			term := newWatchTerm(t)
			wait := watchRunCLI(t, e, term, args...)
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
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr has no %q:\n%s", want, stderr)
				}
			}
		})
	}
}
