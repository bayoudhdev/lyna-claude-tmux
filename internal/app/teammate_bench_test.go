package app

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// BenchmarkTeammateOpens measures what a team waits for between the pane
// Claude Code splits in for a teammate and the pane of ours it becomes: the
// launcher reads the pane, labels it, puts our border back and arranges the
// window. It runs against a real tmux server, so the number is the round trips
// as well as the work.
//
// The agent itself is not started here: the launcher execs it afterwards, and
// what it costs to start is the agent's, not ours.
func BenchmarkTeammateOpens(b *testing.B) {
	ctx := tmuxtest.Context(b)
	h := newTestHost(b)
	s := openServer(b, h)
	scene := openTeammateScene(b, h, s, 240, 60)
	// The pane the scene opened is the one the first iteration takes over; every
	// iteration after it splits its own and closes it again.
	pane := scene.pane
	b.ReportAllocs()
	for b.Loop() {
		h.env["TMUX_PANE"] = pane
		h.refreshEnviron()
		if _, err := Teammate(ctx, h.Host, TeammateRequest{ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: 3}); err != nil {
			b.Fatalf("Teammate: %v", err)
		}
		b.StopTimer()
		if _, err := s.Client.Run(ctx, "kill-pane", "-t", pane); err != nil {
			b.Fatalf("kill-pane: %v", err)
		}
		out, err := s.Client.Run(ctx, "split-window", "-d", "-t", scene.lead, "-h", "-l", "70%", "-P", "-F", "#{pane_id}", "sleep 3600")
		if err != nil {
			b.Fatalf("split-window: %v", err)
		}
		pane = strings.TrimSpace(out)
		b.StartTimer()
	}
}
