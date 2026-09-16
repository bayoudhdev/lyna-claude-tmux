package tmux_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// clickTimeout is longer than the 300 ms tmux waits for a second press of the
// same button before it calls the pair a double click.
const clickTimeout = 400 * time.Millisecond

// TestE2EWindowMenuLayoutItems opens the window menu on a window tab that is
// not the current window and picks each layout: lyna-tmux must receive the
// layout name and the active pane of the clicked window, whatever the name of
// the directory the pane runs in.
func TestE2EWindowMenuLayoutItems(t *testing.T) {
	w := startWorkspace(t, `it's #{pane_id} ;`)
	// Both windows are named, as every window a workspace opens is: an
	// automatic name follows the process in the pane, and a tab that grows
	// from "sh" to "bash" moves every tab right of it while a click is on its
	// way to one.
	w.run(t, "rename-window", "-t", "=main:1", "claude")
	w.run(t, "new-window", "-d", "-n", "shell", "-t", "=main:", "-c", w.dir)
	clicked := w.run(t, "display-message", "-p", "-t", "=main:2", "#{pane_id}")
	steps := []struct {
		key, layout string
	}{
		{"1", "solo"},
		{"3", "trio"},
		{"5", "review"},
	}
	for i, st := range steps {
		t.Run(st.layout, func(t *testing.T) {
			w.WaitScreen(t, "2:shell", false)
			x, y := w.statusCell(t, "2:shell")
			// tmux reads a second press within 300 ms as a SecondClick, which
			// goes to another key table and never opens the menu. Waiting that
			// out is what a user does between two right clicks, and it keeps
			// the press from being resolved against the click before it.
			if i > 0 {
				time.Sleep(clickTimeout)
			}
			w.Click(t, 2, x, y)
			w.WaitScreen(t, windowMenu, false)
			w.Keys(t, st.key)
			tmuxtest.WaitFor(t, "layout call", func() bool { return len(w.calls(t)) > i })
			_, args, _ := strings.Cut(w.calls(t)[i], "|")
			if want := "layout " + st.layout + " --pane " + clicked; args != want {
				t.Fatalf("lyna-tmux called with %q, want %q", args, want)
			}
			w.WaitScreen(t, windowMenu, true)
		})
	}
}
