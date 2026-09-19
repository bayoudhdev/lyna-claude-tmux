package tmux_test

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestIntegrationListPanesTranscript stores transcript paths on panes of a
// real server the way the hook stores them, and reads them back through
// list-panes: a path is data, and comes back exactly as it went in, whatever a
// format or the command parser would make of its characters.
func TestIntegrationListPanesTranscript(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	cases := []struct {
		name string
		path string
	}{
		{name: "a plain path", path: "/home/u/.claude/projects/-work-api/5f0c2a1e-7b3d-4c8e-9a10-2b3c4d5e6f70.jsonl"},
		{name: "a path with spaces and a hash", path: "/home/u/My Projects #2/.claude/projects/-work-api/s.jsonl"},
		{name: "a path with quotes and a format", path: `/home/u/it's "x"/#{pane_id}/#[fg=red]/s.jsonl`},
		{name: "a path with a semicolon and braces", path: "/home/u/a;b/{c}/s.jsonl"},
		{name: "a path with accents", path: "/home/José/.claude/projects/-work-été/s.jsonl"},
		{name: "a pane with no transcript"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := srv.Client.Run(ctx, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "base", "sleep 3600")
			if err != nil {
				t.Fatal(err)
			}
			pane := strings.TrimSpace(out)
			t.Cleanup(func() { _, _ = srv.Client.Run(ctx, "kill-pane", "-t", pane) })
			if tc.path != "" {
				if _, err := srv.Client.Batch(ctx, tmux.Command{"set-option", "-p", "-t", pane, tmux.OptTranscript, tc.path}); err != nil {
					t.Fatal(err)
				}
			}
			panes, err := srv.Client.ListPanes(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range panes {
				if p.ID == pane {
					if p.Transcript != tc.path {
						t.Fatalf("transcript %q, want %q", p.Transcript, tc.path)
					}
					return
				}
			}
			t.Fatalf("pane %s is not listed: %+v", pane, panes)
		})
	}
}
