package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// demoRepoScript is the builder the git scenes are recorded against.
func demoRepoScript(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the recording fixtures need a POSIX host")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	return filepath.Join(repoRoot(t), "docs", "scenes", "bin", "demo-repo")
}

// runDemoRepo builds the repository at dir and fails the test if the builder
// does not finish.
func runDemoRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command(demoRepoScript(t), dir)
	// A recorder's own git configuration must not decide what the scene shows,
	// and the builder reaches no network, so the run is sealed off from both.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "gitconfig"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(t.TempDir(), "gitconfig-system"),
		"GIT_TERMINAL_PROMPT=0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("demo-repo %s: %v\n%s", dir, err, out)
	}
}

// git reads the repository the builder wrote.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--no-pager", "-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	// Only the trailing newline goes: the two columns of a status line say what
	// is staged and what is not by the space in front of the first one.
	return strings.TrimRight(string(out), "\n")
}

// TestDemoRepoWritesWhatTheScenesShow holds the builder to the repository the
// pictures need: every region of the workstation has something in it, and the
// history has the merges that make its lanes cross.
func TestDemoRepoWritesWhatTheScenesShow(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "acme-gateway")
	runDemoRepo(t, dir)

	cases := []struct {
		name string
		args []string
		want []string
		// least is the smallest number of lines the output may have, for a
		// reading whose exact list is not what matters.
		least int
	}{
		{
			name: "the branches of the project",
			args: []string{"branch", "--format=%(refname:short)"},
			want: []string{"feat/audit", "feat/orders", "feat/ratelimit", "feat/search", "main"},
		},
		{
			name: "the branches of the remote",
			args: []string{"branch", "--remotes", "--format=%(refname:short)"},
			want: []string{"origin/feat/orders", "origin/feat/ratelimit", "origin/main"},
		},
		{
			name: "the tags",
			args: []string{"tag", "--sort=refname"},
			want: []string{"v0.1.0", "v0.2.0"},
		},
		{
			name:  "a stash to pop",
			args:  []string{"stash", "list"},
			least: 1,
		},
		{
			name:  "the worktrees, the main one and the two opened beside it",
			args:  []string{"worktree", "list"},
			least: 3,
		},
		{
			name: "the merges whose lanes cross",
			args: []string{"log", "--branches", "--merges", "--format=%s"},
			want: []string{"merge: admit requests at a rate", "merge: the order endpoints"},
		},
		{
			name:  "a history worth a page",
			args:  []string{"log", "--branches", "--format=%h"},
			least: 12,
		},
		{
			name: "main follows the remote",
			args: []string{"rev-parse", "--abbrev-ref", "main@{upstream}"},
			want: []string{"origin/main"},
		},
		{
			name: "main is ahead of what the remote holds",
			args: []string{"rev-list", "--count", "origin/main..main"},
			want: []string{"1"},
		},
		{
			name: "a working tree with something staged, something not, and something new",
			args: []string{"status", "--porcelain=v1"},
			want: []string{" M CHANGELOG.md", "?? internal/gateway/notes.go", "M  README.md"},
		},
		{
			name: "every author is the one the pictures name",
			args: []string{"log", "--branches", "--format=%an <%ae>"},
			want: []string{"Acme Dev <dev@example.com>"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(gitOut(t, dir, tc.args...), "\n")
			if tc.least > 0 {
				var count int
				for _, line := range lines {
					if strings.TrimSpace(line) != "" {
						count++
					}
				}
				if count < tc.least {
					t.Fatalf("git %s gave %d lines, want at least %d:\n%s",
						strings.Join(tc.args, " "), count, tc.least, strings.Join(lines, "\n"))
				}
				return
			}
			seen := map[string]bool{}
			for _, line := range lines {
				if s := strings.TrimRight(line, " \t\r"); strings.TrimSpace(s) != "" {
					seen[s] = true
				}
			}
			for _, w := range tc.want {
				if !seen[w] {
					t.Errorf("git %s does not have %q:\n%s",
						strings.Join(tc.args, " "), w, strings.Join(lines, "\n"))
				}
			}
			if len(tc.want) > 0 && len(seen) != len(tc.want) {
				// A reading whose whole list is named has exactly that list, so
				// a ref left behind by an earlier run is caught here.
				t.Errorf("git %s gave %d entries, want %d:\n%s",
					strings.Join(tc.args, " "), len(seen), len(tc.want), strings.Join(lines, "\n"))
			}
		})
	}
}

// TestDemoRepoStartsFromTheSameRepository holds the builder to a recording
// that shows the same repository every time: the second run writes over the
// first rather than adding to it.
func TestDemoRepoStartsFromTheSameRepository(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "acme-gateway")
	runDemoRepo(t, dir)
	first := gitOut(t, dir, "log", "--branches", "--format=%s")
	runDemoRepo(t, dir)
	if got := gitOut(t, dir, "log", "--branches", "--format=%s"); got != first {
		t.Fatalf("the second run wrote another history:\nfirst:\n%s\n\nsecond:\n%s", first, got)
	}
	if got := gitOut(t, dir, "stash", "list"); strings.Count(got, "\n") != 0 {
		t.Fatalf("the second run left more than one stash:\n%s", got)
	}
}

// TestDemoRepoRefusesWhatItCannotWrite keeps a builder that removes its target
// from being pointed at anything but a directory of its own.
func TestDemoRepoRefusesWhatItCannotWrite(t *testing.T) {
	t.Parallel()
	script := demoRepoScript(t)
	cases := []struct {
		name string
		args []string
	}{
		{name: "no directory", args: nil},
		{name: "two directories", args: []string{"one", "two"}},
		{name: "the root", args: []string{"/"}},
		{name: "a parent that is not there", args: []string{filepath.Join(t.TempDir(), "no", "such", "dir")}},
		{name: "a directory it did not write", args: []string{notOurs(t)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command(script, tc.args...).CombinedOutput()
			if err == nil {
				t.Fatalf("demo-repo %v finished, want a refusal:\n%s", tc.args, out)
			}
			if !strings.Contains(string(out), "demo-repo:") {
				t.Fatalf("demo-repo %v said nothing about why:\n%s", tc.args, out)
			}
		})
	}
}

// notOurs is a directory holding something the builder did not write, which it
// must refuse to remove.
func notOurs(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme-gateway")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
