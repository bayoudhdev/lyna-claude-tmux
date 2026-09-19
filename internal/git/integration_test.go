package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/gittest"
)

// newRepo is a repository of the test's own with a runner wired to it.
func newRepo(t *testing.T) *repo {
	t.Helper()
	r := gittest.New(t)
	return &repo{Repo: r, Runner: Runner{Environ: r.Environ()}}
}

// repo is a test repository and the runner that reads it.
type repo struct {
	*gittest.Repo
	Runner Runner
}

func TestIntegrationChanges(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(r *repo)
		branch func(b vcs.Head) bool
		want   []vcs.File
		added  int
	}{
		{
			name:   "clean after commit",
			setup:  func(r *repo) { r.Write("a.txt", "a\n"); r.Git("add", "."); r.Git("commit", "-qm", "init") },
			branch: func(b vcs.Head) bool { return b.Name == "main" && len(b.OID) >= 40 && !b.Initial },
			want:   []vcs.File{},
		},
		{
			name: "modified, renamed, staged, untracked and unusual names",
			setup: func(r *repo) {
				r.Write("keep.txt", "a\nb\n")
				r.Write("old.txt", "x\n")
				r.Git("add", ".")
				r.Git("commit", "-qm", "init")
				r.Write("keep.txt", "a\nc\nd\n")
				r.Git("mv", "old.txt", "new name.txt")
				r.Write("tab\tx.txt", "n\n")
				r.Git("add", "tab\tx.txt")
				r.Write("dir/untracked é.txt", "u\n")
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
			setup:  func(r *repo) { r.Write("f", "hi\nthere\n"); r.Git("add", "f") },
			branch: func(b vcs.Head) bool { return b.Initial && b.Name == "main" && b.OID == "" },
			want:   []vcs.File{{Kind: vcs.KindChanged, Path: "f", Index: 'A', Worktree: '.', Added: 2}},
			added:  2,
		},
		{
			name: "ahead of upstream",
			setup: func(r *repo) {
				r.Write("a", "1\n")
				r.Git("add", ".")
				r.Git("commit", "-qm", "one")
				r.Git("branch", "base")
				r.Git("branch", "--set-upstream-to=base")
				r.Write("a", "2\n")
				r.Git("commit", "-qam", "two")
			},
			branch: func(b vcs.Head) bool { return b.Upstream == "base" && b.AheadBehind && b.Ahead == 1 && b.Behind == 0 },
			want:   []vcs.File{},
		},
		{
			name: "detached head",
			setup: func(r *repo) {
				r.Write("a", "1\n")
				r.Git("add", ".")
				r.Git("commit", "-qm", "one")
				r.Git("checkout", "-q", "--detach")
			},
			branch: func(b vcs.Head) bool { return b.Detached && b.Name == "" },
			want:   []vcs.File{},
		},
		{
			name: "binary file",
			setup: func(r *repo) {
				r.Write("img.bin", "\x00\x01\x02")
				r.Git("add", ".")
			},
			branch: func(vcs.Head) bool { return true },
			want:   []vcs.File{{Kind: vcs.KindChanged, Path: "img.bin", Index: 'A', Worktree: '.', Binary: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			tc.setup(r)
			runner := r.Runner
			// GIT_DIR in the base environment must not redirect -C.
			env := slices.Concat(r.Env, []string{"GIT_DIR=" + t.TempDir()})
			runner.Environ = func() []string { return env }
			ch, err := runner.Changes(context.Background(), r.Dir)
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
	gittest.Require(t)
	plain := t.TempDir()
	r := newRepo(t)
	r.Write("a", strings.Repeat("line\n", 100))
	cases := []struct {
		name    string
		runner  Runner
		dir     string
		wantErr error
	}{
		{name: "not a repository", runner: Runner{Environ: func() []string { return gittest.Env(plain) }}, dir: plain, wantErr: ErrNotRepository},
		{name: "output over the cap", runner: Runner{MaxOutput: 8, Environ: func() []string { return r.Env }}, dir: r.Dir, wantErr: ErrOutputTooLarge},
		{name: "missing git", runner: Runner{Bin: filepath.Join(plain, "git")}, dir: r.Dir, wantErr: ErrNotInstalled},
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
	r := newRepo(t)
	r.Write("a", "1\n")
	r.Git("add", ".")
	r.Git("commit", "-qm", "one")
	r.Write("sub/deep/x", "1\n")
	linked := filepath.Join(filepath.Dir(r.Dir), "linked")
	r.Git("worktree", "add", "-q", "-b", "side", linked)

	cases := []struct {
		name string
		dir  string
		want Repo
	}{
		{name: "main worktree from a subdirectory", dir: filepath.Join(r.Dir, "sub", "deep"), want: Repo{Root: r.Dir, GitDir: filepath.Join(r.Dir, ".git"), CommonDir: filepath.Join(r.Dir, ".git")}},
		{name: "linked worktree", dir: linked, want: Repo{Root: linked, GitDir: filepath.Join(r.Dir, ".git", "worktrees", "linked"), CommonDir: filepath.Join(r.Dir, ".git")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Runner.Repo(context.Background(), tc.dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Repo = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestIntegrationWorktrees reads the worktrees of a real repository: the one
// the project is in, one on a branch of its own, one detached and locked, and
// one whose directory was taken away.
func TestIntegrationWorktrees(t *testing.T) {
	r := newRepo(t)
	r.Commit("first", "a.txt", "a\n")
	base := filepath.Dir(r.Dir)
	r.Git("worktree", "add", "-q", filepath.Join(base, "task-a"), "-b", "task-a")
	r.Git("worktree", "add", "-q", "--detach", filepath.Join(base, "held"), "HEAD")
	r.Git("worktree", "lock", filepath.Join(base, "held"), "--reason", "held by a test")
	r.Git("worktree", "add", "-q", filepath.Join(base, "gone"), "-b", "gone")
	if err := os.RemoveAll(filepath.Join(base, "gone")); err != nil {
		t.Fatal(err)
	}

	got, err := r.Runner.Worktrees(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Worktrees() error = %v", err)
	}
	byName := map[string]vcs.Worktree{}
	for _, w := range got {
		byName[w.Name()] = w
	}
	if len(byName) != 4 {
		t.Fatalf("Worktrees() = %+v, want the project and three worktrees", got)
	}
	main, ok := byName["main"]
	if !ok || main.Path != r.Dir || main.Branch != "refs/heads/main" || len(main.Head) != 40 {
		t.Fatalf("the project reads as %+v, want %s on main", main, r.Dir)
	}
	if w := byName["task-a"]; w.Path != filepath.Join(base, "task-a") || w.Branch != "refs/heads/task-a" || w.Detached {
		t.Fatalf("task-a reads as %+v", w)
	}
	if w := byName["held"]; !w.Detached || !w.Locked || w.LockReason != "held by a test" {
		t.Fatalf("the locked worktree reads as %+v", w)
	}
	if w := byName["gone"]; !w.Prunable || w.PruneReason == "" {
		t.Fatalf("the deleted worktree reads as %+v, want it prunable with a reason", w)
	}
}

// TestIntegrationBranches reads the branches of a real repository against a
// remote: one level with its upstream, one ahead of it, one whose upstream was
// deleted, and one that tracks nothing.
func TestIntegrationBranches(t *testing.T) {
	r := newRepo(t)
	r.Commit("first commit subject", "a.txt", "a\n")
	remote := filepath.Join(filepath.Dir(r.Dir), "origin.git")
	r.Git("init", "-q", "--bare", remote)
	r.Git("remote", "add", "origin", remote)
	r.Git("push", "-q", "-u", "origin", "main")
	r.Git("checkout", "-q", "-b", "ahead")
	r.Git("push", "-q", "-u", "origin", "ahead")
	r.Git("commit", "-q", "--allow-empty", "-m", "not pushed yet")
	r.Git("checkout", "-q", "-b", "cut")
	r.Git("push", "-q", "-u", "origin", "cut")
	r.Git("push", "-q", "origin", "--delete", "cut")
	r.Git("fetch", "-q", "--prune", "origin")
	r.Git("checkout", "-q", "-b", "alone")

	got, err := r.Runner.Branches(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	byName := map[string]vcs.LocalBranch{}
	for _, b := range got {
		byName[b.Name] = b
	}
	if len(byName) != 4 {
		t.Fatalf("Branches() = %+v, want four branches", got)
	}
	if b := byName["main"]; b.Upstream != "refs/remotes/origin/main" || b.Ahead != 0 || b.Behind != 0 || b.Subject != "first commit subject" {
		t.Fatalf("main reads as %+v", b)
	}
	if b := byName["ahead"]; b.Ahead != 1 || b.Behind != 0 || b.Gone {
		t.Fatalf("the branch with a commit of its own reads as %+v", b)
	}
	if b := byName["cut"]; !b.Gone || b.Upstream == "" {
		t.Fatalf("the branch whose upstream was deleted reads as %+v", b)
	}
	alone := byName["alone"]
	if alone.Upstream != "" || alone.Gone || !alone.Head {
		t.Fatalf("the branch that tracks nothing reads as %+v, want it current with no upstream", alone)
	}
	if alone.Tip.IsZero() {
		t.Fatalf("branch %+v has no date", alone)
	}
}
