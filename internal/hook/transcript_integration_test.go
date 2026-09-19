package hook

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestIntegrationTranscript runs sessions starting in a pane of a real server
// and reads back, the way the agents rail reads it, the transcript the pane
// keeps.
func TestIntegrationTranscript(t *testing.T) {
	srv := startLive(t)
	const first = "/home/u/.claude/projects/-work-api/5f0c2a1e.jsonl"
	odd := `/home/u/it's "My Projects" #2; {x}/.claude/projects/-work-api/#{pane_id}.jsonl`
	start := func(path string) hookStep {
		return hookStep{"SessionStart", mustJSON(map[string]string{"cwd": "/", "source": "startup", "transcript_path": path})}
	}
	cases := []struct {
		name  string
		steps []hookStep
		want  string
	}{
		{name: "a session start keeps its transcript", steps: []hookStep{start(first)}, want: first},
		{name: "a path is kept as data", steps: []hookStep{start(odd)}, want: odd},
		{
			name:  "a new session replaces the transcript",
			steps: []hookStep{start(first), {"SessionStart", `{"cwd":"/","source":"clear","transcript_path":"/home/u/.claude/projects/-work-api/9b1e.jsonl"}`}},
			want:  "/home/u/.claude/projects/-work-api/9b1e.jsonl",
		},
		{name: "a refused path leaves none", steps: []hookStep{start(first), start("/home/u/../etc/passwd.jsonl")}},
		{name: "the events of the session leave it alone", steps: []hookStep{
			start(first),
			{"UserPromptSubmit", `{"transcript_path":"/elsewhere/x.jsonl"}`},
			{"Stop", `{"cwd":"/","transcript_path":"/elsewhere/x.jsonl"}`},
			{"SessionEnd", `{"reason":"other"}`},
		}, want: first},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane, _, _ := srv.newSession(t, "tr-"+strconv.Itoa(i))
			for _, st := range tc.steps {
				Run(tmuxtest.Context(t), Input{Event: st.event, Stdin: strings.NewReader(st.payload), Getenv: srv.env(pane, nil)}, srv.deps())
			}
			panes, err := srv.Client.ListPanes(tmuxtest.Context(t), tmux.ExactSession("tr-"+strconv.Itoa(i)))
			if err != nil || len(panes) != 1 {
				t.Fatalf("panes %+v, %v", panes, err)
			}
			if panes[0].Transcript != tc.want {
				t.Fatalf("transcript %q, want %q", panes[0].Transcript, tc.want)
			}
		})
	}
}
