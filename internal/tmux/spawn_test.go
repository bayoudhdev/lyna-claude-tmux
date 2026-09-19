package tmux_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestPasteLine(t *testing.T) {
	cases := []struct {
		name, pane, text string
		want             []tmux.Command
	}{
		{
			name: "a message for an agent", pane: "%3", text: "Start a api-developer agent, and tell it: read the router",
			want: []tmux.Command{
				{"set-buffer", "-b", "lyna-tmux-spawn", "--", "Start a api-developer agent, and tell it: read the router"},
				{"paste-buffer", "-d", "-p", "-b", "lyna-tmux-spawn", "-t", "%3"},
				{"send-keys", "-t", "%3", "Enter"},
			},
		},
		{
			name: "text a tmux command would read as options", pane: "%0", text: "--settings /tmp/x.json",
			want: []tmux.Command{
				{"set-buffer", "-b", "lyna-tmux-spawn", "--", "--settings /tmp/x.json"},
				{"paste-buffer", "-d", "-p", "-b", "lyna-tmux-spawn", "-t", "%0"},
				{"send-keys", "-t", "%0", "Enter"},
			},
		},
		{
			// A paste that carried the sequence ending a bracketed paste would
			// have the rest of it read as keys; lines would be submitted one by
			// one.
			name: "text that would break out of the paste", pane: "%3",
			text: "fix the router\x1b[201~\x03rm -rf ~\n\u009b31mnow\r",
			want: []tmux.Command{
				{"set-buffer", "-b", "lyna-tmux-spawn", "--", "fix the routerrm -rf ~ now"},
				{"paste-buffer", "-d", "-p", "-b", "lyna-tmux-spawn", "-t", "%3"},
				{"send-keys", "-t", "%3", "Enter"},
			},
		},
		{name: "nothing left to type", pane: "%3", text: " \x1b[2J\n\t"},
		{name: "a pane named instead of identified", pane: "api", text: "hello"},
		{name: "a pane selected by pattern", pane: "%", text: "hello"},
		{name: "no pane at all", text: "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tmux.PasteLine(tc.pane, tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("PasteLine =\n%q\nwant\n%q", got, tc.want)
			}
			for i := range got {
				if !slices.Equal(got[i], tc.want[i]) {
					t.Fatalf("command %d =\n%q\nwant\n%q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestIntegrationPasteLineTypesAtThePane runs the commands against a real tmux
// server and reads what the program in the pane was given: the whole line, and
// the newline that submits it.
func TestIntegrationPasteLineTypesAtThePane(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	out := filepath.Join(t.TempDir(), "typed.txt")
	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "ws", "-x", "80", "-y", "24", "cat > "+out); err != nil {
		t.Fatal(err)
	}
	pane, err := srv.Client.Display(ctx, tmux.ExactSession("ws"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	const line = "Start a api-developer agent in a git worktree of its own named review-api, and tell it: read the router"
	if _, err := srv.Client.Batch(ctx, tmux.PasteLine(pane, line)...); err != nil {
		t.Fatal(err)
	}
	// cat writes the line out once its own input has a newline in it, which is
	// what the Enter of the paste sends.
	tmuxtest.WaitFor(t, "the pane to be typed at", func() bool {
		b, err := os.ReadFile(out)
		return err == nil && strings.Contains(string(b), line+"\n")
	})
}
