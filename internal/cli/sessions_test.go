package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// cliEnv runs commands against an isolated lyna-tmux home and server and
// records process replacement instead of performing it.
type cliEnv struct {
	host  app.Host
	env   map[string]string
	execs [][]string
	envs  [][]string
	cwd   string
	term  Terminal
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	bin := tmuxtest.Require(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	socket := tmuxtest.Socket(t, bin)
	env := map[string]string{
		"LYNA_TMUX_HOME": root, "LYNA_TMUX_SOCKET_NAME": socket, "PATH": os.Getenv("PATH"),
		"HOME": root, "TERM": "xterm-256color", "LANG": "en_US.UTF-8", "TMUX": "/tmp/outer,1,0",
	}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	e := &cliEnv{env: env, cwd: root, term: Terminal{Interactive: true, Width: 160, Height: 48}, host: app.Host{
		Getenv: func(k string) string { return env[k] }, Environ: environ, Home: root,
		Exe: filepath.Join(root, "lmux"), TmuxBin: bin,
	}}
	return e
}

func (e *cliEnv) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	d := Deps{
		Host: func() (app.Host, error) { return e.host, nil },
		Exec: func(path string, argv, env []string) error {
			e.execs = append(e.execs, append([]string{path}, argv...))
			e.envs = append(e.envs, env)
			return nil
		},
		LookPath: func(name string) (string, error) { return name, nil },
		Getwd:    func() (string, error) { return e.cwd, nil },
		Terminal: func() Terminal { return e.term },
		Now:      time.Now,
	}
	root := NewRootWith(Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, d)
	code = run(t.Context(), root, args)
	return code, out.String(), errOut.String()
}

// containsFolded matches an error message the way fang prints it: first
// letter capitalized, a trailing period, and lines padded then wrapped to the
// terminal. A message holding a path longer than the terminal is wrapped at
// the width rather than between words, which cuts whatever follows the path
// wherever the line ends, "--name" included. Every space therefore goes,
// rather than runs of them becoming one.
func containsFolded(s, sub string) bool {
	fold := func(x string) string { return strings.ToLower(strings.Join(strings.Fields(x), "")) }
	return strings.Contains(fold(s), fold(sub))
}

func TestContainsFolded(t *testing.T) {
	// The wrapped message is what a run on a temporary directory long enough
	// to fill the line printed; the word "--name" lost its second character to
	// the line end.
	wrapped := "       ERROR  \n" +
		"              Workspace name is taken: api is used by the workspace of\n" +
		"              /private/var/folders/20/jp1_0n3n7kndh6rnbqb5344m0000gn/T/TestCreateCLI63206629/001/src/api; pick another name with -\n" +
		"              -name.\n"
	cases := []struct {
		name string
		s    string
		sub  string
		want bool
	}{
		{name: "plain", s: "workspace name is taken", sub: "name is taken", want: true},
		{name: "capitalized and ended by fang", s: "Workspace name is taken.", sub: "workspace name is taken", want: true},
		{name: "padded and wrapped between words", s: "  unknown\n  command\n", sub: "unknown command", want: true},
		{name: "wrapped inside a word", s: wrapped, sub: "--name", want: true},
		{name: "the message itself", s: wrapped, sub: "workspace name is taken", want: true},
		{name: "another message", s: wrapped, sub: "profile must be one of", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsFolded(tc.s, tc.sub); got != tc.want {
				t.Fatalf("containsFolded(_, %q) = %v, want %v", tc.sub, got, tc.want)
			}
		})
	}
}

