package tmux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
)

// scripted answers each tmux invocation with the next result and records the
// arguments and whether the context was already done.
type scripted struct {
	results []Result
	calls   [][]string
	done    []bool
}

func (s *scripted) Exec(ctx context.Context, _ string, args []string) (Result, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	s.done = append(s.done, ctx.Err() != nil)
	if len(s.calls) > len(s.results) {
		return Result{}, nil
	}
	return s.results[len(s.calls)-1], nil
}

func ok(stdout string) Result { return Result{Stdout: []byte(stdout)} }

func fail(stdout, stderr string) Result {
	return Result{Stdout: []byte(stdout), Stderr: []byte(stderr), ExitCode: 1}
}

const created = "$1\x1f@2\x1f%3\n"

func mustPlan(t *testing.T, name string) layout.Plan {
	t.Helper()
	p, err := layout.Builtin(name, layout.Options{Session: "api"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func duoSpec(t *testing.T) WorkspaceSpec {
	t.Helper()
	return WorkspaceSpec{
		Session:   "api",
		Project:   "/src/api",
		Sandbox:   "standard",
		Isolation: "bash",
		Width:     200,
		Height:    50,
		Window: WindowSpec{
			Name: "api",
			Dir:  "/src/#[api]",
			Plan: mustPlan(t, layout.Duo),
			Procs: []PaneProcess{
				{
					Argv: []string{"/bin/claude", "--name", "api;"}, Env: []string{"LYNA_TMUX_MANAGED=1", "LYNA_TMUX_SESSION=api"},
					Options: map[string]string{"@lt_settings": "/s/a b.json", "@lt_a": "v;"},
				},
				{Options: map[string]string{"@lt_x": "1"}},
			},
		},
	}
}

func TestCreateWorkspaceCommands(t *testing.T) {
	rec := &scripted{results: []Result{ok(created), ok("%4\n"), ok("")}}
	built, err := New(Options{Executor: rec}).CreateWorkspace(context.Background(), duoSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{
			"new-session", "-d", "-s", "api", "-c", "/src/#{?,,##}[api]", "-P", "-F", "#{session_id}\x1f#{window_id}\x1f#{pane_id}", "-n", "api",
			"-x", "200", "-y", "50", "-e", "LYNA_TMUX_MANAGED=1", "-e", "LYNA_TMUX_SESSION=api", "--", "/bin/claude", "--name", `api\;`,
			";", "set-environment", "-u", "-t", "=api:", "LYNA_TMUX_MANAGED",
			";", "set-environment", "-u", "-t", "=api:", "LYNA_TMUX_SESSION",
			";", "set-option", "-t", "=api:", "@lt_managed", "1",
			";", "set-option", "-t", "=api:", "@lt_project", "/src/api",
			";", "set-option", "-t", "=api:", "@lt_sandbox", "standard",
			";", "set-option", "-t", "=api:", "@lt_isolation", "bash",
			";", "set-option", "-t", "=api:", "@lt_layout", "duo",
			";", "set-option", "-p", "-t", "=api:", "@lt_role", "claude",
			";", "set-option", "-p", "-t", "=api:", "remain-on-exit", "failed",
			";", "set-option", "-p", "-t", "=api:", "@lt_a", `v\;`,
			";", "set-option", "-p", "-t", "=api:", "@lt_settings", "/s/a b.json",
		},
		{
			"split-window", "-t", "%3", "-h", "-l", "38%", "-c", "/src/#{?,,##}[api]", "-P", "-F", "#{pane_id}",
			";", "set-option", "-p", "-t", "@2", "@lt_role", "shell",
			";", "set-option", "-p", "-t", "@2", "@lt_x", "1",
		},
		{"select-pane", "-t", "%3"},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("calls:\n%q\nwant:\n%q", rec.calls, want)
	}
	wantBuilt := Built{SessionID: "$1", WindowID: "@2", Panes: []string{"%3", "%4"}}
	if !reflect.DeepEqual(built, wantBuilt) {
		t.Fatalf("built = %+v, want %+v", built, wantBuilt)
	}
}

func TestCreateWorkspaceSplitParents(t *testing.T) {
	rec := &scripted{results: []Result{ok(created), ok("%4\n"), ok("%5\n"), ok("%6\n"), ok("")}}
	spec := duoSpec(t)
	spec.Window.Plan = mustPlan(t, layout.Quad)
	spec.Window.Procs = make([]PaneProcess, 4)
	built, err := New(Options{Executor: rec}).CreateWorkspace(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	// Quad splits pane 1 right, pane 1 down, then pane 2 down.
	wantTargets := []struct{ target, dir string }{{"%3", "-h"}, {"%3", "-v"}, {"%4", "-v"}}
	for i, w := range wantTargets {
		call := rec.calls[i+1]
		if call[0] != "split-window" || call[2] != w.target || call[3] != w.dir {
			t.Fatalf("split %d = %q, want target %s %s", i+1, call[:4], w.target, w.dir)
		}
		if !strings.Contains(strings.Join(call, " "), "@lt_role claude ; set-option -p -t @2 remain-on-exit failed") {
			t.Fatalf("split %d does not tag a claude pane: %q", i+1, call)
		}
	}
	if got := strings.Join(built.Panes, ","); got != "%3,%4,%5,%6" {
		t.Fatalf("panes = %s", got)
	}
}

func TestAddWindowCommands(t *testing.T) {
	cases := []struct {
		name     string
		detached bool
		last     []string
	}{
		{name: "attached", last: []string{"select-pane", "-t", "%3"}},
		{name: "detached", detached: true, last: []string{"select-pane", "-t", "%3", ";", "last-window", "-t", "=api:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: []Result{ok(created), ok("")}}
			w := WindowSpec{Dir: "/src/api", Plan: mustPlan(t, layout.Solo), Procs: []PaneProcess{{Shell: "make watch"}}}
			if _, err := New(Options{Executor: rec}).AddWindow(context.Background(), "api", w, tc.detached); err != nil {
				t.Fatal(err)
			}
			want := [][]string{
				{
					"new-window", "-t", "=api:", "-c", "/src/api", "-P", "-F", "#{session_id}\x1f#{window_id}\x1f#{pane_id}", "--", "make watch",
					";", "set-option", "-p", "-t", "=api:", "@lt_role", "claude",
					";", "set-option", "-p", "-t", "=api:", "remain-on-exit", "failed",
				},
				tc.last,
			}
			if !reflect.DeepEqual(rec.calls, want) {
				t.Fatalf("calls:\n%q\nwant:\n%q", rec.calls, want)
			}
		})
	}
}

func TestPaneProcessArgs(t *testing.T) {
	cases := []struct {
		name string
		proc PaneProcess
		want []string
	}{
		{name: "default shell", proc: PaneProcess{}, want: nil},
		{name: "env only", proc: PaneProcess{Env: []string{"A=1"}}, want: []string{"-e", "A=1"}},
		{name: "argv executed directly", proc: PaneProcess{Argv: []string{"claude", "--name", "x y"}}, want: []string{"--", "claude", "--name", "x y"}},
		{name: "single argv kept one word", proc: PaneProcess{Argv: []string{"/opt/my tools/it's"}}, want: []string{"--", "/bin/sh", "-c", `exec "$0"`, "/opt/my tools/it's"}},
		{name: "shell line", proc: PaneProcess{Shell: "npm run dev", Env: []string{"B=2"}}, want: []string{"-e", "B=2", "--", "npm run dev"}},
		{name: "leading dash program", proc: PaneProcess{Argv: []string{"-weird", "x"}}, want: []string{"--", "-weird", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.proc.args(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("args() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkspaceValidate(t *testing.T) {
	cases := []struct {
		name    string
		mod     func(*WorkspaceSpec)
		wantErr string
	}{
		{name: "valid", mod: func(*WorkspaceSpec) {}},
		{name: "bad session name", mod: func(s *WorkspaceSpec) { s.Session = "a:b" }, wantErr: "contains"},
		{name: "empty session name", mod: func(s *WorkspaceSpec) { s.Session = "" }, wantErr: "empty"},
		{name: "negative size", mod: func(s *WorkspaceSpec) { s.Width = -1 }, wantErr: "negative"},
		{name: "nul in project", mod: func(s *WorkspaceSpec) { s.Project = "a\x00" }, wantErr: "NUL"},
		{name: "relative dir", mod: func(s *WorkspaceSpec) { s.Window.Dir = "src/api" }, wantErr: "not absolute"},
		{name: "nul in dir", mod: func(s *WorkspaceSpec) { s.Window.Dir = "/src\x00" }, wantErr: "NUL"},
		{name: "nul in window name", mod: func(s *WorkspaceSpec) { s.Window.Name = "a\x00" }, wantErr: "NUL"},
		{name: "process count mismatch", mod: func(s *WorkspaceSpec) { s.Window.Procs = s.Window.Procs[:1] }, wantErr: "2 panes but 1 processes"},
		{name: "invalid plan", mod: func(s *WorkspaceSpec) { s.Window.Plan.Focus = 5 }, wantErr: "focus"},
		{name: "argv and shell", mod: func(s *WorkspaceSpec) { s.Window.Procs[1] = PaneProcess{Argv: []string{"a"}, Shell: "b"} }, wantErr: "pane 2: a pane runs either"},
		{name: "empty program", mod: func(s *WorkspaceSpec) { s.Window.Procs[1] = PaneProcess{Argv: []string{"", "x"}} }, wantErr: "empty program"},
		{name: "nul in argv", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Argv = []string{"a", "b\x00"} }, wantErr: "NUL"},
		{name: "nul in shell", mod: func(s *WorkspaceSpec) { s.Window.Procs[1] = PaneProcess{Shell: "a\x00"} }, wantErr: "NUL"},
		{name: "env without equals", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Env = []string{"NOVALUE"} }, wantErr: "not KEY=VALUE"},
		{name: "env empty key", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Env = []string{"=x"} }, wantErr: "not KEY=VALUE"},
		{name: "env key leading digit", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Env = []string{"1A=x"} }, wantErr: "not KEY=VALUE"},
		{name: "env key with dash", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Env = []string{"A-B=x"} }, wantErr: "not KEY=VALUE"},
		{name: "env value nul", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Env = []string{"A=\x00"} }, wantErr: "NUL"},
		{name: "env value may hold anything else", mod: func(s *WorkspaceSpec) { s.Window.Procs[0].Env = []string{"A_1=#{x}; =$(id)"} }},
		{name: "option without @", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{"lt_x": "1"} }, wantErr: "pane 2: pane option \"lt_x\""},
		{name: "option bare @", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{"@": "1"} }, wantErr: "not a user option"},
		{name: "option with space", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{"@a b": "1"} }, wantErr: "not a user option"},
		{name: "option with separator", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{"@a;b": "1"} }, wantErr: "not a user option"},
		{name: "option overriding the role", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{OptRole: "claude"} }, wantErr: "not a user option"},
		{name: "option value nul", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{"@a": "\x00"} }, wantErr: "NUL"},
		{name: "option name characters", mod: func(s *WorkspaceSpec) { s.Window.Procs[1].Options = map[string]string{"@Lt_9-x": "#{pane_id}; $(id)"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := duoSpec(t)
			tc.mod(&spec)
			rec := &scripted{results: []Result{ok(created), ok("%4\n"), ok("")}}
			_, err := New(Options{Executor: rec}).CreateWorkspace(context.Background(), spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("invalid spec reached tmux: %q", rec.calls)
			}
		})
	}
}

