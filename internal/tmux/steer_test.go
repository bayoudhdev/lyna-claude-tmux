package tmux_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestStopPane(t *testing.T) {
	cases := []struct {
		name, pane string
		want       []tmux.Command
	}{
		{
			name: "the pane of a teammate", pane: "%7",
			want: []tmux.Command{
				{"run-shell", "-C", "-t", "%7", tmux.AgentsSignal},
				{"kill-pane", "-t", "%7"},
			},
		},
		{name: "a pane named instead of identified", pane: "review-api"},
		{name: "a pane selected by pattern", pane: "%"},
		{name: "a session target", pane: "=api:"},
		{name: "no pane at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tmux.StopPane(tc.pane)
			if len(got) != len(tc.want) {
				t.Fatalf("StopPane =\n%q\nwant\n%q", got, tc.want)
			}
			for i := range got {
				if !slices.Equal(got[i], tc.want[i]) {
					t.Fatalf("command %d =\n%q\nwant\n%q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestIntegrationStopPaneClosesOnlyThatPane runs the commands against a real
// tmux server: the pane named is closed, every other pane of the workspace is
// left running, and the rails of the workspace are told.
func TestIntegrationStopPaneClosesOnlyThatPane(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "ws", "-x", "120", "-y", "40", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	lead, err := srv.Client.Display(ctx, tmux.ExactSession("ws"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	split := func() string {
		t.Helper()
		out, err := srv.Client.Run(ctx, "split-window", "-d", "-t", lead, "-P", "-F", "#{pane_id}", "sleep 3600")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	mate, shell := split(), split()
	session, err := srv.Client.Display(ctx, lead, "#{session_id}")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := srv.Client.Batch(ctx, tmux.StopPane(mate)...); err != nil {
		t.Fatal(err)
	}
	panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range panes {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	want := []string{lead, shell}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("panes left %q, want %q", ids, want)
	}
	// tmux keeps a signal sent while nobody waits, so a wait on the channel
	// returns at once when the stop signaled it, and blocks until the test
	// context ends when it did not.
	if _, err := srv.Client.Run(ctx, "wait-for", tmux.AgentsChannel(session)); err != nil {
		t.Fatalf("the agents channel was not signaled: %v", err)
	}
}
