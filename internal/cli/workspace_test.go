package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// wsEnv is a cliEnv whose claude is the fake and whose lyna-tmux binary is a
// script that records "cwd|args" and keeps its pane alive.
type wsEnv struct {
	*cliEnv
	record  string
	exeLog  string
	project string
	tmux    *tmux.Client
}

func newWsEnv(t *testing.T) *wsEnv {
	t.Helper()
	e := newCLIEnv(t)
	w := &wsEnv{
		cliEnv:  e,
		record:  e.withFakeClaude(t),
		exeLog:  filepath.Join(e.host.Home, "exe.log"),
		project: filepath.Join(e.host.Home, "src", "api"),
		tmux:    tmux.New(tmux.Options{Bin: e.host.TmuxBin, Socket: tmux.Socket{Name: e.env["LYNA_TMUX_SOCKET_NAME"]}}),
	}
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> " + tmux.ShellQuote(w.exeLog) + "\nexec sleep 3600\n"
	if err := os.WriteFile(e.host.Exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return w
}

// invocations waits for at least n recorded claude starts.
func (w *wsEnv) invocations(t *testing.T, n int) []fakeclaude.Invocation {
	t.Helper()
	var got []fakeclaude.Invocation
	tmuxtest.WaitFor(t, "claude start", func() bool {
		if _, err := os.Stat(w.record); err != nil {
			return false
		}
		got = got[:0]
		for _, r := range fakeclaude.ReadRecords(t, w.record) {
			if r.Kind == "invocation" {
				got = append(got, r)
			}
		}
		return len(got) >= n
	})
	return got
}

// waitExe waits for the recording script to log line.
func (w *wsEnv) waitExe(t *testing.T, line string) {
	t.Helper()
	tmuxtest.WaitFor(t, "lyna-tmux call "+line, func() bool {
		data, err := os.ReadFile(w.exeLog)
		return err == nil && slices.Contains(strings.Split(string(data), "\n"), line)
	})
}

// inside makes commands run as if started in pane of the workspace server.
func (w *wsEnv) inside(pane string) {
	w.setenv("TMUX", app.SocketPath(w.host.Getenv, w.env["LYNA_TMUX_SOCKET_NAME"])+",1,0")
	w.setenv("TMUX_PANE", pane)
}

// outside makes commands run from a pane of another tmux server.
func (w *wsEnv) outside() {
	w.setenv("TMUX", "/tmp/outer,1,0")
	w.setenv("TMUX_PANE", "%0")
}

type wsPane struct {
	id, role, settings, path string
	left, top                int
	active                   bool
}

func (w *wsEnv) panes(t *testing.T, target string) []wsPane {
	t.Helper()
	out, err := w.tmux.Run(tmuxtest.Context(t), "list-panes", "-t", target, "-F",
		"#{pane_id}|#{@lt_role}|#{@lt_settings}|#{pane_left}|#{pane_top}|#{pane_active}|#{pane_current_path}")
	if err != nil {
		t.Fatal(err)
	}
	var panes []wsPane
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		left, _ := strconv.Atoi(f[3])
		top, _ := strconv.Atoi(f[4])
		panes = append(panes, wsPane{id: f[0], role: f[1], settings: f[2], left: left, top: top, active: f[5] == "1", path: f[6]})
	}
	return panes
}

// activeWindow returns the panes of the session's active window, which must
// be named name.
func (w *wsEnv) activeWindow(t *testing.T, session, name string) []wsPane {
	t.Helper()
	got, err := w.tmux.Display(tmuxtest.Context(t), tmux.ExactSession(session), "#{window_id}|#{window_name}")
	if err != nil {
		t.Fatal(err)
	}
	id, gotName, _ := strings.Cut(got, "|")
	if gotName != name {
		t.Fatalf("active window of %s is %q, want %q", session, gotName, name)
	}
	return w.panes(t, id)
}

func (w *wsEnv) roles(panes []wsPane) []string {
	out := make([]string, len(panes))
	for i, p := range panes {
		out[i] = p.role
	}
	return out
}

