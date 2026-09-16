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

// sleeper keeps a pane alive without depending on the default shell.
var sleeper = tmux.PaneProcess{Argv: []string{"/bin/sh", "-c", "exec sleep 3600"}}

func sleepers(n int) []tmux.PaneProcess {
	procs := make([]tmux.PaneProcess, n)
	for i := range procs {
		procs[i] = sleeper
	}
	return procs
}

func TestIntegrationWorkspaceLayouts(t *testing.T) {
	srv := tmuxtest.Start(t)
	custom, err := layout.Custom("mine", "ws", []layout.CustomPane{
		{Role: "shell"},
		{Role: "claude", Split: "down", Size: 70, Worktree: true},
		{Role: "command", Split: "right", Size: 30, Parent: 1, Command: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	plans := []layout.Plan{custom}
	for _, name := range []string{layout.Solo, layout.Duo, layout.Trio, layout.Quad, layout.Review} {
		p, err := layout.Builtin(name, layout.Options{Session: "ws"})
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, p)
	}
	dir := t.TempDir()
	for i, plan := range plans {
		t.Run(plan.Name, func(t *testing.T) {
			ctx := tmuxtest.Context(t)
			name := "ws" + strconv.Itoa(i)
			built, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
				Session: name, Project: dir, Sandbox: "strict", Width: 200, Height: 50,
				Window: tmux.WindowSpec{Name: plan.Name, Dir: dir, Plan: plan, Procs: sleepers(len(plan.Panes))},
			})
			if err != nil {
				t.Fatal(err)
			}
			panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession(name))
			if err != nil {
				t.Fatal(err)
			}
			if len(panes) != len(plan.Panes) || len(built.Panes) != len(plan.Panes) {
				t.Fatalf("%d panes listed, %d built, plan has %d", len(panes), len(built.Panes), len(plan.Panes))
			}
			byID := map[string]tmux.Pane{}
			for _, p := range panes {
				byID[p.ID] = p
			}
			for j, id := range built.Panes {
				p, found := byID[id]
				if !found {
					t.Fatalf("built pane %s not listed", id)
				}
				if p.Role != string(plan.Panes[j].Role) {
					t.Errorf("pane %d (%s) role %q, want %q", j+1, id, p.Role, plan.Panes[j].Role)
				}
				if p.Active != (j == plan.Focus) {
					t.Errorf("pane %d active = %v, focus is pane %d", j+1, p.Active, plan.Focus+1)
				}
				if p.WindowName != plan.Name {
					t.Errorf("window name %q, want %q", p.WindowName, plan.Name)
				}
			}
			sessions, err := srv.Client.ListSessions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got tmux.Session
			for _, s := range sessions {
				if s.Name == name {
					got = s
				}
			}
			if !got.Managed || got.Project != dir || got.Sandbox != "strict" || got.Layout != plan.Name || got.ID != built.SessionID {
				t.Fatalf("session options = %+v, built %+v", got, built)
			}
		})
	}
}

func TestIntegrationWorkspaceGeometry(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := t.TempDir()
	cases := []struct {
		name   string
		layout string
		// widths are the expected pane widths in plan order.
		widths []int
	}{
		// 200 columns: a split gives the new pane its percentage of the split
		// pane and the old pane the rest minus one border column.
		{name: "duo", layout: layout.Duo, widths: []int{123, 76}},
		{name: "quad", layout: layout.Quad, widths: []int{99, 100, 99, 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := layout.Builtin(tc.layout, layout.Options{Session: "g"})
			if err != nil {
				t.Fatal(err)
			}
			built, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
				Session: "geo-" + tc.name, Width: 200, Height: 50,
				Window: tmux.WindowSpec{Dir: dir, Plan: plan, Procs: sleepers(len(plan.Panes))},
			})
			if err != nil {
				t.Fatal(err)
			}
			for j, id := range built.Panes {
				w, err := srv.Client.Display(ctx, id, "#{pane_width}")
				if err != nil {
					t.Fatal(err)
				}
				if n, _ := strconv.Atoi(w); n < tc.widths[j]-1 || n > tc.widths[j]+1 {
					t.Errorf("pane %d width %s, want %d (plus or minus 1)", j+1, w, tc.widths[j])
				}
			}
		})
	}
}

// readWhenPresent waits for a process in a pane to write path and returns it.
// Panes write their output to a temporary name and rename it into place: a
// file whose every line ends in a newline cannot be told apart from a partial
// write by looking at its content.
func readWhenPresent(t *testing.T, path string) string {
	t.Helper()
	var data []byte
	tmuxtest.WaitFor(t, path, func() bool {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			return false
		}
		data = b
		return true
	})
	return string(data)
}