func TestCreateWorkspaceFailures(t *testing.T) {
	kill := []string{"kill-session", "-t", "=api:"}
	cases := []struct {
		name       string
		results    []Result
		wantExists bool
		wantKill   bool
		wantErr    string
	}{
		{name: "duplicate session is left alone", results: []Result{fail("", "duplicate session: api")}, wantExists: true, wantErr: "duplicate session"},
		{name: "new-session failure without output", results: []Result{fail("", "create window failed")}, wantErr: "create window failed"},
		{name: "option failure after creation", results: []Result{fail(created, "invalid option"), ok("")}, wantKill: true, wantErr: "invalid option"},
		{name: "unexpected new-session output", results: []Result{ok("garbage\n"), ok("")}, wantKill: true, wantErr: "unexpected output"},
		{name: "split failure", results: []Result{ok(created), fail("", "no space for new pane"), ok("")}, wantKill: true, wantErr: "no space for new pane"},
		{name: "unexpected split output", results: []Result{ok(created), ok("oops\n"), ok("")}, wantKill: true, wantErr: "unexpected output"},
		{name: "focus failure", results: []Result{ok(created), ok("%4\n"), fail("", "can't find pane: %3"), ok("")}, wantKill: true, wantErr: "can't find pane"},
		{name: "cleanup of a vanished session stays quiet", results: []Result{ok(created), fail("", "boom"), fail("", "can't find session: api")}, wantKill: true, wantErr: "boom"},
		{name: "cleanup failure is reported", results: []Result{ok(created), fail("", "boom"), fail("", "permission denied")}, wantKill: true, wantErr: "cleanup: tmux kill-session: permission denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: tc.results}
			_, err := New(Options{Executor: rec}).CreateWorkspace(context.Background(), duoSpec(t))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if got := errors.Is(err, ErrExists); got != tc.wantExists {
				t.Fatalf("Is(ErrExists) = %v, want %v", got, tc.wantExists)
			}
			killed := reflect.DeepEqual(rec.calls[len(rec.calls)-1], kill)
			if killed != tc.wantKill {
				t.Fatalf("last call %q, want kill %v", rec.calls[len(rec.calls)-1], tc.wantKill)
			}
			if strings.Contains(tc.wantErr, "boom") && !strings.Contains(err.Error(), "boom") {
				t.Fatalf("cleanup hid the cause: %v", err)
			}
		})
	}
}