func (e *cliEnv) start(t *testing.T, name, project string) {
	t.Helper()
	ctx := tmuxtest.Context(t)
	s, err := app.OpenServer(ctx, e.host)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := layout.Builtin(layout.Duo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sleep := tmux.PaneProcess{Argv: []string{"/bin/sh", "-c", "exec sleep 3600"}}
	if _, err := s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: name, Project: project, Sandbox: "standard",
		Window: tmux.WindowSpec{Dir: e.host.Home, Plan: plan, Procs: []tmux.PaneProcess{sleep, sleep}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCommandsCLI(t *testing.T) {
	e := newCLIEnv(t)
	type step struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
		check    func(t *testing.T, stdout string)
	}
	jsonNames := func(want ...string) func(t *testing.T, stdout string) {
		return func(t *testing.T, stdout string) {
			t.Helper()
			var entries []lsEntry
			if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, stdout)
			}
			names := make([]string, 0, len(entries))
			for _, x := range entries {
				names = append(names, x.Name)
				if x.Windows != 1 || x.Layout != "duo" || x.Sandbox != "standard" || x.Created.IsZero() {
					t.Fatalf("entry %+v", x)
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, want) {
				t.Fatalf("names %q, want %q", names, want)
			}
		}
	}
	steps := []step{
		{name: "ls without workspaces", args: []string{"ls"}, outHas: []string{"No workspaces. Start one with: lmux create"}},
		{name: "ls json without workspaces", args: []string{"ls", "--json"}, check: func(t *testing.T, stdout string) {
			t.Helper()
			if strings.TrimSpace(stdout) != "[]" {
				t.Fatalf("stdout %q, want []", stdout)
			}
		}},
		{name: "attach without workspaces", args: []string{"attach"}, wantCode: 1, errHas: []string{"no such workspace running"}},
		{
			name:   "ls lists workspaces with sanitized project",
			setup:  func(t *testing.T) { t.Helper(); e.start(t, "api", "/src/api\x1b]0;evil\x07"); e.start(t, "web", "") },
			args:   []string{"ls"},
			outHas: []string{"NAME", "PROJECT", "api", "/src/api", "web"},
			check: func(t *testing.T, stdout string) {
				t.Helper()
				if strings.ContainsRune(stdout, 0x1b) || strings.ContainsRune(stdout, 0x07) {
					t.Fatalf("terminal controls in ls output: %q", stdout)
				}
			},
		},
		{name: "ls json", args: []string{"ls", "--json"}, check: jsonNames("api", "web")},
		{name: "list alias", args: []string{"list"}, outHas: []string{"api", "web"}},
		{name: "attach needs a name with several workspaces", args: []string{"attach"}, wantCode: 1, errHas: []string{"several workspaces are running", "api", "web"}},
		{name: "attach missing workspace", args: []string{"a", "nope"}, wantCode: 1, errHas: []string{"no such workspace: nope"}},
		{name: "attach invalid name", args: []string{"attach", "a;b"}, wantCode: 1, errHas: []string{"invalid"}},
		{
			name: "attach replaces the process with tmux",
			args: []string{"attach", "api"},
			check: func(t *testing.T, _ string) {
				t.Helper()
				if len(e.execs) != 1 {
					t.Fatalf("exec calls %q", e.execs)
				}
				argv := e.execs[0]
				if argv[0] != e.host.TmuxBin || !slices.Contains(argv, "attach-session") || argv[len(argv)-1] != "=api:" {
					t.Fatalf("exec argv %q", argv)
				}
				for _, kv := range e.envs[0] {
					if strings.HasPrefix(kv, "TMUX=") {
						t.Fatalf("attach environment has %q", kv)
					}
				}
			},
		},
		{name: "kill needs a target", args: []string{"kill"}, wantCode: 1, errHas: []string{"pass a workspace name or --all"}},
		{name: "kill rejects name with --all", args: []string{"kill", "api", "--all"}, wantCode: 1, errHas: []string{"pass a workspace name or --all"}},
		{name: "rename needs two names", args: []string{"rename", "api"}, wantCode: 1, errHas: []string{"accepts 2 arg"}},
		{name: "rename", args: []string{"rename", "api", "backend"}, outHas: []string{"Renamed workspace api to backend"}},
		{name: "rename onto existing", args: []string{"rename", "backend", "web"}, wantCode: 1, errHas: []string{"already exists"}},
		{name: "kill", args: []string{"kill", "backend"}, outHas: []string{"Ended workspace backend"}},
		{name: "kill missing", args: []string{"kill", "backend"}, wantCode: 1, errHas: []string{"no such workspace: backend"}},
		{name: "attach the only workspace", args: []string{"attach"}, check: func(t *testing.T, _ string) {
			t.Helper()
			if last := e.execs[len(e.execs)-1]; last[len(last)-1] != "=web:" {
				t.Fatalf("attached %q", last)
			}
		}},
		{name: "kill all", args: []string{"kill", "--all"}, outHas: []string{"Ended every workspace"}},
		{name: "ls after kill all", args: []string{"ls", "--json"}, check: jsonNames()},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := e.run(t, st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
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
				st.check(t, stdout)
			}
		})
	}
}

// TestAttachNeedsATerminal pins the guard on attach: tmux replaces this
// process, so a run without a terminal has to be refused here, before the
// handover turns into tmux's "open terminal failed: not a terminal".
func TestAttachNeedsATerminal(t *testing.T) {
	e := newCLIEnv(t)
	e.start(t, "api", "/src/api")
	const refusal = "this is not a terminal to attach"
	cases := []struct {
		name        string
		args        []string
		interactive bool
		wantCode    int
		errHas      string
		wantExecs   int
	}{
		{name: "a named workspace", args: []string{"attach", "api"}, wantCode: 1, errHas: refusal},
		{name: "the only workspace", args: []string{"attach"}, wantCode: 1, errHas: refusal},
		{name: "the alias", args: []string{"a", "api"}, wantCode: 1, errHas: refusal},
		{name: "a missing workspace is refused for the terminal first", args: []string{"attach", "nope"}, wantCode: 1, errHas: refusal},
		{name: "listing needs no terminal", args: []string{"ls"}},
		{name: "a terminal attaches", args: []string{"attach", "api"}, interactive: true, wantExecs: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.term.Interactive = tc.interactive
			before := len(e.execs)
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			if tc.errHas != "" && !containsFolded(stderr, tc.errHas) {
				t.Fatalf("stderr %q, want %q", stderr, tc.errHas)
			}
			if got := len(e.execs) - before; got != tc.wantExecs {
				t.Fatalf("%d process replacements, want %d: %q", got, tc.wantExecs, e.execs[before:])
			}
		})
	}
}

func TestOpenServerFailureReported(t *testing.T) {
	e := newCLIEnv(t)
	e.host.TmuxBin = filepath.Join(e.host.Home, "missing-tmux")
	for _, args := range [][]string{{"ls"}, {"attach", "x"}, {"kill", "x"}, {"rename", "a", "b"}} {
		t.Run(args[0], func(t *testing.T) {
			code, _, stderr := e.run(t, args...)
			if code != 1 || !containsFolded(stderr, "not installed") {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
		})
	}
	hostErr := errors.New("no executable path")
	var out, errOut bytes.Buffer
	root := NewRootWith(Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, Deps{Host: func() (app.Host, error) { return app.Host{}, hostErr }})
	if code := run(t.Context(), root, []string{"ls"}); code != 1 || !containsFolded(errOut.String(), "no executable path") {
		t.Fatalf("host error: exit %d, stderr %q", code, errOut.String())
	}
}
