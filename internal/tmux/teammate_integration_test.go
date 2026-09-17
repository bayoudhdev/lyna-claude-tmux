package tmux_test

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestIntegrationTeammateLayout plays out what a spawning team does to a
// window on a real server: the workspace remembers the arrangement the user
// has, Claude Code splits a pane in and tiles the window for itself, and the
// teammate is either kept beside the lead or moved to a window of its own,
// which is what puts the arrangement back.
func TestIntegrationTeammateLayout(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	duo, err := layout.Builtin(layout.Duo, layout.Options{SplitRatio: 62})
	if err != nil {
		t.Fatal(err)
	}
	built, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: "ws", Project: t.TempDir(), Sandbox: "standard", Isolation: "bash", Width: 200, Height: 50,
		Window: tmux.WindowSpec{Name: "claude", Dir: t.TempDir(), Plan: duo, Procs: sleepers(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	lead, window := built.Panes[0], built.WindowID
	windowLayout := func() string {
		t.Helper()
		out, err := srv.Client.Display(ctx, window, "#{window_layout}")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	remembered := func() string {
		t.Helper()
		out, err := srv.Client.ShowOption(ctx, "-w", window, tmux.OptWinLayout)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The user's own arrangement, stored the way a hook stores it.
	if _, err := srv.Client.Batch(ctx, tmux.RememberLayout(lead)...); err != nil {
		t.Fatal(err)
	}
	user := windowLayout()
	if remembered() != user {
		t.Fatalf("remembered %q, want the arrangement of the window %q", remembered(), user)
	}

	// Claude Code opens a teammate: a pane of its own, then the whole window
	// tiled for it.
	teammate, err := srv.Client.SplitPane(ctx, tmux.SplitSpec{
		Pane: lead, Window: window, Dir: t.TempDir(), Role: layout.RoleClaude,
		Proc: tmux.PaneProcess{Argv: []string{"/bin/sh", "-c", "exec sleep 3600"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Batch(ctx, tmux.AdoptTeammate(tmux.Teammate{Pane: teammate, Agent: "review-api"})...); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "select-layout", "-t", window, "main-vertical"); err != nil {
		t.Fatal(err)
	}
	// A hook firing now must not store the arrangement of a window holding a
	// teammate: it is Claude Code's arrangement, not the user's.
	if _, err := srv.Client.Batch(ctx, tmux.RememberLayout(lead)...); err != nil {
		t.Fatal(err)
	}
	if remembered() != user {
		t.Fatalf("remembered %q while a teammate was in the window, want %q", remembered(), user)
	}

	// Kept beside the lead: the lead takes the share the workspace gives it.
	if _, err := srv.Client.Batch(ctx, tmux.TileAgents(window, lead)...); err != nil {
		t.Fatal(err)
	}
	width, err := srv.Client.Display(ctx, lead, "#{pane_width}")
	if err != nil {
		t.Fatal(err)
	}
	if want := "80"; width != want {
		t.Fatalf("lead width %s, want %s of a 200 cell window", width, want)
	}

	// Moved to a window of its own: the window it leaves is the user's again.
	if _, err := srv.Client.Batch(ctx, tmux.BreakOutTeammate(teammate, window, "review-api", user)...); err != nil {
		t.Fatal(err)
	}
	if got := windowLayout(); got != user {
		t.Fatalf("window arrangement %q, want %q", got, user)
	}
	windows, err := srv.Client.Run(ctx, "list-windows", "-t", tmux.ExactSession("ws"), "-F", "#{window_name}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(windows, "review-api") {
		t.Fatalf("windows %q, want one named after the teammate", windows)
	}
	// The user is left where they were working.
	current, err := srv.Client.Display(ctx, tmux.ExactSession("ws"), "#{window_id}")
	if err != nil {
		t.Fatal(err)
	}
	if current != window {
		t.Fatalf("current window %s, want %s", current, window)
	}

	// With the last teammate gone, the window is followed again.
	if _, err := srv.Client.Batch(ctx, tmux.RememberLayout(lead)...); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "select-layout", "-t", window, "even-vertical"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Batch(ctx, tmux.RememberLayout(lead)...); err != nil {
		t.Fatal(err)
	}
	if got := remembered(); got != windowLayout() {
		t.Fatalf("remembered %q, want the arrangement the user moved to %q", got, windowLayout())
	}
}
