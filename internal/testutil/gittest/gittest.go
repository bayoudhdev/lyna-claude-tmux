// Package gittest builds throwaway git repositories for tests: a repository
// in a temporary directory, run with an environment of its own, so a test
// never reads or writes the user's git configuration and never depends on it.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a git repository a test owns: its directory and the environment
// every git command against it runs with.
type Repo struct {
	t *testing.T
	// Dir is the working tree, with symlinks resolved so the paths git prints
	// compare equal to the ones the test built.
	Dir string
	// Env is the environment of the repository, hermetic by construction.
	Env []string
}

// Require skips the test when git is not installed.
func Require(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// Env is an environment that reads no configuration of the user's: no system
// and no global file, a home of its own, and an author and committer so a
// commit needs no identity from anywhere else.
func Env(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
}

// New makes an initialized repository on branch main in a directory of the
// test's own.
func New(t *testing.T) *Repo {
	t.Helper()
	r := At(t, "repo")
	if err := os.Mkdir(r.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("init", "-q", "-b", "main")
	return r
}

// At is a repository that does not exist yet: the directory named under a
// temporary root, with the environment a repository there would run with. The
// caller creates it, which is what a test of a directory outside any
// repository wants.
func At(t *testing.T, name string) *Repo {
	t.Helper()
	Require(t)
	base := t.TempDir()
	// Resolve symlinks (macOS /var -> /private/var) so the absolute paths git
	// prints compare equal to the ones the test holds.
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	dir := base
	if name != "" {
		dir = filepath.Join(base, name)
	}
	return &Repo{t: t, Dir: dir, Env: Env(base)}
}

// Git runs one git command in the repository and returns its output, failing
// the test when the command does.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = r.Env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Write puts content at a path of the repository, making the directories it
// needs.
func (r *Repo) Write(name, content string) {
	r.t.Helper()
	path := filepath.Join(r.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Environ returns the environment as a function, which is the shape the git
// adapter takes it in.
func (r *Repo) Environ() func() []string {
	env := r.Env
	return func() []string { return env }
}

// Commit writes a file, stages everything and commits it with the subject
// given, which is the three commands a test repeats most.
func (r *Repo) Commit(subject, name, content string) {
	r.t.Helper()
	r.Write(name, content)
	r.Git("add", ".")
	r.Git("commit", "-qm", subject)
}
