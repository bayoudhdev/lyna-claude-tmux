package watch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// testRepo is a git repository in a temporary directory with a hermetic git
// environment: no system or global configuration, a private HOME.
type testRepo struct {
	t   *testing.T
	dir string
	env []string
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func hermeticGitEnv(home string) []string {
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

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	// Resolve symlinks (macOS /var -> /private/var) so git's absolute paths
	// compare equal.
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	r := &testRepo{t: t, dir: filepath.Join(base, "repo"), env: hermeticGitEnv(base)}
	if err := os.Mkdir(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (r *testRepo) write(name, content string) {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) runner() Runner {
	env := r.env
	return Runner{Environ: func() []string { return env }}
}

func TestIntegrationChanges(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(r *testRepo)
		branch func(b vcs.Head) bool
		want   []vcs.File
		added  int
	}{
		{
			name:   "clean after commit",
			setup:  func(r *testRepo) { r.write("a.txt", "a\n"); r.git("add", "."); r.git("commit", "-qm", "init") },
			branch: func(b vcs.Head) bool { return b.Name == "main" && len(b.OID) >= 40 && !b.Initial },
			want:   []vcs.File{},
		},
		{
			name: "modified, renamed, staged, untracked and unusual names",
			setup: func(r *testRepo) {
				r.write("keep.txt", "a\nb\n")
				r.write("old.txt", "x\n")
				r.git("add", ".")
				r.git("commit", "-qm", "init")
				r.write("keep.txt", "a\nc\nd\n")
				r.git("mv", "old.txt", "new name.txt")
				r.write("tab\tx.txt", "n\n")
				r.git("add", "tab\tx.txt")
				r.write("dir/untracked é.txt", "u\n")
			},
			branch: func(b vcs.Head) bool { return b.Name == "main" },
			want: []vcs.File{
				{Kind: vcs.KindUntracked, Path: "dir/", Index: '?', Worktree: '?'},
				{Kind: vcs.KindChanged, Path: "keep.txt", Index: '.', Worktree: 'M', Added: 2, Deleted: 1},
				{Kind: vcs.KindRenamed, Path: "new name.txt", OrigPath: "old.txt", Index: 'R', Worktree: '.'},
				{Kind: vcs.KindChanged, Path: "tab\tx.txt", Index: 'A', Worktree: '.', Added: 1},
			},
			added: 3,
		},
		{
			name:   "first commit not made yet",
			setup:  func(r *testRepo) { r.write("f", "hi\nthere\n"); r.git("add", "f") },
			branch: func(b vcs.Head) bool { return b.Initial && b.Name == "main" && b.OID == "" },
			want:   []vcs.File{{Kind: vcs.KindChanged, Path: "f", Index: 'A', Worktree: '.', Added: 2}},
			added:  2,
		},
		{
			name: "ahead of upstream",
			setup: func(r *testRepo) {
				r.write("a", "1\n")
				r.git("add", ".")
				r.git("commit", "-qm", "one")
				r.git("branch", "base")
				r.git("branch", "--set-upstream-to=base")
				r.write("a", "2\n")
				r.git("commit", "-qam", "two")
			},
			branch: func(b vcs.Head) bool { return b.Upstream == "base" && b.AheadBehind && b.Ahead == 1 && b.Behind == 0 },
			want:   []vcs.File{},
		},
		{
			name: "detached head",
			setup: func(r *testRepo) {
				r.write("a", "1\n")
				r.git("add", ".")
				r.git("commit", "-qm", "one")
				r.git("checkout", "-q", "--detach")
			},
			branch: func(b vcs.Head) bool { return b.Detached && b.Name == "" },
			want:   []vcs.File{},
		},
		{
			name: "binary file",
			setup: func(r *testRepo) {
				r.write("img.bin", "\x00\x01\x02")
				r.git("add", ".")
			},
			branch: func(vcs.Head) bool { return true },
			want:   []vcs.File{{Kind: vcs.KindChanged, Path: "img.bin", Index: 'A', Worktree: '.', Binary: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t)
			tc.setup(r)
			runner := r.runner()
			// GIT_DIR in the base environment must not redirect -C.
			env := slices.Concat(r.env, []string{"GIT_DIR=" + t.TempDir()})
			runner.Environ = func() []string { return env }
			ch, err := runner.Changes(context.Background(), r.dir)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.branch(ch.Head) {
				t.Errorf("branch = %+v", ch.Head)
			}
			if !reflect.DeepEqual(ch.Files, tc.want) {
				t.Errorf("files =\n%+v\nwant\n%+v", ch.Files, tc.want)
			}
			if ch.Added != tc.added {
				t.Errorf("added = %d, want %d", ch.Added, tc.added)
			}
		})
	}
}

func TestIntegrationRunnerRefusals(t *testing.T) {
	requireGit(t)
	plain := t.TempDir()
	r := newTestRepo(t)
	r.write("a", strings.Repeat("line\n", 100))
	cases := []struct {
		name    string
		runner  Runner
		dir     string
		wantErr error
	}{
		{name: "not a repository", runner: Runner{Environ: func() []string { return hermeticGitEnv(plain) }}, dir: plain, wantErr: ErrNotRepository},
		{name: "output over the cap", runner: Runner{MaxOutput: 8, Environ: func() []string { return r.env }}, dir: r.dir, wantErr: ErrOutputTooLarge},
		{name: "missing git", runner: Runner{Bin: filepath.Join(plain, "git")}, dir: r.dir, wantErr: ErrGitNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.runner.Changes(context.Background(), tc.dir); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Changes error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestIntegrationRepo(t *testing.T) {
	r := newTestRepo(t)
	r.write("a", "1\n")
	r.git("add", ".")
	r.git("commit", "-qm", "one")
	r.write("sub/deep/x", "1\n")
	linked := filepath.Join(filepath.Dir(r.dir), "linked")
	r.git("worktree", "add", "-q", "-b", "side", linked)

	cases := []struct {
		name string
		dir  string
		want Repo
	}{
		{name: "main worktree from a subdirectory", dir: filepath.Join(r.dir, "sub", "deep"), want: Repo{Root: r.dir, GitDir: filepath.Join(r.dir, ".git"), CommonDir: filepath.Join(r.dir, ".git")}},
		{name: "linked worktree", dir: linked, want: Repo{Root: linked, GitDir: filepath.Join(r.dir, ".git", "worktrees", "linked"), CommonDir: filepath.Join(r.dir, ".git")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.runner().Repo(context.Background(), tc.dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Repo = %+v, want %+v", got, tc.want)
			}
		})
	}
}
