package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// hostileDir creates a directory whose name is valid shell, tmux command and
// format syntax, so every command that carries it must treat it as data.
func hostileDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, `it's "q" #{pane_id} #[fg=red] %H ;`)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIntegrationDescribeAndSplitPane(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := hostileDir(t)
	solo, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	built, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: "ws", Project: dir, Sandbox: "standard", Isolation: "bash", Width: 200, Height: 50,
		Window: tmux.WindowSpec{Name: "claude", Dir: dir, Plan: solo, Procs: sleepers(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second window that is not the current one: splits and tags must land
	// in the window of the split pane, not in the session's current window.
	other, err := srv.Client.AddWindow(ctx, "ws", tmux.WindowSpec{Name: "other", Dir: dir, Plan: solo, Procs: sleepers(1)}, true)
	if err != nil {
		t.Fatal(err)
	}

	first, err := srv.Client.DescribePane(ctx, built.Panes[0])
	if err != nil {
		t.Fatal(err)
	}
	want := tmux.PaneContext{
		ID: built.Panes[0], Session: "ws", WindowID: built.WindowID, Path: dir, Project: dir,
		Sandbox: "standard", Isolation: "bash", WindowWidth: 200, WindowHeight: 50,
	}
	if first != want {
		t.Fatalf("DescribePane = %+v, want %+v", first, want)
	}
	if bySession, err := srv.Client.DescribePane(ctx, tmux.ExactSession("ws")); err != nil || bySession != first {
		t.Fatalf("session target describes %+v (%v), want the current window's active pane %+v", bySession, err, first)
	}
	if _, err := srv.Client.DescribePane(ctx, "%99999"); !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("missing pane: err = %v, want ErrNotFound", err)
	}

	geometry := func(t *testing.T, pane string) (left, top int) {
		t.Helper()
		out, err := srv.Client.Display(ctx, pane, "#{pane_left} #{pane_top}")
		if err != nil {
			t.Fatal(err)
		}
		l, tp, _ := strings.Cut(out, " ")
		left, _ = strconv.Atoi(l)
		top, _ = strconv.Atoi(tp)
		return left, top
	}
	steps := []struct {
		name   string
		pane   string
		window string
		down   bool
		role   layout.Role
		proc   tmux.PaneProcess
	}{
		{name: "shell to the right", pane: built.Panes[0], window: built.WindowID, role: layout.RoleShell},
		{
			name: "claude below with options", pane: built.Panes[0], window: built.WindowID, down: true, role: layout.RoleClaude,
			proc: tmux.PaneProcess{Argv: sleeper.Argv, Env: []string{"LT_SPLIT=1"}, Options: map[string]string{tmux.OptSettings: dir + "/s.json"}},
		},
		{name: "pane of a background window", pane: other.Panes[0], window: other.WindowID, role: layout.RoleChanges, proc: sleeper},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			id, err := srv.Client.SplitPane(ctx, tmux.SplitSpec{Pane: st.pane, Window: st.window, Down: st.down, Dir: dir, Role: st.role, Proc: st.proc})
			if err != nil {
				t.Fatal(err)
			}
			got, err := srv.Client.Display(ctx, id, "#{window_id}|#{pane_active}|#{"+tmux.OptRole+"}|#{"+tmux.OptSettings+"}|#{pane_current_path}")
			if err != nil {
				t.Fatal(err)
			}
			wantRow := st.window + "|1|" + string(st.role) + "|" + st.proc.Options[tmux.OptSettings] + "|" + dir
			if got != wantRow {
				t.Fatalf("new pane %s = %q, want %q", id, got, wantRow)
			}
			pl, pt := geometry(t, st.pane)
			nl, nt := geometry(t, id)
			if st.down && (nt <= pt || nl != pl) || !st.down && (nl <= pl || nt != pt) {
				t.Fatalf("split down=%v: parent at %d,%d, new pane at %d,%d", st.down, pl, pt, nl, nt)
			}
			if st.role != layout.RoleClaude {
				return
			}
			if remain, err := srv.Client.ShowOption(ctx, "-p", id, "remain-on-exit"); err != nil || remain != "failed" {
				t.Fatalf("claude pane remain-on-exit = %q (%v)", remain, err)
			}
		})
	}
	if cur, err := srv.Client.Display(ctx, tmux.ExactSession("ws"), "#{window_id}"); err != nil || cur != built.WindowID {
		t.Fatalf("current window %q (%v), want %s unchanged", cur, err, built.WindowID)
	}
	if _, err := srv.Client.SplitPane(ctx, tmux.SplitSpec{Pane: "%99999", Window: built.WindowID, Dir: dir, Role: layout.RoleShell}); !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("split of a missing pane: err = %v, want ErrNotFound", err)
	}
}
