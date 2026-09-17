package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// infraEnv adds to the shared CLI environment what the infra commands need:
// a PATH the test controls, recorded child processes, scripted standard input
// and a machine for doctor that runs no real program.
type infraEnv struct {
	*cliEnv
	// bins maps a program name to its path; anything else is not on PATH.
	bins map[string]string
	// stdin is what a confirmation or a popup key press reads.
	stdin *strings.Reader
	// runs records the argument vectors of attached child processes.
	runs [][]string
	// runErr is returned by the recorded runner.
	runErr error
	// runOut is what the recorded child process writes on its standard output.
	runOut string
	// sys is what doctor and the readiness checks see.
	sys  doctor.Deps
	outs map[string]string
	mu   sync.Mutex
	ran  []string
}

func newInfraEnv(t *testing.T) *infraEnv {
	t.Helper()
	e := &infraEnv{
		cliEnv: newCLIEnv(t),
		bins:   map[string]string{"tmux": tmuxtest.Require(t)},
		stdin:  strings.NewReader(""),
		outs:   map[string]string{},
	}
	e.host.LookPath = e.lookPath
	e.sys = doctor.Deps{
		GOOS:     "darwin",
		ReadFile: func(string, int64) ([]byte, error) { return nil, os.ErrNotExist },
		Exists:   func(string) bool { return false },
		Run: func(ctx context.Context, argv []string) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("command run without a deadline")
			}
			line := strings.Join(argv, " ")
			e.mu.Lock()
			e.ran = append(e.ran, line)
			e.mu.Unlock()
			if out, ok := e.outs[line]; ok {
				return out, nil
			}
			return "", fmt.Errorf("unexpected command %q", line)
		},
	}
	return e
}

