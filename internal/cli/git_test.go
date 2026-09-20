package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepoFor makes a repository with one commit under the home of the test
// environment, which is what the workstation is opened on.
func gitRepoFor(t *testing.T, e *cliEnv, name string) string {
	t.Helper()
	dir := filepath.Join(e.host.Home, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + e.host.Home,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	init := exec.Command("git", "init", "-q", "-b", "main", dir)
	init.Env = env
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello-git.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "the first commit of the workstation")
	return dir
}

// TestGitCLI drives `lmux git` the way a pane and a popup run it, and holds
// every way it refuses to a line that says why.
func TestGitCLI(t *testing.T) {
	e := newCLIEnv(t)
	e.start(t, "api", e.host.Home)
	repo := gitRepoFor(t, e, "workstation")

	cases := []struct {
		name        string
		env         map[string]string
		nonTerminal bool
		inRepo      bool
		args        []string
		// keys drives a running workstation; nil runs the command to
		// completion.
		keys     func(t *testing.T, term *watchTerm)
		wantCode int
		outHas   []string
		errHas   []string
	}{
		{
			name: "outside tmux a popup follows the named workspace and closes on q",
			env:  map[string]string{"TMUX": ""},
			args: []string{"git", "--session", "api", "--dir", "workstation", "--popup"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "the first commit")
				term.waitScreen(t, "main")
				term.send(t, "q")
			},
		},
		{
			name:   "the current directory is the default, and a pane closes on ctrl+c",
			env:    map[string]string{"TMUX": ""},
			inRepo: true,
			args:   []string{"git", "--session", "api"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "the first commit")
				// A pane is not a popup, so q is a key of the view and not a
				// way out of it.
				term.send(t, "q")
				term.send(t, "\x03")
			},
		},
		{
			name: "no terminal", nonTerminal: true, args: []string{"git", "--session", "api"},
			wantCode: 1, errHas: []string{"git draws a live view and needs a terminal"},
		},
		{
			name: "outside tmux without a workspace named", env: map[string]string{"TMUX": ""},
			inRepo: true, args: []string{"git"},
			wantCode: 1, errHas: []string{"no workspace to follow: outside tmux, pass --session"},
		},
		{
			name: "a directory that holds no repository", env: map[string]string{"TMUX": ""},
			args: []string{"git", "--session", "api"}, wantCode: 1, errHas: []string{"not a git repository"},
		},
		{
			name: "a directory that is not there", env: map[string]string{"TMUX": ""},
			args:     []string{"git", "--session", "api", "--dir", "gone"},
			wantCode: 1, errHas: []string{"git directory", "no such file"},
		},
		{
			name: "arguments are rejected", args: []string{"git", "extra"},
			wantCode: 1, errHas: []string{"unknown command \"extra\""},
		},
		{
			name: "it is a command of its own", args: []string{"--help"},
			outHas: []string{"\n    git "},
		},
		{
			name: "own help", args: []string{"git", "--help"},
			outHas: []string{"--session", "--dir", "--popup", "stage and commit"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := map[string]string{}
			for k, v := range tc.env {
				saved[k] = e.env[k]
				e.setenv(k, v)
			}
			e.cwd, e.term.Interactive = e.host.Home, !tc.nonTerminal
			if tc.inRepo {
				e.cwd = repo
			}
			t.Cleanup(func() {
				for k, v := range saved {
					e.setenv(k, v)
				}
			})
			term := newWatchTerm(t)
			wait := watchRunCLI(t, e, term, tc.args...)
			if tc.keys != nil {
				tc.keys(t, term)
			}
			code, stdout, stderr := wait(), term.out.String(), term.errOut.String()
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
		})
	}
}