// checkClaude checks a recorded launch: started in the project with the
// workspace's per-launch settings file and environment, and the pane that
// runs it references that file.
func (w *wsEnv) checkClaude(t *testing.T, inv fakeclaude.Invocation, pane wsPane, lastArg string) {
	t.Helper()
	socket := app.SocketPath(w.host.Getenv, w.env["LYNA_TMUX_SOCKET_NAME"])
	if inv.Cwd != w.project || !slices.Contains(inv.Args, "--name=api") || inv.Args[len(inv.Args)-1] != lastArg {
		t.Fatalf("claude cwd %q args %q, want last argument %q", inv.Cwd, inv.Args, lastArg)
	}
	if inv.Env["LYNA_TMUX_MANAGED"] != "1" || inv.Env["LYNA_TMUX_SESSION"] != "api" || inv.Env["LYNA_TMUX_SOCKET"] != socket {
		t.Fatalf("claude environment %v", inv.Env)
	}
	if inv.SettingsPath == "" || len(inv.Settings) == 0 || pane.settings != inv.SettingsPath || pane.role != "claude" {
		t.Fatalf("settings %q (%s), pane %+v", inv.SettingsPath, inv.SettingsError, pane)
	}
}

func TestWorkspaceWindowCommandsCLI(t *testing.T) {
	w := newWsEnv(t)
	if code, _, stderr := w.run(t, "create", "-d", "-l", "duo", w.project); code != 0 {
		t.Fatalf("create: %s", stderr)
	}
	w.invocations(t, 1)
	first := w.panes(t, "=api:")
	if got := w.roles(first); !slices.Equal(got, []string{"claude", "shell"}) {
		t.Fatalf("workspace roles %q", got)
	}
	claudePane, shellPane := first[0], first[1]
	// A pane of the workspace whose directory is not the project root.
	sub := filepath.Join(w.project, "src")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := w.tmux.Run(tmuxtest.Context(t), "new-window", "-d", "-t", tmux.ExactSession("api"), "-n", "sub", "-c", sub,
		"-P", "-F", "#{pane_id}", "--", "/bin/sh", "-c", "exec sleep 3600")
	if err != nil {
		t.Fatal(err)
	}
	subPane := strings.TrimSpace(out)

	active := func(t *testing.T, panes []wsPane) wsPane {
		t.Helper()
		for _, p := range panes {
			if p.active {
				return p
			}
		}
		t.Fatalf("no active pane in %+v", panes)
		return wsPane{}
	}

	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		silent   bool
		outHas   []string
		errHas   []string
		check    func(t *testing.T)
	}{
		// task
		{name: "task outside a workspace names --session", setup: func(*testing.T) { w.outside() }, args: []string{"task", "fix", "fix it now"}, wantCode: 1, errHas: []string{"not running in a lyna-tmux workspace pane", "pass --session <name>"}},
		{name: "task needs a name", args: []string{"task"}, wantCode: 1, errHas: []string{"accepts between 1 and 2 arg"}},
		{name: "task invalid worktree", args: []string{"task", "a/b", "--session", "api"}, wantCode: 1, errHas: []string{"worktree name", "a/b"}},
		{name: "task hidden worktree", args: []string{"task", ".x", "--session", "api"}, wantCode: 1, errHas: []string{"must not start with '-' or '.'"}},
		{name: "task one-word prompt", args: []string{"task", "fix", "update", "--session", "api"}, wantCode: 1, errHas: []string{"one word"}},
		{name: "task option prompt", args: []string{"task", "fix", "--session", "api", "--", "-v now"}, wantCode: 1, errHas: []string{"must not start with '-'"}},
		{name: "task missing workspace", args: []string{"task", "fix", "--session", "nope"}, wantCode: 1, errHas: []string{"no such workspace: nope"}},
		{
			name: "task with a prompt in a named workspace", args: []string{"task", "fix-login", "fix the login loop", "-s", "api"},
			outHas: []string{"Task fix-login is running in workspace api"},
			check: func(t *testing.T) {
				t.Helper()
				panes := w.activeWindow(t, "api", "fix-login")
				inv := w.invocations(t, 2)[1]
				w.checkClaude(t, inv, panes[0], "fix the login loop")
				if !slices.Contains(inv.Args, "--worktree=fix-login") {
					t.Fatalf("task launch args %q", inv.Args)
				}
			},
		},
		{
			name: "task from a workspace pane", setup: func(*testing.T) { w.inside(shellPane.id) }, args: []string{"task", "spike"},
			outHas: []string{"Task spike is running in workspace api"},
			check: func(t *testing.T) {
				t.Helper()
				panes := w.activeWindow(t, "api", "spike")
				w.checkClaude(t, w.invocations(t, 3)[2], panes[0], "--worktree=spike")
			},
		},
		{
			name: "task of an open worktree selects its window", args: []string{"task", "spike", "keep going now"},
			outHas: []string{"Task spike is already open in workspace api"},
			check: func(t *testing.T) {
				t.Helper()
				if panes := w.activeWindow(t, "api", "spike"); len(panes) != 1 {
					t.Fatalf("a second window for the worktree was built: panes %+v", panes)
				}
			},
		},
		// resume
		{
			name: "resume from a workspace pane", args: []string{"resume"},
			outHas: []string{"Conversation picker is open in workspace api"},
			check: func(t *testing.T) {
				t.Helper()
				panes := w.activeWindow(t, "api", "resume")
				inv := w.invocations(t, 4)[3]
				w.checkClaude(t, inv, panes[0], "--resume")
				if slices.ContainsFunc(inv.Args, func(a string) bool { return strings.HasPrefix(a, "--worktree") }) {
					t.Fatalf("resume runs in a worktree: %q", inv.Args)
				}
			},
		},
		{name: "resume outside a workspace names --session", setup: func(*testing.T) { w.outside() }, args: []string{"resume"}, wantCode: 1, errHas: []string{"pass --session <name>"}},
		{name: "resume takes no arguments", args: []string{"resume", "api"}, wantCode: 1, errHas: []string{"unknown command"}},
		{
			name: "resume in a named workspace selects the open picker", args: []string{"resume", "--session", "api"},
			outHas: []string{"Conversation picker is already open in workspace api"},
			check: func(t *testing.T) {
				t.Helper()
				panes := w.activeWindow(t, "api", "resume")
				if len(panes) != 1 {
					t.Fatalf("a second picker window was built: panes %+v", panes)
				}
				w.checkClaude(t, w.invocations(t, 4)[3], panes[0], "--resume")
			},
		},
		// layout
		{name: "layout outside a workspace names --pane", args: []string{"layout", "trio"}, wantCode: 1, errHas: []string{"pass --pane <pane_id>"}},
		{name: "layout needs a name", args: []string{"layout"}, wantCode: 1, errHas: []string{"accepts 1 arg"}},
		{name: "layout pane name refused", args: []string{"layout", "trio", "--pane", "api"}, wantCode: 1, errHas: []string{"not a pane id"}},
		{name: "layout missing pane", args: []string{"layout", "trio", "--pane", "%99999"}, wantCode: 1, errHas: []string{"no pane %99999"}},
		{name: "layout unknown", args: []string{"layout", "nope", "--pane", claudePane.id}, wantCode: 1, errHas: []string{"unknown layout", "trio"}},
		{
			name: "layout from the window menu", setup: func(*testing.T) { w.inside(shellPane.id) }, args: []string{"layout", "trio", "--pane", claudePane.id},
			silent: true,
			check: func(t *testing.T) {
				t.Helper()
				panes := w.activeWindow(t, "api", "trio")
				if got := w.roles(panes); !slices.Equal(got, []string{"claude", "shell", "changes"}) {
					t.Fatalf("trio roles %q", got)
				}
				w.checkClaude(t, w.invocations(t, 5)[4], panes[0], "--name=api")
				w.waitExe(t, w.project+"|watch --session api --dir "+w.project)
				if len(w.panes(t, claudePane.id)) != 2 {
					t.Fatal("the window the menu opened on changed")
				}
			},
		},
		{
			name: "layout custom from the calling pane",
			setup: func(t *testing.T) {
				t.Helper()
				dir := filepath.Join(w.host.Home, "config")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				conf := "[layouts.pair]\npanes = [{ role = \"shell\" }, { role = \"claude\", split = \"down\" }]\n"
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(conf), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			args: []string{"layout", "pair"}, silent: true,
			check: func(t *testing.T) {
				t.Helper()
				panes := w.activeWindow(t, "api", "pair")
				if got := w.roles(panes); !slices.Equal(got, []string{"shell", "claude"}) || !panes[1].active {
					t.Fatalf("pair panes %+v", panes)
				}
				w.invocations(t, 6)
			},
		},
		// split
		{name: "split outside a workspace names --pane", setup: func(*testing.T) { w.outside() }, args: []string{"split"}, wantCode: 1, errHas: []string{"pass --pane <pane_id>"}},
		{name: "split direction", args: []string{"split", "left"}, wantCode: 1, errHas: []string{`invalid argument "left"`}},
		{name: "split too many arguments", args: []string{"split", "right", "down"}, wantCode: 1, errHas: []string{"accepts at most 1 arg"}},
		{name: "split role", args: []string{"split", "--role", "review", "--pane", shellPane.id}, wantCode: 1, errHas: []string{`unknown pane role "review"`, "shell, claude, changes"}},
		{
			name: "split a shell to the right of the calling pane", setup: func(*testing.T) { w.inside(shellPane.id) }, args: []string{"split"},
			silent: true,
			check: func(t *testing.T) {
				t.Helper()
				panes := w.panes(t, shellPane.id)
				added := panes[len(panes)-1]
				if len(panes) != 3 || added.role != "shell" || !added.active || added.left <= shellPane.left || added.top != shellPane.top {
					t.Fatalf("panes after split %+v", panes)
				}
			},
		},
		{
			name: "split Claude below a named pane", setup: func(*testing.T) { w.outside() }, args: []string{"split", "down", "--role", "claude", "--pane", claudePane.id},
			silent: true,
			check: func(t *testing.T) {
				t.Helper()
				panes := w.panes(t, claudePane.id)
				var added wsPane
				for _, p := range panes {
					if p.active {
						added = p
					}
				}
				if len(panes) != 4 || added.left != claudePane.left || added.top <= claudePane.top {
					t.Fatalf("panes after split %+v", panes)
				}
				w.checkClaude(t, w.invocations(t, 7)[6], added, "--name=api")
			},
		},
		{
			name: "split live changes", args: []string{"split", "right", "--role", "changes", "--pane", claudePane.id},
			silent: true,
			check: func(t *testing.T) {
				t.Helper()
				if got := w.roles(w.panes(t, claudePane.id)); !slices.Contains(got, "changes") || len(got) != 5 {
					t.Fatalf("roles after split %q", got)
				}
				tmuxtest.WaitFor(t, "second changes pane", func() bool {
					data, _ := os.ReadFile(w.exeLog)
					return strings.Count(string(data), "watch --session api --dir "+w.project+"\n") == 2
				})
			},
		},
		{
			name: "split a shell in the pane's own directory", args: []string{"split", "--pane", subPane},
			silent: true,
			check: func(t *testing.T) {
				t.Helper()
				if got := active(t, w.panes(t, subPane)); got.path != sub || got.role != "shell" {
					t.Fatalf("new shell pane %+v, want a shell in %s", got, sub)
				}
			},
		},
		{
			name: "split Claude in the project root", args: []string{"split", "down", "--role", "claude", "--pane", subPane},
			silent: true,
			check: func(t *testing.T) {
				t.Helper()
				got := active(t, w.panes(t, subPane))
				if got.path != w.project {
					t.Fatalf("Claude pane runs in %s, want the project root %s", got.path, w.project)
				}
				w.checkClaude(t, w.invocations(t, 8)[7], got, "--name=api")
			},
		},
		{name: "split missing pane", args: []string{"split", "--pane", "%99999"}, wantCode: 1, errHas: []string{"no pane %99999"}},
		// completion
		{name: "split direction completion", args: []string{"__complete", "split", ""}, outHas: []string{"right\n", "down\n"}},
		{name: "split role completion", args: []string{"__complete", "split", "--role", ""}, outHas: []string{"shell\n", "claude\n", "changes\n"}},
		{name: "layout name completion", args: []string{"__complete", "layout", ""}, outHas: []string{"trio\n", "pair\n"}},
		{name: "task session completion", args: []string{"__complete", "task", "x", "--session", ""}, outHas: []string{"api\n", ":4"}},
		{name: "resume session completion", args: []string{"__complete", "resume", "--session", ""}, outHas: []string{"api\n"}},
	}
	for _, st := range steps {
		if !t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := w.run(t, st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			if st.silent && stdout+stderr != "" {
				t.Fatalf("output from a command run-shell starts: stdout %q stderr %q", stdout, stderr)
			}
			for _, s := range st.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t)
			}
		}) {
			t.Fatalf("step %q failed; later steps depend on it", st.name)
		}
	}
}