func (e *infraEnv) lookPath(name string) (string, error) {
	if p, ok := e.bins[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("exec: %q: %w", name, os.ErrNotExist)
}

// bin installs a shell script on the test PATH and returns its path.
func (e *infraEnv) bin(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(e.host.Home, "bin", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	e.bins[name] = path
	return path
}

// run executes the command line with the test dependencies, replacing the
// machine doctor inspects for the duration of the call.
func (e *infraEnv) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	saved := doctorSystem
	doctorSystem = func() doctor.Deps { return e.sys }
	defer func() { doctorSystem = saved }()
	var out, errOut bytes.Buffer
	d := Deps{
		Host: func() (app.Host, error) { return e.host, nil },
		Exec: func(path string, argv, env []string) error {
			e.execs = append(e.execs, append([]string{path}, argv...))
			e.envs = append(e.envs, env)
			return nil
		},
		LookPath: e.lookPath,
		Run: func(_ context.Context, argv []string, s Streams) error {
			e.runs = append(e.runs, argv)
			if e.runOut != "" {
				if _, err := io.WriteString(s.Out, e.runOut); err != nil {
					return err
				}
			}
			return e.runErr
		},
		Getwd:    func() (string, error) { return e.cwd, nil },
		Terminal: func() Terminal { return e.term },
		Now:      time.Now,
	}
	root := NewRootWith(Streams{In: e.stdin, Out: &out, Err: &errOut}, d)
	code = run(t.Context(), root, args)
	return code, out.String(), errOut.String()
}

// infraProject creates a repository below the test home and returns its root.
func infraProject(t *testing.T, e *infraEnv, name string) string {
	t.Helper()
	root := filepath.Join(e.host.Home, "src", name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// infraFakeDocker installs a docker that records every invocation and reports
// the container state from a file the test writes.
func infraFakeDocker(t *testing.T, e *infraEnv) (log, state string) {
	t.Helper()
	log = filepath.Join(e.host.Home, "docker.log")
	state = filepath.Join(e.host.Home, "docker.state")
	// A container that is created or started reports itself as running
	// afterwards, as the real docker does, so a test can follow a command that
	// starts the container with one that needs it running.
	e.bin(t, "docker", "for a in \"$@\"; do printf '%s\\n' \"$a\" >> '"+log+"'; done\n"+
		"printf -- '--\\n' >> '"+log+"'\n"+
		"case \"$1 $2\" in 'container ls') cat '"+state+"' 2>/dev/null ;; esac\n"+
		"case \"$1\" in run|start) printf 'running\\n' > '"+state+"' ;; esac\nexit 0\n")
	return log, state
}

// infraDockerRuns returns the recorded docker invocations.
func infraDockerRuns(t *testing.T, log string) [][]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	var current []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "--" {
			out = append(out, current)
			current = nil
			continue
		}
		current = append(current, line)
	}
	return out
}

func TestCreateIsolatedCLI(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		noDocker bool
		term     bool
		// files renders the project's dev container, which a build needs.
		files bool
		// stdin answers the question a stopped container raises.
		stdin string
		args  []string
		// inner is what the create inside the container prints.
		inner    string
		wantCode int
		// wantArgv is the argument vector of the last attached command, the
		// one that opens the workspace, and wantAttached how many attached
		// commands ran in all (one unless the container was built here).
		wantArgv     []string
		wantAttached int
		// wantDocker lists the captured docker commands by verb; a single
		// container state query when empty.
		wantDocker []string
		outHas     []string
		outLacks   []string
		errHas     []string
	}{
		{
			name: "attached create in the running container", state: "running\n", term: true,
			args: []string{"create", "--isolation", "container", "--model", "opus", "--", "--verbose"},
			wantArgv: []string{
				"exec", "--interactive", "--tty", "--user", "lyna", "--workdir", "/workspace",
				"--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api",
				"lmux", "create", "--model=opus", "--sandbox=standard", "--width=120", "--height=40",
				"--isolation=container", "/workspace", "--", "--verbose",
			},
			inner: "the attached workspace writes on this terminal\n",
			// An attached create hands the terminal over: what the workspace
			// draws is the user's screen, not a message this host replaces.
			outHas: []string{"the attached workspace writes on this terminal"},
		},
		{
			name: "detached create has no terminal flags", state: "running\n",
			args: []string{"create", "--isolation", "container", "--detach"},
			wantArgv: []string{
				"exec", "--user", "lyna", "--workdir", "/workspace", "--env", "TERM", "--env", "COLORTERM", "--env", "LANG",
				"lyna-tmux-api", "lmux", "create", "--sandbox=standard", "--width=120", "--height=40",
				"--isolation=container", "--detach", "/workspace",
			},
			inner: "Workspace api is running. Attach with: lmux attach api\n",
			outHas: []string{
				"runs in the dev container lyna-tmux-api",
				"Attach with: lmux create --isolation container",
			},
			// The command the container prints names its own tmux server,
			// which this host has no workspace on.
			outLacks: []string{"lmux attach api"},
		},
		{
			name: "detached team says how to reopen the team", state: "running\n",
			args: []string{"team", "--isolation", "container", "--detach"},
			wantArgv: []string{
				"exec", "--user", "lyna", "--workdir", "/workspace", "--env", "TERM", "--env", "COLORTERM", "--env", "LANG",
				"lyna-tmux-api", "lmux", "team", "--sandbox=standard", "--width=120", "--height=40",
				"--isolation=container", "--detach", "/workspace",
			},
			outHas: []string{"Attach with: lmux team --isolation container"},
		},
		{
			name: "the container is not running and the question is declined", state: "exited\n", term: true,
			args: []string{"create", "--isolation", "container"}, wantCode: 1, stdin: "n\n",
			outHas: []string{"Build and start it now?"},
			errHas: []string{"lyna-tmux-api", "lmux sandbox devcontainer up"},
		},
		{
			name: "the container was never created", term: true,
			args: []string{"create", "--isolation", "container"}, wantCode: 1,
			errHas: []string{"not running"},
		},
		{
			name: "a stopped container is built and started on a yes", state: "exited\n", term: true, files: true, stdin: "y\n",
			args: []string{"create", "--isolation", "container"},
			wantArgv: []string{
				"exec", "--interactive", "--tty", "--user", "lyna", "--workdir", "/workspace",
				"--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api",
				"lmux", "create", "--sandbox=standard", "--width=120", "--height=40",
				"--isolation=container", "/workspace",
			},
			// The image build and the firewall run attached, then the workspace.
			wantAttached: 3,
			wantDocker:   []string{"container", "container", "start", "container"},
			outHas:       []string{"Build and start it now?"},
			errHas:       []string{"Starting the dev container lyna-tmux-api"},
		},
		{
			name: "start-container builds without a terminal to ask on", state: "exited\n", files: true,
			args: []string{"create", "--isolation", "container", "--detach", "--start-container"},
			wantArgv: []string{
				"exec", "--user", "lyna", "--workdir", "/workspace", "--env", "TERM", "--env", "COLORTERM", "--env", "LANG",
				"lyna-tmux-api", "lmux", "create", "--sandbox=standard", "--width=120", "--height=40",
				"--isolation=container", "--detach", "/workspace",
			},
			wantAttached: 3,
			wantDocker:   []string{"container", "container", "start", "container"},
			outHas:       []string{"runs in the dev container lyna-tmux-api"},
		},
		{
			name: "without a terminal the flag is named", state: "exited\n", files: true,
			args: []string{"create", "--isolation", "container", "--detach"}, wantCode: 1,
			errHas: []string{"lmux sandbox devcontainer up", "--start-container"},
		},
		{
			name: "docker is not installed", noDocker: true, term: true,
			args: []string{"create", "--isolation", "container"}, wantCode: 1,
			errHas: []string{"docker is not on PATH", "lmux doctor"},
		},
		{
			name: "bash isolation is created on this host", term: true,
			args: []string{"create", "--isolation", "bash", "--detach"}, wantCode: 1,
			errHas: []string{"claude"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			e.term = Terminal{Interactive: tc.term, Width: 120, Height: 40}
			log, state := infraFakeDocker(t, e)
			if tc.noDocker {
				delete(e.bins, "docker")
			}
			if tc.state != "" {
				if err := os.WriteFile(state, []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			e.cwd = infraProject(t, e, "api")
			if tc.files {
				if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init", "--version", "v1.4.0"); code != 0 {
					t.Fatalf("init: %s", stderr)
				}
			}
			if tc.stdin != "" {
				e.stdin = strings.NewReader(tc.stdin)
			}
			e.runOut = tc.inner
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s", code, tc.wantCode, stderr)
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.outLacks {
				if strings.Contains(stdout, unwanted) {
					t.Fatalf("stdout still carries %q:\n%s", unwanted, stdout)
				}
			}
			if tc.wantArgv == nil {
				if len(e.runs) != 0 {
					t.Fatalf("ran %q", e.runs)
				}
				return
			}
			attached := max(tc.wantAttached, 1)
			if len(e.runs) != attached {
				t.Fatalf("attached commands %q, want %d", e.runs, attached)
			}
			// The workspace is opened by the last attached command: a build
			// and the firewall come first when the container was started here.
			got := e.runs[len(e.runs)-1]
			if got[0] != e.bins["docker"] || !slices.Equal(got[1:], tc.wantArgv) {
				t.Fatalf("argv %q\nwant %q", got, tc.wantArgv)
			}
			wantDocker := tc.wantDocker
			if wantDocker == nil {
				wantDocker = []string{"container"}
			}
			var verbs []string
			for _, argv := range infraDockerRuns(t, log) {
				verbs = append(verbs, argv[0])
			}
			if !slices.Equal(verbs, wantDocker) {
				t.Fatalf("captured docker commands %q, want %q", verbs, wantDocker)
			}
		})
	}
}

func TestInfraDirAndConfirm(t *testing.T) {
	e := newInfraEnv(t)
	e.cwd = e.host.Home
	d := Deps{Getwd: func() (string, error) { return e.cwd, nil }, Terminal: func() Terminal { return e.term }}
	t.Run("dir", func(t *testing.T) {
		cases := []struct{ arg, want string }{
			{"", e.host.Home},
			{"src/api", filepath.Join(e.host.Home, "src", "api")},
			{"~", e.host.Home},
			{"~/src", filepath.Join(e.host.Home, "src")},
			{"/tmp/./x", "/tmp/x"},
		}
		for _, tc := range cases {
			got, err := d.infraDir(e.host, tc.arg)
			if err != nil || got != tc.want {
				t.Fatalf("infraDir(%q) = %q, %v, want %q", tc.arg, got, err, tc.want)
			}
		}
	})
	t.Run("confirm", func(t *testing.T) {
		cases := []struct {
			in       string
			terminal bool
			want     bool
			wantErr  bool
		}{
			{in: "y\n", terminal: true, want: true},
			{in: "YES\n", terminal: true, want: true},
			{in: "n\n", terminal: true},
			{in: "\n", terminal: true},
			{in: "", terminal: true},
			{in: "y\n", wantErr: true},
		}
		for _, tc := range cases {
			e.term = Terminal{Interactive: tc.terminal}
			cmd := NewRootWith(Streams{In: strings.NewReader(tc.in), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, d)
			got, err := d.infraConfirm(cmd, "Remove them?", "pass --yes")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("confirm(%q, terminal %v) = %v, %v", tc.in, tc.terminal, got, err)
			}
		}
	})
}