func TestIntegrationWorkspaceProcesses(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	if _, err := srv.Client.Run(ctx, "set-option", "-g", "default-shell", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dir := filepath.Join(base, `it's "q" $(touch pwned) ;#{pane_id} %H #[fg=red] \; ~`)
	out := filepath.Join(base, "out")
	scripts := filepath.Join(base, "my scripts")
	for _, d := range []string{dir, out, scripts} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(scripts, "it's run;")
	body := "#!/bin/sh\necho single-ran > " + tmux.ShellQuote(filepath.Join(out, "p3.part")) +
		"\nmv " + tmux.ShellQuote(filepath.Join(out, "p3.part")) + " " + tmux.ShellQuote(filepath.Join(out, "p3")) + "\nexec sleep 3600\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	hostileArgs := []string{";", "a;", "{", "}", "#{pane_id}", "$(id)", "it's", " spaced "}
	record := `pwd -P > "$0/p0.pwd.part"; env | grep '^LT_' | sort > "$0/p0.env.part"; printf '%s\n' "$@" > "$0/p0.args.part"; ` +
		`for f in p0.pwd p0.env p0.args; do mv "$0/$f.part" "$0/$f"; done; exec sleep 3600`
	plan := layout.Plan{Name: "procs", Panes: []layout.Pane{
		{Role: layout.RoleClaude},
		{Role: layout.RoleShell, Split: layout.SplitRight, Size: 50},
		{Role: layout.RoleCommand, Split: layout.SplitDown, Size: 50, Parent: 1, Command: "echo"},
		{Role: layout.RoleShell, Split: layout.SplitDown, Size: 50},
	}}
	built, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: "procs", Project: dir, Width: 200, Height: 50,
		Window: tmux.WindowSpec{Name: "#[fg=red] #{pane_id}", Dir: dir, Plan: plan, Procs: []tmux.PaneProcess{
			{Argv: append([]string{"/bin/sh", "-c", record, out}, hostileArgs...), Env: []string{"LT_ZERO=#{pane_id};", "LT_SHARED=zero"}},
			{
				Argv: []string{"/bin/sh", "-c", `env | grep '^LT_' | sort > "$0/p1.env.part"; mv "$0/p1.env.part" "$0/p1.env"; exec sleep 3600`, out}, Env: []string{"LT_ONE=1 $(id) ;"},
				Options: map[string]string{"@lt_settings": dir + "/s.json", "@lt_other": "x"},
			},
			{Shell: "echo shell-ran > " + tmux.ShellQuote(filepath.Join(out, "p2.part")) +
				"; mv " + tmux.ShellQuote(filepath.Join(out, "p2.part")) + " " + tmux.ShellQuote(filepath.Join(out, "p2")) + "; exec sleep 3600"},
			{Argv: []string{script}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name, file, want string
	}{
		{"hostile start directory", "p0.pwd", realDir + "\n"},
		{"first pane env set and not format-expanded", "p0.env", "LT_SHARED=zero\nLT_ZERO=#{pane_id};\n"},
		{"argv passed verbatim", "p0.args", strings.Join(hostileArgs, "\n") + "\n"},
		{"split pane env is its own", "p1.env", "LT_ONE=1 $(id) ;\n"},
		{"shell line runs through the default shell", "p2", "shell-ran\n"},
		{"single argv with spaces and quotes", "p3", "single-ran\n"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if got := readWhenPresent(t, filepath.Join(out, c.file)); got != c.want {
				t.Fatalf("%s = %q, want %q", c.file, got, c.want)
			}
		})
	}

	t.Run("later panes inherit no pane environment", func(t *testing.T) {
		if _, err := srv.Client.Run(ctx, "split-window", "-t", built.Panes[0], "--",
			"/bin/sh", "-c", `{ env | grep '^LT_'; echo end; } > "$0/p4.env.part"; mv "$0/p4.env.part" "$0/p4.env"; exec sleep 3600`, out); err != nil {
			t.Fatal(err)
		}
		if got := readWhenPresent(t, filepath.Join(out, "p4.env")); got != "end\n" {
			t.Fatalf("new pane environment has %q", got)
		}
	})

	t.Run("pane options land on their pane literally", func(t *testing.T) {
		for i, want := range []string{"\x1f", dir + "/s.json\x1fx", "\x1f", "\x1f"} {
			got, err := srv.Client.Display(ctx, built.Panes[i], "#{@lt_settings}\x1f#{@lt_other}")
			if err != nil || got != want {
				t.Fatalf("pane %d options %q, %v; want %q", i, got, err, want)
			}
		}
	})

	t.Run("window name is literal", func(t *testing.T) {
		got, err := srv.Client.Display(ctx, built.WindowID, "#{window_name}")
		if err != nil || got != "#[fg=red] #{pane_id}" {
			t.Fatalf("window name %q, %v", got, err)
		}
	})

	t.Run("no shell expansion of the directory", func(t *testing.T) {
		matches, err := filepath.Glob(filepath.Join(base, "*", "pwned"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("pwned files: %v %v", matches, err)
		}
		if _, err := os.Stat(filepath.Join(base, "pwned")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pwned file in base: %v", err)
		}
	})
}

func TestIntegrationWorkspaceRemainOnExit(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	plan, err := layout.Builtin(layout.Duo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	failing := tmux.PaneProcess{Argv: []string{"/bin/sh", "-c", "exit 7"}}
	built, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: "exits",
		Window:  tmux.WindowSpec{Dir: t.TempDir(), Plan: plan, Procs: []tmux.PaneProcess{failing, failing}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var panes []tmux.Pane
	tmuxtest.WaitFor(t, "the failed shell pane to close", func() bool {
		panes, err = srv.Client.ListPanes(ctx, tmux.ExactSession("exits"))
		return err == nil && len(panes) == 1 && panes[0].Dead
	})
	if panes[0].ID != built.Panes[0] || panes[0].Role != string(layout.RoleClaude) {
		t.Fatalf("remaining pane %+v, want the claude pane %s", panes[0], built.Panes[0])
	}
	status, err := srv.Client.Display(ctx, built.Panes[0], "#{pane_dead_status}")
	if err != nil || status != "7" {
		t.Fatalf("dead status %q, %v", status, err)
	}
}

func TestIntegrationWorkspaceExists(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	plan, err := layout.Builtin(layout.Duo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	spec := tmux.WorkspaceSpec{Session: "twice", Window: tmux.WindowSpec{Dir: t.TempDir(), Plan: plan, Procs: sleepers(2)}}
	if _, err := srv.Client.CreateWorkspace(ctx, spec); err != nil {
		t.Fatal(err)
	}
	solo, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	spec.Window.Plan, spec.Window.Procs, spec.Project = solo, sleepers(1), "/elsewhere"
	if _, err := srv.Client.CreateWorkspace(ctx, spec); !errors.Is(err, tmux.ErrExists) {
		t.Fatalf("second create err = %v, want ErrExists", err)
	}
	panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("twice"))
	if err != nil || len(panes) != 2 {
		t.Fatalf("existing session changed: %d panes, %v", len(panes), err)
	}
	project, err := srv.Client.ShowOption(ctx, "", tmux.ExactSession("twice"), tmux.OptProject)
	if err != nil || project != "" {
		t.Fatalf("existing session options changed: %q, %v", project, err)
	}
}

func TestIntegrationAddWindow(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := t.TempDir()
	solo, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := srv.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{Session: "tasks", Window: tmux.WindowSpec{Dir: dir, Plan: solo, Procs: sleepers(1)}})
	if err != nil {
		t.Fatal(err)
	}
	duo, err := layout.Builtin(layout.Duo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		detached bool
	}{
		{name: "detached keeps the current window", detached: true},
		{name: "attached selects the new window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := srv.Client.Display(ctx, tmux.ExactSession("tasks"), "#{window_id}")
			if err != nil {
				t.Fatal(err)
			}
			built, err := srv.Client.AddWindow(ctx, "tasks", tmux.WindowSpec{Name: "task", Dir: dir, Plan: duo, Procs: sleepers(2)}, tc.detached)
			if err != nil {
				t.Fatal(err)
			}
			current, err := srv.Client.Display(ctx, tmux.ExactSession("tasks"), "#{window_id}")
			if err != nil {
				t.Fatal(err)
			}
			want := built.WindowID
			if tc.detached {
				want = before
			}
			if current != want {
				t.Fatalf("current window %s, want %s", current, want)
			}
			for j, id := range built.Panes {
				role, err := srv.Client.ShowOption(ctx, "-p", id, tmux.OptRole)
				if err != nil || role != string(duo.Panes[j].Role) {
					t.Fatalf("pane %s role %q (%v), want %q", id, role, err, duo.Panes[j].Role)
				}
			}
			if built.SessionID != first.SessionID || built.WindowID == first.WindowID {
				t.Fatalf("built %+v in session %+v", built, first)
			}
		})
	}
}