func TestAddWindowFailures(t *testing.T) {
	cases := []struct {
		name     string
		results  []Result
		wantKill bool
		wantErr  string
	}{
		{name: "missing session", results: []Result{fail("", "can't find session: api")}, wantErr: "can't find session"},
		{name: "unexpected output", results: []Result{ok("x\n")}, wantErr: "unexpected output"},
		{name: "option failure after creation", results: []Result{fail(created, "bad"), ok("")}, wantKill: true, wantErr: "bad"},
		{name: "split failure", results: []Result{ok(created), fail("", "no space for new pane"), ok("")}, wantKill: true, wantErr: "no space"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: tc.results}
			w := duoSpec(t).Window
			_, err := New(Options{Executor: rec}).AddWindow(context.Background(), "api", w, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			killed := reflect.DeepEqual(rec.calls[len(rec.calls)-1], []string{"kill-window", "-t", "@2"})
			if killed != tc.wantKill {
				t.Fatalf("last call %q, want kill %v", rec.calls[len(rec.calls)-1], tc.wantKill)
			}
		})
	}
}

func TestAddWindowValidates(t *testing.T) {
	cases := []struct {
		name    string
		session string
		mod     func(*WindowSpec)
		want    error
	}{
		{name: "bad session", session: "a.b", mod: func(*WindowSpec) {}, want: session.ErrInvalidName},
		{name: "bad window", session: "api", mod: func(w *WindowSpec) { w.Dir = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{}
			w := duoSpec(t).Window
			tc.mod(&w)
			_, err := New(Options{Executor: rec}).AddWindow(context.Background(), tc.session, w, false)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("invalid window reached tmux: %q", rec.calls)
			}
		})
	}
}

func TestCleanupSurvivesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &scripted{results: []Result{ok(created), fail("", "boom"), ok("")}}
	exec := ExecutorFunc(func(c context.Context, bin string, args []string) (Result, error) {
		if args[0] == "split-window" {
			cancel()
		}
		return rec.Exec(c, bin, args)
	})
	if _, err := New(Options{Executor: exec}).CreateWorkspace(ctx, duoSpec(t)); err == nil {
		t.Fatal("expected error")
	}
	last := len(rec.calls) - 1
	if rec.calls[last][0] != "kill-session" || rec.done[last] {
		t.Fatalf("cleanup %q ran with a done context: %v", rec.calls[last], rec.done[last])
	}
}

func TestParseCreated(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", created, true},
		{"missing field", "$1\x1f@2\n", false},
		{"bad session id", "1\x1f@2\x1f%3", false},
		{"bad window id", "$1\x1f2\x1f%3", false},
		{"bad pane id", "$1\x1f@2\x1f%x", false},
		{"bare percent", "$1\x1f@2\x1f%", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := parseCreated(tc.in); got != tc.ok {
				t.Fatalf("parseCreated(%q) ok = %v, want %v", tc.in, got, tc.ok)
			}
		})
	}
}