// wsFileState is the content and modification time of a file.
func wsFileState(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(info.ModTime().UnixNano(), 10) + " " + string(data)
}

// TestSessionCompletionIsReadOnly checks that completing --session lists the
// workspaces without writing anything: opening the server regenerates the
// tmux configuration, which pressing TAB must never do.
func TestSessionCompletionIsReadOnly(t *testing.T) {
	w := newWsEnv(t)
	if code, _, stderr := w.run(t, "create", "-d", "-l", "solo", w.project); code != 0 {
		t.Fatalf("create: %s", stderr)
	}
	w.invocations(t, 1)
	conf := filepath.Join(w.host.Home, "state", "tmux.conf")
	// A theme change makes the generated configuration differ from the file
	// on disk, so every path that opens the server rewrites it.
	dir := filepath.Join(w.host.Home, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[ui]\ntheme = \"light\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		args        []string
		outHas      string
		wantRewrite bool
	}{
		{name: "task completion", args: []string{"__complete", "task", "x", "--session", ""}, outHas: "api\n"},
		{name: "resume completion", args: []string{"__complete", "resume", "--session", ""}, outHas: "api\n"},
		{name: "a command that opens the server does rewrite it", args: []string{"ls"}, outHas: "api", wantRewrite: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := wsFileState(t, conf)
			code, stdout, stderr := w.run(t, tc.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if !strings.Contains(stdout, tc.outHas) {
				t.Fatalf("stdout %q, want %q", stdout, tc.outHas)
			}
			if rewritten := wsFileState(t, conf) != before; rewritten != tc.wantRewrite {
				t.Fatalf("configuration rewritten %v, want %v", rewritten, tc.wantRewrite)
			}
		})
	}
}

// TestLayoutAutoUsesWindowSize checks that the auto layout resolves for the
// size of the window the command runs for, not for an unknown size.
func TestLayoutAutoUsesWindowSize(t *testing.T) {
	w := newWsEnv(t)
	w.term = Terminal{Interactive: true, Width: 240, Height: 60}
	if code, _, stderr := w.run(t, "create", "-d", "-l", "solo", w.project); code != 0 {
		t.Fatalf("create: %s", stderr)
	}
	w.invocations(t, 1)
	pane := w.panes(t, tmux.ExactSession("api"))[0]
	code, stdout, stderr := w.run(t, "layout", "auto", "--pane", pane.id)
	if code != 0 || stdout+stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if got := w.roles(w.activeWindow(t, "api", "trio")); !slices.Equal(got, []string{"claude", "shell", "changes"}) {
		t.Fatalf("auto layout in a 240x60 window opened %q", got)
	}
}
