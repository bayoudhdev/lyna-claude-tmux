package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// withFakeClaude makes claude resolve to the fake, recording into a file.
func (e *cliEnv) withFakeClaude(t *testing.T) (record string) {
	t.Helper()
	fake := fakeclaude.Build(t)
	record = filepath.Join(e.host.Home, "claude.jsonl")
	e.setenv(fakeclaude.EnvRecord, record)
	e.setenv("CLAUDE_CONFIG_DIR", filepath.Join(e.host.Home, "claude-home"))
	e.host.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return fake, nil
		}
		return "", os.ErrNotExist
	}
	return record
}

func (e *cliEnv) setenv(k, v string) {
	e.env[k] = v
	e.host.Environ = e.host.Environ[:0:0]
	for key, val := range e.env {
		e.host.Environ = append(e.host.Environ, key+"="+val)
	}
}

func TestCreateCLI(t *testing.T) {
	e := newCLIEnv(t)
	record := e.withFakeClaude(t)
	projects := filepath.Join(e.host.Home, "src")
	for _, dir := range []string{"api/.git", "api/pkg", "web", "tools"} {
		if err := os.MkdirAll(filepath.Join(projects, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	api := filepath.Join(projects, "api")
	invocations := func(t *testing.T, n int) []fakeclaude.Invocation {
		t.Helper()
		var got []fakeclaude.Invocation
		tmuxtest.WaitFor(t, "claude started", func() bool {
			if _, err := os.Stat(record); err != nil {
				return false
			}
			got = got[:0]
			for _, r := range fakeclaude.ReadRecords(t, record) {
				if r.Kind == "invocation" {
					got = append(got, r)
				}
			}
			return len(got) >= n
		})
		return got
	}
	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
		check    func(t *testing.T, stdout string)
	}{
		{name: "two directories", args: []string{"create", "a", "b"}, wantCode: 1, errHas: []string{"at most one directory"}},
		{
			name: "not a terminal needs detach", args: []string{"create", api}, wantCode: 1, errHas: []string{"pass --detach"},
			setup: func(t *testing.T) { t.Helper(); e.term.Interactive = false },
			check: func(t *testing.T, _ string) {
				t.Helper()
				e.term.Interactive = true
				if _, err := os.Stat(record); err == nil {
					t.Fatal("claude started without a terminal to attach")
				}
			},
		},
		{name: "invalid sandbox", args: []string{"create", "-d", api, "--sandbox", "loose"}, wantCode: 1, errHas: []string{"loose"}},
		{
			name: "detached from the working directory", args: []string{"create", "--detach"},
			setup:  func(t *testing.T) { t.Helper(); e.cwd = filepath.Join(api, "pkg") },
			outHas: []string{"Workspace api is running. Attach with: lyna-tmux attach api"},
			check: func(t *testing.T, _ string) {
				t.Helper()
				if inv := invocations(t, 1)[0]; inv.Cwd != api || !slices.Contains(inv.Args, "--name=api") {
					t.Fatalf("launch %+v", inv)
				}
				if len(e.execs) != 0 {
					t.Fatalf("attached while detached: %q", e.execs)
				}
			},
		},
		{
			name: "running project attaches", args: []string{"new", "pkg"},
			setup:  func(t *testing.T) { t.Helper(); e.cwd = api },
			outHas: []string{"Workspace api is already open for " + api},
			check: func(t *testing.T, _ string) {
				t.Helper()
				if len(e.execs) != 1 || e.execs[0][len(e.execs[0])-1] != "=api:" || !slices.Contains(e.execs[0], "attach-session") {
					t.Fatalf("exec %q", e.execs)
				}
			},
		},
		{
			name: "flags and claude arguments", args: []string{"create", "-d", "-n", "web-ui", "-l", "solo", "--model", "opus", "-c", "~/src/web", "--", "--verbose", "fix the tests"},
			outHas: []string{"Workspace web-ui is running"},
			check: func(t *testing.T, _ string) {
				t.Helper()
				inv := invocations(t, 2)[1]
				n := len(inv.Args)
				if inv.Cwd != filepath.Join(projects, "web") || inv.Args[n-3] != "--continue" || inv.Args[n-2] != "--verbose" || inv.Args[n-1] != "fix the tests" || !slices.Contains(inv.Args, "--model=opus") {
					t.Fatalf("launch cwd %q args %q", inv.Cwd, inv.Args)
				}
			},
		},
		{name: "ls shows both", args: []string{"ls"}, outHas: []string{"api", "web-ui", "solo", "duo"}},
		{name: "name taken by another project", args: []string{"create", "-d", "-n", "api", filepath.Join(projects, "tools")}, wantCode: 1, errHas: []string{"workspace name is taken", "--name"}},
		{
			name: "layout completion includes custom layouts", args: []string{"__complete", "create", "--layout", ""},
			setup: func(t *testing.T) {
				t.Helper()
				dir := filepath.Join(e.host.Home, "config")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				conf := "[layouts.pair]\npanes = [{ role = \"claude\" }, { role = \"shell\", split = \"right\" }]\n"
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(conf), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			outHas: []string{"solo\n", "review\n", "auto\n", "pair\n", ":4"},
		},
		{name: "sandbox completion", args: []string{"__complete", "create", "--sandbox", ""}, outHas: []string{"standard\n", "strict\n", "off\n"}},
		{
			// The terminal of this process is 160 by 48 cells, which auto
			// resolves to duo; the size given wins and resolves to trio.
			name: "a given size resolves the auto layout", args: []string{"create", "-d", "-n", "wide", "-l", "auto", "--width", "200", "--height", "50", filepath.Join(projects, "tools")},
			outHas: []string{"Workspace wide is running"},
			check: func(t *testing.T, _ string) {
				t.Helper()
				code, stdout, stderr := e.run(t, "ls")
				if code != 0 || !strings.Contains(stdout, "wide") || !strings.Contains(stdout, "trio") {
					t.Fatalf("ls exit %d:\n%s%s", code, stdout, stderr)
				}
			},
		},
		{
			name: "the size flags stay out of the help", args: []string{"create", "--help"},
			outHas: []string{"--layout", "--detach"},
			check: func(t *testing.T, stdout string) {
				t.Helper()
				for _, flag := range []string{"--width", "--height"} {
					if strings.Contains(stdout, flag) {
						t.Fatalf("help lists %s:\n%s", flag, stdout)
					}
				}
			},
		},
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
