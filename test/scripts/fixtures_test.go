package scripts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// fixtureClaude is the recording wrapper under docs/scenes/fixtures/bin.
func fixtureClaude(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the recording fixtures need a POSIX host")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	return filepath.Join(repoRoot(t), "docs", "scenes", "fixtures", "bin", "claude")
}

// realClaude is a stub of the binary the wrapper must hand every other call
// to; it reports the arguments it was given.
const realClaude = `#!/bin/sh
if [ "$1" = agents ] && [ "$2" = --json ]; then
  printf '[{"pid":4242,"cwd":"%s","kind":"interactive","startedAt":1,"name":"real"}]\n' "$PWD"
  exit 0
fi
echo "real claude $*"
`

func runFixtureClaude(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(fixtureClaude(t), args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		exit = ee.ExitCode()
	}
	return out.String(), errOut.String(), exit
}

// TestFixtureClaudeAgents covers the wrapper that keeps the recorder's own
// agents out of the documentation: it answers only the one call, only when the
// scene asks, and what it answers is a list lyna-tmux can read.
func TestFixtureClaudeAgents(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	project := filepath.Join(root, "acme-api")
	for _, dir := range []string{bin, project} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(bin, "claude"), realClaude)
	path := "PATH=" + filepath.Dir(fixtureClaude(t)) + ":" + bin + ":/usr/bin:/bin"

	cases := []struct {
		name     string
		env      []string
		args     []string
		want     []string
		unwanted []string
		records  int
	}{
		{
			name: "the fixture answers the agent list",
			env:  []string{path, "LYNA_TMUX_DEMO_AGENTS=1", "LYNA_TMUX_DEMO_PROJECT=" + project},
			args: []string{"agents", "--json"},
			want: []string{"rate-limit-middleware", "flaky-test-hunt", "docs-pass", "acme-web"},
			// The real list is still consulted, for the agents of the recorded
			// project, which are the ones with a pane to jump to: three
			// fixture jobs and the one real workspace.
			records: 4,
		},
		{
			name:     "without the variable the real binary answers",
			env:      []string{path, "LYNA_TMUX_DEMO_PROJECT=" + project},
			args:     []string{"agents", "--json"},
			want:     []string{`"name":"real"`},
			unwanted: []string{"rate-limit-middleware"},
			records:  1,
		},
		{
			name:     "every other call reaches the real binary",
			env:      []string{path, "LYNA_TMUX_DEMO_AGENTS=1"},
			args:     []string{"--version"},
			want:     []string{"real claude --version"},
			unwanted: []string{"rate-limit-middleware"},
		},
		{
			name:     "the agent list of another command is not the fixture",
			env:      []string{path, "LYNA_TMUX_DEMO_AGENTS=1"},
			args:     []string{"agents"},
			want:     []string{"real claude agents"},
			unwanted: []string{"rate-limit-middleware"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := runFixtureClaude(t, project, tc.env, tc.args...)
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout does not hold %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(stdout, unwanted) {
					t.Fatalf("stdout holds %q:\n%s", unwanted, stdout)
				}
			}
			if tc.records == 0 {
				return
			}
			records, err := agent.ParseRecords([]byte(strings.TrimSpace(stdout)))
			if err != nil {
				t.Fatalf("lyna-tmux cannot read the list: %v\n%s", err, stdout)
			}
			if len(records) != tc.records {
				t.Fatalf("%d records, want %d:\n%s", len(records), tc.records, stdout)
			}
			for _, r := range records {
				if r.CWD == "" || r.Kind == "" || r.StartedAt == 0 {
					t.Fatalf("incomplete record %+v", r)
				}
			}
		})
	}
}

// TestFixtureClaudeIsValidJSON keeps the fixture readable by the tool it feeds,
// whatever a later edit does to it.
func TestFixtureClaudeIsValidJSON(t *testing.T) {
	root := t.TempDir()
	env := []string{
		"PATH=" + filepath.Dir(fixtureClaude(t)) + ":/usr/bin:/bin",
		"LYNA_TMUX_DEMO_AGENTS=1", "LYNA_TMUX_DEMO_PROJECT=" + root,
	}
	// With no real binary behind it the wrapper still answers, from the
	// fixture alone.
	stdout, stderr, exit := runFixtureClaude(t, root, env, "agents", "--json")
	if exit != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if len(raw) != 3 {
		t.Fatalf("%d fixture records, want 3:\n%s", len(raw), stdout)
	}
	for _, r := range raw {
		if _, ok := r["cwd"]; !ok {
			t.Fatalf("a record has no working directory: %v", r)
		}
	}
}

// fixtureShell is the shell the recordings open their panes with.
func fixtureShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the recording fixtures need a POSIX host")
	}
	return filepath.Join(repoRoot(t), "docs", "scenes", "fixtures", "bin", "shell")
}

// startupHome is a home directory whose start-up files would change what a
// recorded pane shows.
func startupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{".bashrc", ".bash_profile", ".profile"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("PS1='rc> '\necho START-UP\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// TestFixtureShell covers the command form of the shell a recorded pane runs:
// tmux hands the command of a session to it with -c, and what comes back must
// be what the scene asked for, not what the account's own setup prints.
func TestFixtureShell(t *testing.T) {
	sh := fixtureShell(t)
	info, err := os.Stat(sh)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("%s is not executable (%s): tmux cannot start a pane with it", sh, info.Mode())
	}
	home := startupHome(t)
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "PS1=the account's own"}
	cases := []struct {
		name, command, want string
	}{
		{name: "the shell is bash, not the account's", command: `printf %s "${BASH_VERSION:+bash}"`, want: "bash"},
		{name: "the command runs as it was given", command: "printf %s ok", want: "ok"},
		{name: "no start-up file is read", command: "printf %s done", want: "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(sh, "-c", tc.command)
			cmd.Env = env
			cmd.Dir = home
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v\nstderr: %s", err, errOut.String())
			}
			if out.String() != tc.want {
				t.Fatalf("output %q, want %q", out.String(), tc.want)
			}
			if errOut.Len() > 0 {
				t.Fatalf("stderr: %s", errOut.String())
			}
		})
	}
}

// TestFixtureShellPrompt starts the fixture shell the way tmux starts a pane
// shell, with no argument at all, and reads the prompt off the screen. That
// is the form that draws a prompt and reads start-up files, so it is the one
// that decides what the recordings show.
func TestFixtureShellPrompt(t *testing.T) {
	sh := fixtureShell(t)
	home := startupHome(t)
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	if _, err := srv.Client.Run(ctx, "set-option", "-g", "default-shell", sh); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "new-window", "-d", "-n", "shell", "-c", home,
		"-e", "HOME="+home, "-e", "PS1=the account's own"); err != nil {
		t.Fatal(err)
	}
	var screen string
	tmuxtest.WaitFor(t, "the shell prompt", func() bool {
		out, err := srv.Client.Run(ctx, "capture-pane", "-p", "-t", "=base:shell")
		if err != nil {
			return false
		}
		screen = out
		return strings.TrimRight(out, " \n") == "$"
	})
	for _, unwanted := range []string{"START-UP", "rc>", "the account's own"} {
		if strings.Contains(screen, unwanted) {
			t.Fatalf("the pane shows %q:\n%s", unwanted, screen)
		}
	}
}
