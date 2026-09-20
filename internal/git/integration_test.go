package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
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
// TestIntegrationMerged reads a real repository for the answer the form that
// deletes a branch is worded from.
func TestIntegrationMerged(t *testing.T) {
	r := newRepo(t)
	r.Commit("first", "a.txt", "a\n")
	r.Git("branch", "done")
	r.Git("checkout", "-q", "-b", "solo")
	r.Commit("second", "b.txt", "b\n")
	r.Git("checkout", "-q", "main")
	r.Git("checkout", "-q", "-b", "folded")
	r.Commit("third", "c.txt", "c\n")
	r.Git("checkout", "-q", "main")
	r.Git("merge", "-q", "--no-ff", "-m", "fold it in", "folded")

	cases := []struct {
		name string
		want bool
	}{
		{name: "done", want: true},
		{name: "folded", want: true},
		{name: "solo"},
		{name: "never-was"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Runner.Merged(t.Context(), r.Dir, tc.name, "HEAD")
			if err != nil {
				t.Fatalf("Merged() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("Merged(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

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

// history builds a repository whose shape the history reads are checked
// against: a first commit, a branch that diverged, a merge of the two, a tag
// on the merge and a commit on top of it.
func history(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.Commit("first commit subject", "a.txt", "a\n")
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("a change of the branch", "side.txt", "s\n")
	r.Git("checkout", "-q", "main")
	r.Commit("a change of the project", "main.txt", "m\n")
	r.Git("merge", "-q", "--no-ff", "-m", "a merge commit", "side")
	r.Git("tag", "-a", "v0.1.0", "-m", "the first tag")
	r.Commit("the last commit", "a.txt", "b\n")
	return r
}

func TestIntegrationLog(t *testing.T) {
	r := history(t)
	got, err := r.Runner.Log(t.Context(), r.Dir, LogOptions{})
	if err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("Log() read %d commits, want the whole history: %+v", len(got), got)
	}
	subjects := make([]string, len(got))
	at := map[string]vcs.Commit{}
	for i, c := range got {
		subjects[i] = c.Subject
		at[c.Subject] = c
		if !isHexOID(c.OID) || c.Author == "" || c.Authored.IsZero() || c.Committed.IsZero() {
			t.Fatalf("commit %+v is missing what every commit carries", c)
		}
	}
	if subjects[0] != "the last commit" || subjects[len(subjects)-1] != "first commit subject" {
		t.Fatalf("Log() read %v, want the newest commit first and the oldest last", subjects)
	}
	merge := at["a merge commit"]
	if !merge.Merge() || len(merge.Parents) != 2 {
		t.Fatalf("the merge reads as %+v", merge)
	}
	// A commit is never read before a commit that follows it.
	seen := map[string]bool{}
	for _, c := range got {
		for _, p := range c.Parents {
			if seen[p] {
				t.Fatalf("Log() read the parent %s before its child %s", p[:7], c.Short())
			}
		}
		seen[c.OID] = true
	}
	var tags, branches int
	for _, c := range got {
		for _, ref := range c.Refs {
			switch ref.Kind {
			case vcs.RefTag:
				tags++
				if ref.Name != "v0.1.0" {
					t.Fatalf("the tag reads as %+v", ref)
				}
			case vcs.RefBranch:
				branches++
			case vcs.RefRemote, vcs.RefOther:
			}
		}
	}
	if tags != 1 || branches != 2 {
		t.Fatalf("Log() read %d tags and %d branches, want the tag and both branches", tags, branches)
	}
	head := at["the last commit"]
	if len(head.Refs) != 1 || head.Refs[0].Name != "main" || !head.Refs[0].Head {
		t.Fatalf("the last commit carries %+v, want the branch HEAD is on", head.Refs)
	}
}

func TestIntegrationLogOptions(t *testing.T) {
	cases := []struct {
		name string
		opt  LogOptions
		want []string
	}{
		{
			name: "a page of the history",
			opt:  LogOptions{Max: 2},
			want: []string{"the last commit", "a merge commit"},
		},
		{
			name: "the page after it",
			opt:  LogOptions{Max: 2, Skip: 2},
			// Topological order walks the branch merged in before the commit
			// it was merged into.
			want: []string{"a change of the branch", "a change of the project"},
		},
		{
			name: "one branch alone",
			opt:  LogOptions{Revs: []string{"side"}},
			want: []string{"a change of the branch", "first commit subject"},
		},
		{
			name: "what a branch holds that another does not",
			opt:  LogOptions{Revs: []string{"side", "^main~2"}},
			want: []string{"a change of the branch"},
		},
		{
			name: "the commits that touched a file",
			opt:  LogOptions{Paths: []string{"side.txt"}},
			want: []string{"a change of the branch"},
		},
		{
			name: "the first parent of every merge",
			opt:  LogOptions{FirstParent: true, Revs: []string{"main"}},
			want: []string{"the last commit", "a merge commit", "a change of the project", "first commit subject"},
		},
		{
			name: "a tag names a commit",
			opt:  LogOptions{Max: 1, Revs: []string{"v0.1.0"}},
			want: []string{"a merge commit"},
		},
	}
	r := history(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Runner.Log(t.Context(), r.Dir, tc.opt)
			if err != nil {
				t.Fatalf("Log() error = %v", err)
			}
			subjects := make([]string, len(got))
			for i, c := range got {
				subjects[i] = c.Subject
			}
			if !slices.Equal(subjects, tc.want) {
				t.Fatalf("Log() read %v, want %v", subjects, tc.want)
			}
		})
	}
}

func TestIntegrationLogRefusals(t *testing.T) {
	cases := []struct {
		name string
		opt  LogOptions
	}{
		{name: "a revision git would read as an option", opt: LogOptions{Revs: []string{"--exec=id"}}},
		{name: "a revision that is empty", opt: LogOptions{Revs: []string{""}}},
		{name: "a path leaving the repository", opt: LogOptions{Paths: []string{"../secrets"}}},
		{name: "an absolute path", opt: LogOptions{Paths: []string{"/etc/passwd"}}},
		{name: "a page that counts backwards", opt: LogOptions{Skip: -1}},
	}
	r := history(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Runner.Log(t.Context(), r.Dir, tc.opt)
			if err == nil {
				t.Fatalf("Log() = %+v, want the reading refused", got)
			}
		})
	}
}

func TestIntegrationStashes(t *testing.T) {
	r := newRepo(t)
	r.Commit("first commit subject", "a.txt", "a\n")
	r.Write("a.txt", "b\n")
	r.Git("stash", "push", "-q", "-m", "a message of my own")
	r.Write("a.txt", "c\n")
	r.Git("stash", "push", "-q")
	r.Write("untracked.txt", "u\n")
	r.Write("a.txt", "d\n")
	r.Git("stash", "push", "-q", "-u", "-m", "with an untracked file")

	got, err := r.Runner.Stashes(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Stashes() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Stashes() read %+v, want three entries", got)
	}
	if s := got[0]; s.Ref != "stash@{0}" || s.Message != "with an untracked file" || !s.Untracked || s.Branch != "main" {
		t.Fatalf("the entry pushed last reads as %+v", s)
	}
	if s := got[1]; !s.WIP || s.Untracked || !strings.HasSuffix(s.Message, "first commit subject") {
		t.Fatalf("the entry pushed with no message reads as %+v", s)
	}
	if s := got[2]; s.Message != "a message of my own" || s.WIP {
		t.Fatalf("the entry pushed with a message reads as %+v", s)
	}
	for i, s := range got {
		if s.Index != i || !isHexOID(s.OID) || !isHexOID(s.Base) || s.Created.IsZero() {
			t.Fatalf("entry %d reads as %+v", i, s)
		}
	}

	r.Git("stash", "clear")
	empty, err := r.Runner.Stashes(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Stashes() error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Stashes() read %+v after the list was cleared", empty)
	}
}

// TestIntegrationInProgress puts a repository in the middle of each operation
// and reads it back. The states are the ones a user runs into: a merge, a
// cherry pick and a revert stopped on a conflict, a rebase stopped halfway,
// and a bisection.
func TestIntegrationInProgress(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *repo)
		want  vcs.InProgress
		heads int
	}{
		{
			name:  "a repository in the middle of nothing",
			setup: func(*testing.T, *repo) {},
			want:  vcs.InProgress{},
		},
		{
			name: "a merge stopped on a conflict",
			setup: func(t *testing.T, r *repo) {
				mustConflict(t, r, "merge", "side")
			},
			want:  vcs.InProgress{Kind: vcs.OperationMerge},
			heads: 1,
		},
		{
			name: "a cherry pick stopped on a conflict",
			setup: func(t *testing.T, r *repo) {
				mustConflict(t, r, "cherry-pick", "side")
			},
			want:  vcs.InProgress{Kind: vcs.OperationCherryPick},
			heads: 1,
		},
		{
			name: "a revert stopped on a conflict",
			setup: func(t *testing.T, r *repo) {
				r.Git("checkout", "-q", "side")
				r.Commit("a change on top", "a.txt", "on top\n")
				mustConflict(t, r, "revert", "--no-edit", "HEAD~1")
			},
			want:  vcs.InProgress{Kind: vcs.OperationRevert},
			heads: 1,
		},
		{
			name: "a rebase stopped on a conflict",
			setup: func(t *testing.T, r *repo) {
				r.Git("checkout", "-q", "side")
				r.Commit("a second change of the branch", "side.txt", "again\n")
				mustConflict(t, r, "rebase", "main")
			},
			want:  vcs.InProgress{Kind: vcs.OperationRebase, Branch: "side", Step: 1, Total: 2},
			heads: 1,
		},
		{
			name: "a bisection",
			setup: func(t *testing.T, r *repo) {
				if out, err := r.Try("bisect", "start", "main", "main~1"); err != nil {
					t.Fatalf("git bisect start: %v\n%s", err, out)
				}
			},
			want: vcs.InProgress{Kind: vcs.OperationBisect, Branch: "main", Bisecting: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := conflicting(t)
			tc.setup(t, r)
			got, err := r.Runner.InProgress(t.Context(), r.Dir)
			if err != nil {
				t.Fatalf("InProgress() error = %v", err)
			}
			if got.Kind != tc.want.Kind || got.Branch != tc.want.Branch ||
				got.Step != tc.want.Step || got.Total != tc.want.Total || got.Bisecting != tc.want.Bisecting {
				t.Fatalf("InProgress() = %+v, want %+v", got, tc.want)
			}
			if len(got.Heads) != tc.heads {
				t.Fatalf("InProgress() = %+v, want %d commits named", got, tc.heads)
			}
			for _, h := range got.Heads {
				if !isHexOID(h) {
					t.Fatalf("InProgress() names %q, which is no commit", h)
				}
			}
			if got.Kind == vcs.OperationRebase && !isHexOID(got.Onto) {
				t.Fatalf("the rebase reads as %+v, want the commit it replays onto", got)
			}
			if got.Running() != (tc.want.Kind != vcs.OperationNone) {
				t.Fatalf("Running() = %v for %+v", got.Running(), got)
			}
		})
	}
}

// conflicting is a repository whose branch and project changed the same file,
// so merging, picking or replaying one on the other stops.
func conflicting(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.Commit("first commit subject", "a.txt", "one\n")
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("a change of the branch", "a.txt", "branch\n")
	r.Git("checkout", "-q", "main")
	r.Commit("a change of the project", "a.txt", "project\n")
	return r
}

// mustConflict runs a command that has to stop on a conflict, which is what
// leaves the repository in the middle of the operation.
func mustConflict(t *testing.T, r *repo, args ...string) {
	t.Helper()
	out, err := r.Try(args...)
	if err == nil {
		t.Fatalf("git %v went through, want it stopped on a conflict\n%s", args, out)
	}
}

func TestIntegrationReadInProgressRefusals(t *testing.T) {
	r := newRepo(t)
	r.Commit("first", "a.txt", "a\n")
	gitDir := filepath.Join(r.Dir, ".git")

	t.Run("a state file that is a link", func(t *testing.T) {
		// The link points at a file a merge would have written, so the reading
		// is refused for what it is and not for what it says.
		target := filepath.Join(filepath.Dir(r.Dir), "elsewhere")
		if err := os.WriteFile(target, []byte(strings.Repeat("a", 40)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(gitDir, "MERGE_HEAD")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(link) })
		got, err := ReadInProgress(gitDir)
		if err == nil {
			t.Fatalf("ReadInProgress() = %+v, want the link refused", got)
		}
		if !strings.Contains(err.Error(), "not a plain file") {
			t.Fatalf("ReadInProgress() error = %v, want it to name the link", err)
		}
	})
	t.Run("a state file too large to be one", func(t *testing.T) {
		path := filepath.Join(gitDir, "MERGE_HEAD")
		// The first line is an object name, so the file is refused for its
		// size and not for what it holds.
		big := append([]byte(strings.Repeat("a", 40)+"\n"), make([]byte, maxStateFile)...)
		if err := os.WriteFile(path, big, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(path) })
		_, err := ReadInProgress(gitDir)
		if !errors.Is(err, ErrOutputTooLarge) {
			t.Fatalf("ReadInProgress() error = %v, want %v", err, ErrOutputTooLarge)
		}
	})
	t.Run("a directory of a rebase that is a file", func(t *testing.T) {
		path := filepath.Join(gitDir, "rebase-merge")
		if err := os.WriteFile(path, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(path) })
		got, err := ReadInProgress(gitDir)
		if err != nil {
			t.Fatalf("ReadInProgress() error = %v", err)
		}
		if got.Running() {
			t.Fatalf("ReadInProgress() = %+v, want nothing running", got)
		}
	})
	t.Run("a git directory that does not exist", func(t *testing.T) {
		got, err := ReadInProgress(filepath.Join(r.Dir, "nowhere"))
		if err != nil {
			t.Fatalf("ReadInProgress() error = %v", err)
		}
		if got.Running() {
			t.Fatalf("ReadInProgress() = %+v, want nothing running", got)
		}
	})
}

// isHexOID reports a full object name, which every reading of a repository
// carries.
func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func TestIntegrationWorktreeAdd(t *testing.T) {
	cases := []struct {
		name     string
		req      AddWorktree
		branch   string
		detached bool
	}{
		{name: "a worktree on a branch of its own", req: AddWorktree{Name: "task-a"}, branch: "refs/heads/task-a"},
		{
			name:   "a branch named apart from the directory",
			req:    AddWorktree{Name: "task-b", Branch: "feat/b"},
			branch: "refs/heads/feat/b",
		},
		{
			name:   "a branch starting where another one stands",
			req:    AddWorktree{Name: "task-c", Branch: "feat/c", Start: "side"},
			branch: "refs/heads/feat/c",
		},
		{
			name:   "a branch that already exists",
			req:    AddWorktree{Name: "task-d", Branch: "side", Checkout: true},
			branch: "refs/heads/side",
		},
		{name: "a commit with no branch at all", req: AddWorktree{Name: "task-e", Detach: true, Start: "side"}, detached: true},
	}
	r := history(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Runner.AddWorktree(t.Context(), r.Dir, tc.req)
			if err != nil {
				t.Fatalf("AddWorktree() error = %v", err)
			}
			want := filepath.Join(r.Dir, ".claude", "worktrees", tc.req.Name)
			if got.Path != want {
				t.Fatalf("AddWorktree() opened %q, want %q", got.Path, want)
			}
			if got.Branch != tc.branch || got.Detached != tc.detached {
				t.Fatalf("AddWorktree() = %+v, want branch %q detached=%v", got, tc.branch, tc.detached)
			}
			if info, err := os.Stat(filepath.Join(want, ".git")); err != nil || info.IsDir() {
				t.Fatalf("the worktree at %s holds no git file: %v", want, err)
			}
			if tc.req.Start != "" {
				side := strings.TrimSpace(r.Git("rev-parse", tc.req.Start))
				if got.Head != side {
					t.Fatalf("AddWorktree() opened %s at %s, want %s", tc.req.Name, got.Head, side)
				}
			}
		})
	}
}

func TestIntegrationWorktreeAddRefusals(t *testing.T) {
	r := history(t)
	if _, err := r.Runner.AddWorktree(t.Context(), r.Dir, AddWorktree{Name: "taken"}); err != nil {
		t.Fatalf("AddWorktree() error = %v", err)
	}
	cases := []struct {
		name string
		req  AddWorktree
	}{
		{name: "a name that is a path", req: AddWorktree{Name: "../escape"}},
		{name: "a name that would be an option", req: AddWorktree{Name: "-force"}},
		{name: "a name of nothing", req: AddWorktree{Name: ""}},
		{name: "a branch git refuses", req: AddWorktree{Name: "ok", Branch: "feat/.hidden"}},
		{name: "a branch that would be an option", req: AddWorktree{Name: "ok", Branch: "--upload-pack=id"}},
		{name: "a revision that would be an option", req: AddWorktree{Name: "ok", Start: "--output=/tmp/x"}},
		{name: "a branch checked out and a revision at once", req: AddWorktree{Name: "ok", Branch: "side", Checkout: true, Start: "main"}},
		{name: "a directory already taken", req: AddWorktree{Name: "taken"}},
		{name: "a branch already checked out elsewhere", req: AddWorktree{Name: "again", Branch: "main", Checkout: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Runner.AddWorktree(t.Context(), r.Dir, tc.req)
			if err == nil {
				t.Fatalf("AddWorktree() = %+v, want it refused", got)
			}
		})
	}
}

func TestIntegrationWorktreeRemoveAndPrune(t *testing.T) {
	r := history(t)
	ctx := t.Context()
	if _, err := r.Runner.AddWorktree(ctx, r.Dir, AddWorktree{Name: "clean"}); err != nil {
		t.Fatalf("AddWorktree() error = %v", err)
	}
	held, err := r.Runner.AddWorktree(ctx, r.Dir, AddWorktree{Name: "held"})
	if err != nil {
		t.Fatalf("AddWorktree() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(held.Path, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a worktree with nothing in it", func(t *testing.T) {
		if err := r.Runner.RemoveWorktree(ctx, r.Dir, "clean", false); err != nil {
			t.Fatalf("RemoveWorktree() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, ".claude", "worktrees", "clean")); !os.IsNotExist(err) {
			t.Fatalf("the directory is still there: %v", err)
		}
	})
	t.Run("a worktree holding changes", func(t *testing.T) {
		if err := r.Runner.RemoveWorktree(ctx, r.Dir, "held", false); err == nil {
			t.Fatal("RemoveWorktree() threw the changes away without being asked to")
		}
		if err := r.Runner.RemoveWorktree(ctx, r.Dir, "held", true); err != nil {
			t.Fatalf("RemoveWorktree() error = %v", err)
		}
	})
	t.Run("a worktree that is not one of ours", func(t *testing.T) {
		outside := filepath.Join(filepath.Dir(r.Dir), "outside")
		r.Git("worktree", "add", "-q", outside, "-b", "outside")
		t.Cleanup(func() { r.Git("worktree", "remove", "--force", outside) })
		err := r.Runner.RemoveWorktree(ctx, r.Dir, "outside", false)
		if err == nil {
			t.Fatal("RemoveWorktree() removed a worktree outside the project's own directory")
		}
		if !strings.Contains(err.Error(), "is not a worktree of this project") {
			t.Fatalf("RemoveWorktree() error = %v, want the refusal to come before the command", err)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("the worktree outside the project was touched: %v", err)
		}
	})
	t.Run("a directory that is not a worktree", func(t *testing.T) {
		// Nothing is handed to git that was not read back from git: a
		// directory sitting where a worktree would be is refused here.
		plain := filepath.Join(r.Dir, ".claude", "worktrees", "plain")
		if err := os.MkdirAll(plain, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(plain) })
		err := r.Runner.RemoveWorktree(ctx, r.Dir, "plain", true)
		if err == nil {
			t.Fatal("RemoveWorktree() accepted a directory that is not a worktree")
		}
		if !strings.Contains(err.Error(), "is not a worktree of this project") {
			t.Fatalf("RemoveWorktree() error = %v, want the refusal to come before the command", err)
		}
		if _, err := os.Stat(plain); err != nil {
			t.Fatalf("the directory was touched: %v", err)
		}
	})
	t.Run("a name that is not one", func(t *testing.T) {
		if err := r.Runner.RemoveWorktree(ctx, r.Dir, "../escape", false); err == nil {
			t.Fatal("RemoveWorktree() accepted a name that is a path")
		}
	})
	t.Run("the ones whose directory is gone", func(t *testing.T) {
		w, err := r.Runner.AddWorktree(ctx, r.Dir, AddWorktree{Name: "vanished"})
		if err != nil {
			t.Fatalf("AddWorktree() error = %v", err)
		}
		if err := os.RemoveAll(w.Path); err != nil {
			t.Fatal(err)
		}
		if err := r.Runner.PruneWorktrees(ctx, r.Dir); err != nil {
			t.Fatalf("PruneWorktrees() error = %v", err)
		}
		list, err := r.Runner.Worktrees(ctx, r.Dir)
		if err != nil {
			t.Fatalf("Worktrees() error = %v", err)
		}
		for _, got := range list {
			if got.Name() == "vanished" {
				t.Fatalf("Worktrees() still lists %+v", got)
			}
		}
	})
}

// working builds a working tree holding one of everything the staging
// commands act on: a change, a staged change, a deletion, an untracked file,
// an untracked directory and a file the project ignores.
func working(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.Write(".gitignore", "ignored.txt\n")
	r.Write("kept.txt", "kept\n")
	r.Write("staged.txt", "staged\n")
	r.Write("gone.txt", "gone\n")
	r.Write("sub/deep.txt", "deep\n")
	r.Git("add", ".")
	r.Git("commit", "-qm", "first commit subject")
	r.Write("kept.txt", "changed\n")
	r.Write("staged.txt", "changed\n")
	r.Git("add", "staged.txt")
	r.Git("rm", "-q", "gone.txt")
	r.Write("untracked.txt", "new\n")
	r.Write("new-dir/inside.txt", "new\n")
	r.Write("ignored.txt", "ignored\n")
	return r
}

// state reads the working tree back as a path to its two status codes, which
// is what the view shows and what a staging command has to change.
func state(t *testing.T, r *repo) map[string]string {
	t.Helper()
	ch, err := r.Runner.Changes(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Changes() error = %v", err)
	}
	out := make(map[string]string, len(ch.Files))
	for _, f := range ch.Files {
		out[f.Path] = string([]byte{f.Index, f.Worktree})
	}
	return out
}

func TestIntegrationStaging(t *testing.T) {
	cases := []struct {
		name string
		act  func(t *testing.T, r *repo) error
		want map[string]string
	}{
		{
			name: "one file staged",
			act:  func(t *testing.T, r *repo) error { return r.Runner.Stage(t.Context(), r.Dir, "kept.txt") },
			want: map[string]string{"kept.txt": "M.", "staged.txt": "M.", "gone.txt": "D.", "untracked.txt": "??", "new-dir/": "??"},
		},
		{
			name: "every change staged",
			act:  func(t *testing.T, r *repo) error { return r.Runner.StageAll(t.Context(), r.Dir) },
			want: map[string]string{"kept.txt": "M.", "staged.txt": "M.", "gone.txt": "D.", "untracked.txt": "A.", "new-dir/inside.txt": "A."},
		},
		{
			name: "every change staged from a subdirectory",
			act: func(t *testing.T, r *repo) error {
				return r.Runner.StageAll(t.Context(), filepath.Join(r.Dir, "sub"))
			},
			want: map[string]string{"kept.txt": "M.", "staged.txt": "M.", "gone.txt": "D.", "untracked.txt": "A.", "new-dir/inside.txt": "A."},
		},
		{
			name: "one file taken out of the index",
			act:  func(t *testing.T, r *repo) error { return r.Runner.Unstage(t.Context(), r.Dir, "staged.txt") },
			want: map[string]string{"kept.txt": ".M", "staged.txt": ".M", "gone.txt": "D.", "untracked.txt": "??", "new-dir/": "??"},
		},
		{
			name: "the index emptied",
			act:  func(t *testing.T, r *repo) error { return r.Runner.UnstageAll(t.Context(), r.Dir) },
			want: map[string]string{"kept.txt": ".M", "staged.txt": ".M", "gone.txt": ".D", "untracked.txt": "??", "new-dir/": "??"},
		},
		{
			name: "one change thrown away",
			act:  func(t *testing.T, r *repo) error { return r.Runner.Discard(t.Context(), r.Dir, "kept.txt") },
			want: map[string]string{"staged.txt": "M.", "gone.txt": "D.", "untracked.txt": "??", "new-dir/": "??"},
		},
		{
			name: "every change of the working tree thrown away",
			act: func(t *testing.T, r *repo) error {
				if err := r.Runner.UnstageAll(t.Context(), r.Dir); err != nil {
					return err
				}
				return r.Runner.DiscardAll(t.Context(), r.Dir)
			},
			want: map[string]string{"untracked.txt": "??", "new-dir/": "??"},
		},
		{
			name: "one untracked file deleted",
			act:  func(t *testing.T, r *repo) error { return r.Runner.CleanUntracked(t.Context(), r.Dir, "untracked.txt") },
			want: map[string]string{"kept.txt": ".M", "staged.txt": "M.", "gone.txt": "D.", "new-dir/": "??"},
		},
		{
			name: "every untracked file deleted",
			act:  func(t *testing.T, r *repo) error { return r.Runner.CleanAllUntracked(t.Context(), r.Dir) },
			want: map[string]string{"kept.txt": ".M", "staged.txt": "M.", "gone.txt": "D."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := working(t)
			if err := tc.act(t, r); err != nil {
				t.Fatalf("the command failed: %v", err)
			}
			got := state(t, r)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the working tree reads as %v, want %v", got, tc.want)
			}
			// A file the project ignores is never thrown away.
			if _, err := os.Stat(filepath.Join(r.Dir, "ignored.txt")); err != nil {
				t.Fatalf("the ignored file was deleted: %v", err)
			}
		})
	}
}

func TestIntegrationStagingRefusals(t *testing.T) {
	cases := []struct {
		name string
		act  func(t *testing.T, r *repo) error
	}{
		{name: "staging with no path", act: func(t *testing.T, r *repo) error { return r.Runner.Stage(t.Context(), r.Dir) }},
		{name: "unstaging with no path", act: func(t *testing.T, r *repo) error { return r.Runner.Unstage(t.Context(), r.Dir) }},
		{name: "discarding with no path", act: func(t *testing.T, r *repo) error { return r.Runner.Discard(t.Context(), r.Dir) }},
		{name: "cleaning with no path", act: func(t *testing.T, r *repo) error { return r.Runner.CleanUntracked(t.Context(), r.Dir) }},
		{
			name: "a path leaving the project",
			act:  func(t *testing.T, r *repo) error { return r.Runner.Stage(t.Context(), r.Dir, "../outside.txt") },
		},
		{
			name: "an absolute path",
			act:  func(t *testing.T, r *repo) error { return r.Runner.Discard(t.Context(), r.Dir, "/etc/passwd") },
		},
		{
			name: "a path leaving the project halfway",
			act: func(t *testing.T, r *repo) error {
				return r.Runner.CleanUntracked(t.Context(), r.Dir, "sub/../../outside")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := working(t)
			before := state(t, r)
			if err := tc.act(t, r); err == nil {
				t.Fatal("the command went through, want it refused")
			}
			if after := state(t, r); !reflect.DeepEqual(before, after) {
				t.Fatalf("the working tree changed: %v, was %v", after, before)
			}
		})
	}
}

// TestIntegrationUnstageBeforeTheFirstCommit covers the one state where a
// restore cannot work: there is no commit to restore the index from.
func TestIntegrationUnstageBeforeTheFirstCommit(t *testing.T) {
	r := newRepo(t)
	r.Write("a.txt", "a\n")
	r.Write("b.txt", "b\n")
	r.Git("add", ".")

	if err := r.Runner.Unstage(t.Context(), r.Dir, "a.txt"); err != nil {
		t.Fatalf("Unstage() error = %v", err)
	}
	if got, want := state(t, r), map[string]string{"a.txt": "??", "b.txt": "A."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the working tree reads as %v, want %v", got, want)
	}
	if err := r.Runner.UnstageAll(t.Context(), r.Dir); err != nil {
		t.Fatalf("UnstageAll() error = %v", err)
	}
	if got, want := state(t, r), map[string]string{"a.txt": "??", "b.txt": "??"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the working tree reads as %v, want %v", got, want)
	}
}

func TestIntegrationCommit(t *testing.T) {
	r := newRepo(t)
	ctx := t.Context()
	r.Write("a.txt", "a\n")
	r.Git("add", ".")

	first, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "first commit subject\n\na body of its own\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if !vcs.IsObjectName(first) {
		t.Fatalf("Commit() returned %q", first)
	}
	if got := strings.TrimSpace(r.Git("log", "-1", "--format=%B")); got != "first commit subject\n\na body of its own" {
		t.Fatalf("the message reads as %q", got)
	}

	t.Run("a second commit of what is staged", func(t *testing.T) {
		r.Write("b.txt", "b\n")
		r.Write("c.txt", "c\n")
		r.Git("add", "b.txt")
		second, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "the staged file alone"})
		if err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		if second == first {
			t.Fatal("Commit() wrote nothing")
		}
		if files := r.Git("show", "--name-only", "--format=", "HEAD"); strings.Contains(files, "c.txt") {
			t.Fatalf("the commit holds %q, want the staged file alone", files)
		}
		if got := state(t, r); got["c.txt"] != "??" {
			t.Fatalf("the working tree reads as %v, want the untracked file left alone", got)
		}
	})
	t.Run("a path committed as it stands", func(t *testing.T) {
		r.Write("d.txt", "d\n")
		r.Git("add", "d.txt")
		r.Write("d.txt", "d again\n")
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "one path", Paths: []string{"d.txt"}}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("show", "HEAD:d.txt")); got != "d again" {
			t.Fatalf("the commit holds %q, want the working tree of that path", got)
		}
	})
	t.Run("a message kept while the commit changes", func(t *testing.T) {
		before := strings.TrimSpace(r.Git("log", "-1", "--format=%s"))
		r.Write("e.txt", "e\n")
		r.Git("add", "e.txt")
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Amend: true, Keep: true}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); got != before {
			t.Fatalf("the message reads as %q, want %q", got, before)
		}
		if files := r.Git("show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "e.txt") {
			t.Fatalf("the amended commit holds %q", files)
		}
	})
	t.Run("a message written again", func(t *testing.T) {
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Amend: true, Message: "a better subject"}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); got != "a better subject" {
			t.Fatalf("the message reads as %q", got)
		}
	})
	t.Run("a commit signed off", func(t *testing.T) {
		r.Write("f.txt", "f\n")
		r.Git("add", "f.txt")
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "signed", SignOff: true}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		if got := r.Git("log", "-1", "--format=%B"); !strings.Contains(got, "Signed-off-by: t <t@example.invalid>") {
			t.Fatalf("the message reads as %q", got)
		}
	})
	t.Run("a commit with nothing in it", func(t *testing.T) {
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "nothing staged"}); err == nil {
			t.Fatal("Commit() wrote a commit with nothing staged")
		}
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "nothing staged", AllowEmpty: true}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	})
	t.Run("the hooks of the project", func(t *testing.T) {
		hook := filepath.Join(r.Dir, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(hook) })
		r.Write("g.txt", "g\n")
		r.Git("add", "g.txt")
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "refused by the hook"}); err == nil {
			t.Fatal("Commit() went past a hook that refused it")
		}
		if _, err := r.Runner.Commit(ctx, r.Dir, CommitOptions{Message: "asked for", NoVerify: true}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	})
	t.Run("a message with no editor to open", func(t *testing.T) {
		// An editor that never exits would hang the pane; the environment
		// makes every editor a command that exits at once.
		editor := filepath.Join(filepath.Dir(r.Dir), "editor")
		if err := os.WriteFile(editor, []byte("#!/bin/sh\nsleep 60\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		run := Runner{Environ: func() []string { return append(r.Environ()(), "GIT_EDITOR="+editor, "EDITOR="+editor) }}
		r.Write("h.txt", "h\n")
		r.Git("add", "h.txt")
		if _, err := run.Commit(ctx, r.Dir, CommitOptions{Message: "written from a file"}); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	})
}

func TestIntegrationCommitRefusals(t *testing.T) {
	cases := []struct {
		name string
		opt  CommitOptions
	}{
		{name: "a message of nothing", opt: CommitOptions{Message: "   \n\t\n"}},
		{name: "no message at all", opt: CommitOptions{}},
		{name: "keeping a message without amending", opt: CommitOptions{Keep: true}},
		{name: "keeping a message and writing one", opt: CommitOptions{Amend: true, Keep: true, Message: "both"}},
		{name: "a message holding a NUL byte", opt: CommitOptions{Message: "a\x00b"}},
		{name: "a path leaving the project", opt: CommitOptions{Message: "m", Paths: []string{"../outside"}}},
		{name: "an absolute path", opt: CommitOptions{Message: "m", Paths: []string{"/etc/passwd"}}},
	}
	r := newRepo(t)
	r.Commit("first commit subject", "a.txt", "a\n")
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Runner.Commit(t.Context(), r.Dir, tc.opt); err == nil {
				t.Fatal("Commit() went through, want it refused")
			}
			if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
				t.Fatalf("HEAD moved to %s", got)
			}
		})
	}
}

// TestIntegrationCommitMessageIsNeverOnACommandLine proves the message
// reaches git through a file, and that the file is gone afterwards.
func TestIntegrationCommitMessageIsNeverOnACommandLine(t *testing.T) {
	const secret = "a subject nobody else should read"
	f := &fakeGit{outputs: map[string]Result{
		"commit":    {},
		"rev-parse": {Stdout: []byte(hashA + "\n")},
	}}
	var file string
	got, err := Runner{Executor: ExecutorFunc(func(ctx context.Context, bin string, args, env []string, limit int64) (Result, error) {
		for _, a := range args {
			if strings.Contains(a, secret) {
				t.Fatalf("the message was passed as %q", a)
			}
			if rest, ok := strings.CutPrefix(a, "--file="); ok {
				file = rest
				data, err := os.ReadFile(rest)
				if err != nil {
					t.Fatalf("the message file: %v", err)
				}
				if string(data) != secret {
					t.Fatalf("the message file holds %q", data)
				}
				info, err := os.Stat(rest)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("the message file is %v (%v)", info.Mode().Perm(), err)
				}
			}
		}
		return f.Exec(ctx, bin, args, env, limit)
	})}.Commit(t.Context(), "/repo", CommitOptions{Message: secret})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got != hashA {
		t.Fatalf("Commit() = %q, want %q", got, hashA)
	}
	if file == "" {
		t.Fatal("no message file was passed")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the message file is still there: %v", err)
	}
}

func TestIntegrationStashPush(t *testing.T) {
	cases := []struct {
		name    string
		opt     StashPush
		want    map[string]string
		message string
		files   []string
		wantErr bool
	}{
		{
			name:    "everything tracked",
			opt:     StashPush{Message: "a message of my own"},
			want:    map[string]string{"untracked.txt": "??", "new-dir/": "??"},
			message: "a message of my own",
			files:   []string{"gone.txt", "kept.txt", "staged.txt"},
		},
		{
			name:    "untracked files as well",
			opt:     StashPush{Message: "with untracked", Untracked: true},
			want:    map[string]string{},
			message: "with untracked",
			files:   []string{"gone.txt", "kept.txt", "new-dir/inside.txt", "staged.txt", "untracked.txt"},
		},
		{
			name:    "what is staged, left in the tree",
			opt:     StashPush{Message: "keeping the index", KeepIndex: true},
			want:    map[string]string{"staged.txt": "M.", "gone.txt": "D.", "untracked.txt": "??", "new-dir/": "??"},
			message: "keeping the index",
			files:   []string{"gone.txt", "kept.txt", "staged.txt"},
		},
		{
			name:    "what is staged and nothing else",
			opt:     StashPush{Message: "the staged changes", StagedOnly: true},
			want:    map[string]string{"kept.txt": ".M", "untracked.txt": "??", "new-dir/": "??"},
			message: "the staged changes",
			files:   []string{"gone.txt", "staged.txt"},
		},
		{
			name:    "one path alone",
			opt:     StashPush{Message: "one path", Paths: []string{"kept.txt"}},
			want:    map[string]string{"staged.txt": "M.", "gone.txt": "D.", "untracked.txt": "??", "new-dir/": "??"},
			message: "one path",
			// The entry holds the whole state of the tree; the paths say what
			// is put back to the commit, which is what the tree above shows.
			files: []string{"gone.txt", "kept.txt", "staged.txt"},
		},
		{
			name:    "no message of its own",
			opt:     StashPush{},
			want:    map[string]string{"untracked.txt": "??", "new-dir/": "??"},
			message: "first commit subject",
			files:   []string{"gone.txt", "kept.txt", "staged.txt"},
		},
		{
			name:    "what is staged, and paths beside it",
			opt:     StashPush{StagedOnly: true, Paths: []string{"kept.txt"}},
			wantErr: true,
		},
		{name: "a path leaving the project", opt: StashPush{Paths: []string{"../outside"}}, wantErr: true},
		{name: "a message holding a NUL byte", opt: StashPush{Message: "a\x00b"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := working(t)
			before := state(t, r)
			err := r.Runner.StashPush(t.Context(), r.Dir, tc.opt)
			if tc.wantErr {
				if err == nil {
					t.Fatal("StashPush() went through, want it refused")
				}
				if after := state(t, r); !reflect.DeepEqual(before, after) {
					t.Fatalf("the working tree changed: %v, was %v", after, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("StashPush() error = %v", err)
			}
			if got := state(t, r); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the working tree reads as %v, want %v", got, tc.want)
			}
			list, err := r.Runner.Stashes(t.Context(), r.Dir)
			if err != nil {
				t.Fatalf("Stashes() error = %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("Stashes() = %+v, want the one just pushed", list)
			}
			if !strings.Contains(list[0].Message, tc.message) {
				t.Fatalf("the entry reads as %q, want it to say %q", list[0].Message, tc.message)
			}
			files, err := r.Runner.StashFiles(t.Context(), r.Dir, 0)
			if err != nil {
				t.Fatalf("StashFiles() error = %v", err)
			}
			paths := make([]string, len(files))
			for i, f := range files {
				paths[i] = f.Path
			}
			slices.Sort(paths)
			if !slices.Equal(paths, tc.files) {
				t.Fatalf("StashFiles() = %v, want %v", paths, tc.files)
			}
		})
	}
}

func TestIntegrationStashApplyPopAndDrop(t *testing.T) {
	ctx := t.Context()
	t.Run("an entry taken back and kept in the list", func(t *testing.T) {
		r := working(t)
		want := state(t, r)
		if err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "put away"}); err != nil {
			t.Fatalf("StashPush() error = %v", err)
		}
		if err := r.Runner.StashApply(ctx, r.Dir, 0, true); err != nil {
			t.Fatalf("StashApply() error = %v", err)
		}
		if got := state(t, r); !reflect.DeepEqual(got, want) {
			t.Fatalf("the working tree reads as %v, want %v", got, want)
		}
		list, err := r.Runner.Stashes(ctx, r.Dir)
		if err != nil || len(list) != 1 {
			t.Fatalf("Stashes() = %+v (%v), want the entry kept", list, err)
		}
	})
	t.Run("an entry taken back and removed", func(t *testing.T) {
		r := working(t)
		if err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "put away"}); err != nil {
			t.Fatalf("StashPush() error = %v", err)
		}
		if err := r.Runner.StashPop(ctx, r.Dir, 0, false); err != nil {
			t.Fatalf("StashPop() error = %v", err)
		}
		list, err := r.Runner.Stashes(ctx, r.Dir)
		if err != nil || len(list) != 0 {
			t.Fatalf("Stashes() = %+v (%v), want the list empty", list, err)
		}
		// Without the index restored, what was staged comes back as a change
		// of the working tree.
		if got := state(t, r)["staged.txt"]; got != ".M" {
			t.Fatalf("staged.txt reads as %q, want a change of the working tree", got)
		}
	})
	t.Run("an entry dropped", func(t *testing.T) {
		r := working(t)
		if err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "first away"}); err != nil {
			t.Fatalf("StashPush() error = %v", err)
		}
		r.Write("kept.txt", "again\n")
		if err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "second away"}); err != nil {
			t.Fatalf("StashPush() error = %v", err)
		}
		if err := r.Runner.StashDrop(ctx, r.Dir, 0); err != nil {
			t.Fatalf("StashDrop() error = %v", err)
		}
		list, err := r.Runner.Stashes(ctx, r.Dir)
		if err != nil || len(list) != 1 || !strings.Contains(list[0].Message, "first away") {
			t.Fatalf("Stashes() = %+v (%v), want the older entry alone", list, err)
		}
	})
	t.Run("the list cleared", func(t *testing.T) {
		r := working(t)
		if err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "away"}); err != nil {
			t.Fatalf("StashPush() error = %v", err)
		}
		if err := r.Runner.StashClear(ctx, r.Dir); err != nil {
			t.Fatalf("StashClear() error = %v", err)
		}
		list, err := r.Runner.Stashes(ctx, r.Dir)
		if err != nil || len(list) != 0 {
			t.Fatalf("Stashes() = %+v (%v), want the list empty", list, err)
		}
	})
	t.Run("an entry that is not there", func(t *testing.T) {
		r := working(t)
		for _, entry := range []int{-1, vcs.MaxStashes, 3} {
			if err := r.Runner.StashDrop(ctx, r.Dir, entry); err == nil {
				t.Fatalf("StashDrop(%d) went through, want it refused", entry)
			}
		}
	})
	t.Run("a push with nothing to put away", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "nothing"})
		if !errors.Is(err, ErrNothingToStash) {
			t.Fatalf("StashPush() error = %v, want %v", err, ErrNothingToStash)
		}
	})
	t.Run("an entry that cannot be taken back", func(t *testing.T) {
		r := working(t)
		if err := r.Runner.StashPush(ctx, r.Dir, StashPush{Message: "away"}); err != nil {
			t.Fatalf("StashPush() error = %v", err)
		}
		r.Write("kept.txt", "something else\n")
		if err := r.Runner.StashApply(ctx, r.Dir, 0, false); err == nil {
			t.Fatal("StashApply() went through over a change of its own")
		}
		list, err := r.Runner.Stashes(ctx, r.Dir)
		if err != nil || len(list) != 1 {
			t.Fatalf("Stashes() = %+v (%v), want the entry kept", list, err)
		}
	})
}

// head reads what the working tree stands on: the branch, or the empty string
// when HEAD is detached.
func head(t *testing.T, r *repo) string {
	t.Helper()
	out, err := r.Try("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func TestIntegrationCheckout(t *testing.T) {
	ctx := t.Context()
	t.Run("a branch that is already there", func(t *testing.T) {
		r := history(t)
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Branch: "side"}); err != nil {
			t.Fatalf("Checkout() error = %v", err)
		}
		if got := head(t, r); got != "side" {
			t.Fatalf("the working tree stands on %q, want side", got)
		}
	})
	t.Run("a branch created where another one stands", func(t *testing.T) {
		r := history(t)
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Branch: "feat/new", New: true, Start: "side"}); err != nil {
			t.Fatalf("Checkout() error = %v", err)
		}
		if got := head(t, r); got != "feat/new" {
			t.Fatalf("the working tree stands on %q, want feat/new", got)
		}
		if got, want := r.Git("rev-parse", "HEAD"), r.Git("rev-parse", "side"); got != want {
			t.Fatalf("the branch starts at %s, want %s", got, want)
		}
	})
	t.Run("a branch of a remote followed", func(t *testing.T) {
		r := history(t)
		remote := filepath.Join(filepath.Dir(r.Dir), "origin.git")
		r.Git("init", "-q", "--bare", remote)
		r.Git("remote", "add", "origin", remote)
		r.Git("push", "-q", "origin", "side:theirs")
		r.Git("fetch", "-q", "origin")
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Branch: "theirs", New: true, Track: "origin/theirs"}); err != nil {
			t.Fatalf("Checkout() error = %v", err)
		}
		branches, err := r.Runner.Branches(ctx, r.Dir)
		if err != nil {
			t.Fatalf("Branches() error = %v", err)
		}
		var found bool
		for _, b := range branches {
			if b.Name == "theirs" {
				found = true
				if b.Upstream != "refs/remotes/origin/theirs" {
					t.Fatalf("the branch follows %q", b.Upstream)
				}
			}
		}
		if !found {
			t.Fatalf("Branches() = %+v, want the branch that was opened", branches)
		}
	})
	t.Run("a branch of a remote is never guessed", func(t *testing.T) {
		r := history(t)
		remote := filepath.Join(filepath.Dir(r.Dir), "guess.git")
		r.Git("init", "-q", "--bare", remote)
		r.Git("remote", "add", "origin", remote)
		r.Git("push", "-q", "origin", "side:theirs")
		r.Git("fetch", "-q", "origin")
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Branch: "theirs"}); err == nil {
			t.Fatal("Checkout() opened a branch that was never asked for")
		}
	})
	t.Run("a commit with no branch on it", func(t *testing.T) {
		r := history(t)
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Detach: true, Start: "side"}); err != nil {
			t.Fatalf("Checkout() error = %v", err)
		}
		if got := head(t, r); got != "" {
			t.Fatalf("the working tree stands on %q, want no branch at all", got)
		}
		if got, want := r.Git("rev-parse", "HEAD"), r.Git("rev-parse", "side"); got != want {
			t.Fatalf("HEAD stands at %s, want %s", got, want)
		}
	})
	t.Run("changes standing in the way", func(t *testing.T) {
		r := history(t)
		// a.txt differs between the two branches, so a change to it is in the
		// way of the switch rather than carried over by it.
		r.Write("a.txt", "changed in the way\n")
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Branch: "side"}); err == nil {
			t.Fatal("Checkout() threw a change away without being asked to")
		}
		if err := r.Runner.Checkout(ctx, r.Dir, Checkout{Branch: "side", Discard: true}); err != nil {
			t.Fatalf("Checkout() error = %v", err)
		}
		if got := state(t, r); len(got) != 0 {
			t.Fatalf("the working tree reads as %v, want it clean", got)
		}
	})
}

func TestIntegrationBranchActions(t *testing.T) {
	ctx := t.Context()
	t.Run("a branch created and renamed", func(t *testing.T) {
		r := history(t)
		if err := r.Runner.CreateBranch(ctx, r.Dir, "feat/one", "side"); err != nil {
			t.Fatalf("CreateBranch() error = %v", err)
		}
		if got := head(t, r); got != "main" {
			t.Fatalf("the working tree moved to %q", got)
		}
		if err := r.Runner.RenameBranch(ctx, r.Dir, "feat/one", "feat/two", false); err != nil {
			t.Fatalf("RenameBranch() error = %v", err)
		}
		names := branchNames(t, r)
		if slices.Contains(names, "feat/one") || !slices.Contains(names, "feat/two") {
			t.Fatalf("the branches read as %v", names)
		}
	})
	t.Run("a rename onto a name that is taken", func(t *testing.T) {
		r := history(t)
		if err := r.Runner.CreateBranch(ctx, r.Dir, "one", ""); err != nil {
			t.Fatalf("CreateBranch() error = %v", err)
		}
		if err := r.Runner.RenameBranch(ctx, r.Dir, "one", "side", false); err == nil {
			t.Fatal("RenameBranch() wrote over a branch that was there")
		}
		if err := r.Runner.RenameBranch(ctx, r.Dir, "one", "side", true); err != nil {
			t.Fatalf("RenameBranch() error = %v", err)
		}
	})
	t.Run("a branch deleted", func(t *testing.T) {
		r := history(t)
		if err := r.Runner.CreateBranch(ctx, r.Dir, "merged", "main"); err != nil {
			t.Fatalf("CreateBranch() error = %v", err)
		}
		if err := r.Runner.DeleteBranch(ctx, r.Dir, "merged", false); err != nil {
			t.Fatalf("DeleteBranch() error = %v", err)
		}
		if names := branchNames(t, r); slices.Contains(names, "merged") {
			t.Fatalf("the branches read as %v", names)
		}
	})
	t.Run("a branch holding commits of its own", func(t *testing.T) {
		r := history(t)
		r.Git("checkout", "-q", "-b", "alone", "main")
		r.Commit("a commit no other branch holds", "alone.txt", "a\n")
		r.Git("checkout", "-q", "main")
		if err := r.Runner.DeleteBranch(ctx, r.Dir, "alone", false); err == nil {
			t.Fatal("DeleteBranch() threw commits away without being asked to")
		}
		if err := r.Runner.DeleteBranch(ctx, r.Dir, "alone", true); err != nil {
			t.Fatalf("DeleteBranch() error = %v", err)
		}
	})
	t.Run("a branch that follows one of a remote", func(t *testing.T) {
		r := history(t)
		remote := filepath.Join(filepath.Dir(r.Dir), "origin.git")
		r.Git("init", "-q", "--bare", remote)
		r.Git("remote", "add", "origin", remote)
		r.Git("push", "-q", "origin", "main")
		r.Git("fetch", "-q", "origin")
		if err := r.Runner.SetUpstream(ctx, r.Dir, "main", "origin/main"); err != nil {
			t.Fatalf("SetUpstream() error = %v", err)
		}
		if got := upstreamOf(t, r, "main"); got != "refs/remotes/origin/main" {
			t.Fatalf("main follows %q", got)
		}
		if err := r.Runner.UnsetUpstream(ctx, r.Dir, "main"); err != nil {
			t.Fatalf("UnsetUpstream() error = %v", err)
		}
		if got := upstreamOf(t, r, "main"); got != "" {
			t.Fatalf("main follows %q, want nothing", got)
		}
	})
}

func branchNames(t *testing.T, r *repo) []string {
	t.Helper()
	list, err := r.Runner.Branches(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	names := make([]string, len(list))
	for i, b := range list {
		names[i] = b.Name
	}
	return names
}

func upstreamOf(t *testing.T, r *repo, branch string) string {
	t.Helper()
	list, err := r.Runner.Branches(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	for _, b := range list {
		if b.Name == branch {
			return b.Upstream
		}
	}
	t.Fatalf("Branches() = %+v, want %s among them", list, branch)
	return ""
}

func TestIntegrationMerge(t *testing.T) {
	ctx := t.Context()
	t.Run("a branch that only moves forward", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		r.Git("checkout", "-q", "-b", "side")
		r.Commit("a change of the branch", "side.txt", "s\n")
		r.Git("checkout", "-q", "main")
		if err := r.Runner.Merge(ctx, r.Dir, Merge{Rev: "side", FastForwardOnly: true}); err != nil {
			t.Fatalf("Merge() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); got != "a change of the branch" {
			t.Fatalf("the branch stands at %q, want the commit of the branch", got)
		}
	})
	t.Run("a merge commit with a message of its own", func(t *testing.T) {
		r := diverged(t)
		if err := r.Runner.Merge(ctx, r.Dir, Merge{Rev: "side", NoFastForward: true, Message: "a merge of my own"}); err != nil {
			t.Fatalf("Merge() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); got != "a merge of my own" {
			t.Fatalf("the merge reads as %q", got)
		}
		if parents := strings.Fields(r.Git("log", "-1", "--format=%P")); len(parents) != 2 {
			t.Fatalf("the merge holds %d parents", len(parents))
		}
	})
	t.Run("a merge that cannot only move forward", func(t *testing.T) {
		r := diverged(t)
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		if err := r.Runner.Merge(ctx, r.Dir, Merge{Rev: "side", FastForwardOnly: true}); err == nil {
			t.Fatal("Merge() wrote a merge although only moving forward was asked for")
		}
		if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
			t.Fatalf("HEAD moved to %s", got)
		}
	})
	t.Run("the changes brought in and left staged", func(t *testing.T) {
		r := diverged(t)
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		if err := r.Runner.Merge(ctx, r.Dir, Merge{Rev: "side", Squash: true}); err != nil {
			t.Fatalf("Merge() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
			t.Fatalf("a squash wrote a commit at %s", got)
		}
		if got := state(t, r)["side.txt"]; got != "A." {
			t.Fatalf("the working tree reads as %v, want the change staged", state(t, r))
		}
	})
	t.Run("a merge stopped before the commit", func(t *testing.T) {
		r := diverged(t)
		if err := r.Runner.Merge(ctx, r.Dir, Merge{Rev: "side", NoCommit: true}); err != nil {
			t.Fatalf("Merge() error = %v", err)
		}
		got, err := r.Runner.InProgress(ctx, r.Dir)
		if err != nil {
			t.Fatalf("InProgress() error = %v", err)
		}
		if got.Kind != vcs.OperationMerge {
			t.Fatalf("InProgress() = %+v, want a merge waiting", got)
		}
	})
	t.Run("a merge and a fast forward at once", func(t *testing.T) {
		r := diverged(t)
		if err := r.Runner.Merge(ctx, r.Dir, Merge{Rev: "side", NoFastForward: true, FastForwardOnly: true}); err == nil {
			t.Fatal("Merge() accepted two answers to the same question")
		}
	})
}

// diverged is a repository whose branch and project each hold a commit the
// other has not got, with no conflict between them.
func diverged(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.Commit("first commit subject", "a.txt", "a\n")
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("a change of the branch", "side.txt", "s\n")
	r.Git("checkout", "-q", "main")
	r.Commit("a change of the project", "main.txt", "m\n")
	return r
}

func TestIntegrationRebase(t *testing.T) {
	ctx := t.Context()
	t.Run("a branch replayed over the project", func(t *testing.T) {
		r := diverged(t)
		r.Git("checkout", "-q", "side")
		if err := r.Runner.Rebase(ctx, r.Dir, Rebase{Upstream: "main"}); err != nil {
			t.Fatalf("Rebase() error = %v", err)
		}
		subjects := strings.Fields(strings.ReplaceAll(r.Git("log", "--format=%f"), "\n", " "))
		want := []string{"a-change-of-the-branch", "a-change-of-the-project", "first-commit-subject"}
		if !slices.Equal(subjects, want) {
			t.Fatalf("the branch reads as %v, want %v", subjects, want)
		}
	})
	t.Run("a branch replayed without leaving the one it stands on", func(t *testing.T) {
		r := diverged(t)
		if err := r.Runner.Rebase(ctx, r.Dir, Rebase{Upstream: "main", Branch: "side"}); err != nil {
			t.Fatalf("Rebase() error = %v", err)
		}
		if got := head(t, r); got != "side" {
			t.Fatalf("the working tree stands on %q, want the branch that was replayed", got)
		}
	})
	t.Run("a branch moved off the commits it was built on", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		r.Git("checkout", "-q", "-b", "feature")
		r.Commit("a change of the feature", "f.txt", "f\n")
		r.Git("checkout", "-q", "-b", "on-top")
		r.Commit("a change on top", "t.txt", "t\n")
		if err := r.Runner.Rebase(ctx, r.Dir, Rebase{Upstream: "feature", Onto: "main"}); err != nil {
			t.Fatalf("Rebase() error = %v", err)
		}
		subjects := strings.Fields(strings.ReplaceAll(r.Git("log", "--format=%f"), "\n", " "))
		want := []string{"a-change-on-top", "first-commit-subject"}
		if !slices.Equal(subjects, want) {
			t.Fatalf("the branch reads as %v, want %v", subjects, want)
		}
	})
	t.Run("the changes of the working tree carried across", func(t *testing.T) {
		r := diverged(t)
		r.Git("checkout", "-q", "side")
		r.Write("side.txt", "changed while replaying\n")
		if err := r.Runner.Rebase(ctx, r.Dir, Rebase{Upstream: "main"}); err == nil {
			t.Fatal("Rebase() ran over a working tree holding changes")
		}
		if err := r.Runner.Rebase(ctx, r.Dir, Rebase{Upstream: "main", AutoStash: true}); err != nil {
			t.Fatalf("Rebase() error = %v", err)
		}
		if got := state(t, r)["side.txt"]; got != ".M" {
			t.Fatalf("the working tree reads as %v, want the change back", state(t, r))
		}
	})
}

func TestIntegrationContinueSkipAndAbort(t *testing.T) {
	ctx := t.Context()
	t.Run("a merge given up", func(t *testing.T) {
		r := conflicting(t)
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		mustConflict(t, r, "merge", "side")
		if err := r.Runner.Abort(ctx, r.Dir); err != nil {
			t.Fatalf("Abort() error = %v", err)
		}
		got, err := r.Runner.InProgress(ctx, r.Dir)
		if err != nil {
			t.Fatalf("InProgress() error = %v", err)
		}
		if got.Running() {
			t.Fatalf("InProgress() = %+v, want nothing running", got)
		}
		if now := strings.TrimSpace(r.Git("rev-parse", "HEAD")); now != head {
			t.Fatalf("HEAD stands at %s, want %s", now, head)
		}
	})
	t.Run("a merge carried on once the conflict is resolved", func(t *testing.T) {
		r := conflicting(t)
		mustConflict(t, r, "merge", "side")
		r.Write("a.txt", "resolved\n")
		r.Git("add", "a.txt")
		if err := r.Runner.Continue(ctx, r.Dir); err != nil {
			t.Fatalf("Continue() error = %v", err)
		}
		if parents := strings.Fields(r.Git("log", "-1", "--format=%P")); len(parents) != 2 {
			t.Fatalf("the merge holds %d parents", len(parents))
		}
	})
	t.Run("a merge is never skipped", func(t *testing.T) {
		r := conflicting(t)
		mustConflict(t, r, "merge", "side")
		if err := r.Runner.Skip(ctx, r.Dir); err == nil {
			t.Fatal("Skip() answered a merge with a word it has not got")
		}
	})
	t.Run("a rebase carried on", func(t *testing.T) {
		r := conflicting(t)
		r.Git("checkout", "-q", "side")
		mustConflict(t, r, "rebase", "main")
		r.Write("a.txt", "resolved\n")
		r.Git("add", "a.txt")
		if err := r.Runner.Continue(ctx, r.Dir); err != nil {
			t.Fatalf("Continue() error = %v", err)
		}
		got, err := r.Runner.InProgress(ctx, r.Dir)
		if err != nil {
			t.Fatalf("InProgress() error = %v", err)
		}
		if got.Running() {
			t.Fatalf("InProgress() = %+v, want the rebase through", got)
		}
	})
	t.Run("a commit left out of a rebase", func(t *testing.T) {
		r := conflicting(t)
		r.Git("checkout", "-q", "side")
		mustConflict(t, r, "rebase", "main")
		if err := r.Runner.Skip(ctx, r.Dir); err != nil {
			t.Fatalf("Skip() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); got != "a change of the project" {
			t.Fatalf("the branch stands at %q, want the commit that was kept", got)
		}
	})
	t.Run("a cherry pick given up", func(t *testing.T) {
		r := conflicting(t)
		mustConflict(t, r, "cherry-pick", "side")
		if err := r.Runner.Abort(ctx, r.Dir); err != nil {
			t.Fatalf("Abort() error = %v", err)
		}
		if got := state(t, r); len(got) != 0 {
			t.Fatalf("the working tree reads as %v, want it clean", got)
		}
	})
	t.Run("an answer with nothing running", func(t *testing.T) {
		r := conflicting(t)
		for name, act := range map[string]func() error{
			"continue": func() error { return r.Runner.Continue(ctx, r.Dir) },
			"skip":     func() error { return r.Runner.Skip(ctx, r.Dir) },
			"abort":    func() error { return r.Runner.Abort(ctx, r.Dir) },
		} {
			if err := act(); err == nil {
				t.Fatalf("%s() answered an operation that is not running", name)
			}
		}
	})
}

func TestIntegrationCherryPickAndRevert(t *testing.T) {
	ctx := t.Context()
	t.Run("a commit replayed onto the branch", func(t *testing.T) {
		r := diverged(t)
		if err := r.Runner.CherryPick(ctx, r.Dir, Pick{Revs: []string{"side"}}); err != nil {
			t.Fatalf("CherryPick() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); got != "a change of the branch" {
			t.Fatalf("the branch stands at %q", got)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "side.txt")); err != nil {
			t.Fatalf("the change was not replayed: %v", err)
		}
	})
	t.Run("two commits folded into what is staged", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		r.Git("checkout", "-q", "-b", "side")
		r.Commit("one", "one.txt", "1\n")
		r.Commit("two", "two.txt", "2\n")
		r.Git("checkout", "-q", "main")
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		if err := r.Runner.CherryPick(ctx, r.Dir, Pick{Revs: []string{"side~1", "side"}, NoCommit: true}); err != nil {
			t.Fatalf("CherryPick() error = %v", err)
		}
		if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
			t.Fatalf("a pick that writes no commit moved HEAD to %s", got)
		}
		got := state(t, r)
		if got["one.txt"] != "A." || got["two.txt"] != "A." {
			t.Fatalf("the working tree reads as %v, want both changes staged", got)
		}
	})
	t.Run("a commit replayed with where it came from", func(t *testing.T) {
		r := diverged(t)
		source := strings.TrimSpace(r.Git("rev-parse", "side"))
		if err := r.Runner.CherryPick(ctx, r.Dir, Pick{Revs: []string{"side"}, Reference: true}); err != nil {
			t.Fatalf("CherryPick() error = %v", err)
		}
		if got := r.Git("log", "-1", "--format=%B"); !strings.Contains(got, source) {
			t.Fatalf("the message reads as %q, want it to name %s", got, source)
		}
	})
	t.Run("a commit undone", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		r.Commit("a change to undo", "b.txt", "b\n")
		if err := r.Runner.Revert(ctx, r.Dir, Pick{Revs: []string{"HEAD"}}); err != nil {
			t.Fatalf("Revert() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "b.txt")); !os.IsNotExist(err) {
			t.Fatalf("the change was not undone: %v", err)
		}
		if got := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); !strings.Contains(got, "a change to undo") {
			t.Fatalf("the message reads as %q, want it to name the commit undone", got)
		}
	})
	t.Run("a merge undone against the branch it was merged into", func(t *testing.T) {
		r := diverged(t)
		r.Git("merge", "-q", "--no-ff", "-m", "a merge commit", "side")
		if err := r.Runner.Revert(ctx, r.Dir, Pick{Revs: []string{"HEAD"}}); err == nil {
			t.Fatal("Revert() undid a merge without being told which side to keep")
		}
		if err := r.Runner.Revert(ctx, r.Dir, Pick{Revs: []string{"HEAD"}, Mainline: 1}); err != nil {
			t.Fatalf("Revert() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "side.txt")); !os.IsNotExist(err) {
			t.Fatalf("the merge was not undone: %v", err)
		}
	})
	t.Run("a pick that stops on a conflict", func(t *testing.T) {
		r := conflicting(t)
		if err := r.Runner.CherryPick(ctx, r.Dir, Pick{Revs: []string{"side"}}); err == nil {
			t.Fatal("CherryPick() went through a conflict")
		}
		got, err := r.Runner.InProgress(ctx, r.Dir)
		if err != nil {
			t.Fatalf("InProgress() error = %v", err)
		}
		if got.Kind != vcs.OperationCherryPick {
			t.Fatalf("InProgress() = %+v, want the pick waiting", got)
		}
	})
	t.Run("the refusals", func(t *testing.T) {
		r := diverged(t)
		cases := []struct {
			name string
			act  func() error
		}{
			{name: "a pick of nothing", act: func() error { return r.Runner.CherryPick(ctx, r.Dir, Pick{}) }},
			{name: "a revert of nothing", act: func() error { return r.Runner.Revert(ctx, r.Dir, Pick{}) }},
			{
				name: "a revision that would be an option",
				act:  func() error { return r.Runner.CherryPick(ctx, r.Dir, Pick{Revs: []string{"--exec=id"}}) },
			},
			{
				name: "a parent counting backwards",
				act:  func() error { return r.Runner.Revert(ctx, r.Dir, Pick{Revs: []string{"HEAD"}, Mainline: -1}) },
			},
			{
				name: "a parent past what a merge holds",
				act:  func() error { return r.Runner.CherryPick(ctx, r.Dir, Pick{Revs: []string{"HEAD"}, Mainline: 99}) },
			},
			{
				name: "a revert recording where it came from",
				act:  func() error { return r.Runner.Revert(ctx, r.Dir, Pick{Revs: []string{"HEAD"}, Reference: true}) },
			},
		}
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.act(); err == nil {
					t.Fatal("the command went through, want it refused")
				}
				if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
					t.Fatalf("HEAD moved to %s", got)
				}
			})
		}
	})
}

func TestIntegrationReset(t *testing.T) {
	cases := []struct {
		name string
		mode ResetMode
		want map[string]string
	}{
		{name: "the branch alone", mode: ResetSoft, want: map[string]string{"b.txt": "A.", "c.txt": "A."}},
		{name: "the branch and the index", mode: ResetMixed, want: map[string]string{"b.txt": "??", "c.txt": "??"}},
		{name: "the branch, the index and the working tree", mode: ResetHard, want: map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			r.Commit("first commit subject", "a.txt", "a\n")
			first := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
			r.Commit("a second commit", "b.txt", "b\n")
			r.Write("c.txt", "c\n")
			r.Git("add", "c.txt")

			if err := r.Runner.Reset(t.Context(), r.Dir, first, tc.mode); err != nil {
				t.Fatalf("Reset() error = %v", err)
			}
			if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != first {
				t.Fatalf("the branch stands at %s, want %s", got, first)
			}
			if got := state(t, r); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the working tree reads as %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("a revision that would be an option", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		if err := r.Runner.Reset(t.Context(), r.Dir, "--hard", ResetSoft); err == nil {
			t.Fatal("Reset() accepted a revision git would read as an option")
		}
	})
	t.Run("a mode this does not know", func(t *testing.T) {
		r := newRepo(t)
		r.Commit("first commit subject", "a.txt", "a\n")
		if err := r.Runner.Reset(t.Context(), r.Dir, "HEAD", ResetMode(42)); err == nil {
			t.Fatal("Reset() accepted a mode it has not got")
		}
	})
}

func TestResetModeString(t *testing.T) {
	cases := []struct {
		mode ResetMode
		want string
	}{
		{ResetMixed, "mixed"},
		{ResetSoft, "soft"},
		{ResetHard, "hard"},
		{ResetMode(42), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.mode.String(); got != tc.want {
				t.Fatalf("ResetMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

func TestIntegrationTags(t *testing.T) {
	ctx := t.Context()
	r := history(t)
	t.Run("a tag that is only a ref", func(t *testing.T) {
		if err := r.Runner.Tag(ctx, r.Dir, Tag{Name: "light", Rev: "main~1"}); err != nil {
			t.Fatalf("Tag() error = %v", err)
		}
		got := tagNamed(t, r, "light")
		if got.Annotated || got.OID != got.Commit {
			t.Fatalf("the tag reads as %+v, want one that is only a ref", got)
		}
		if want := strings.TrimSpace(r.Git("rev-parse", "main~1")); got.Commit != want {
			t.Fatalf("the tag names %s, want %s", got.Commit, want)
		}
	})
	t.Run("a tag carrying a message", func(t *testing.T) {
		if err := r.Runner.Tag(ctx, r.Dir, Tag{Name: "v1.0.0", Message: "the first release\n\nwith a body\n"}); err != nil {
			t.Fatalf("Tag() error = %v", err)
		}
		got := tagNamed(t, r, "v1.0.0")
		if !got.Annotated || got.Subject != "the first release" {
			t.Fatalf("the tag reads as %+v", got)
		}
		if body := r.Git("tag", "-l", "--format=%(contents)", "v1.0.0"); !strings.Contains(body, "with a body") {
			t.Fatalf("the message reads as %q", body)
		}
	})
	t.Run("a tag written over another", func(t *testing.T) {
		if err := r.Runner.Tag(ctx, r.Dir, Tag{Name: "light", Rev: "main"}); err == nil {
			t.Fatal("Tag() wrote over a tag that was there")
		}
		if err := r.Runner.Tag(ctx, r.Dir, Tag{Name: "light", Rev: "main", Force: true}); err != nil {
			t.Fatalf("Tag() error = %v", err)
		}
		if got, want := tagNamed(t, r, "light").Commit, strings.TrimSpace(r.Git("rev-parse", "main")); got != want {
			t.Fatalf("the tag names %s, want %s", got, want)
		}
	})
	t.Run("a tag removed", func(t *testing.T) {
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		if err := r.Runner.DeleteTag(ctx, r.Dir, "light"); err != nil {
			t.Fatalf("DeleteTag() error = %v", err)
		}
		list, err := r.Runner.Tags(ctx, r.Dir)
		if err != nil {
			t.Fatalf("Tags() error = %v", err)
		}
		for _, tag := range list {
			if tag.Name == "light" {
				t.Fatalf("Tags() still lists %+v", tag)
			}
		}
		if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
			t.Fatalf("the commit moved to %s", got)
		}
	})
	t.Run("the refusals", func(t *testing.T) {
		cases := []struct {
			name string
			act  func() error
		}{
			{name: "a name that would be an option", act: func() error { return r.Runner.Tag(ctx, r.Dir, Tag{Name: "-f"}) }},
			{name: "a name git refuses", act: func() error { return r.Runner.Tag(ctx, r.Dir, Tag{Name: "feat/.x"}) }},
			{name: "a name of nothing", act: func() error { return r.Runner.Tag(ctx, r.Dir, Tag{}) }},
			{
				name: "a revision that would be an option",
				act:  func() error { return r.Runner.Tag(ctx, r.Dir, Tag{Name: "ok", Rev: "--exec=id"}) },
			},
			{name: "a tag removed by a name that is none", act: func() error { return r.Runner.DeleteTag(ctx, r.Dir, "-d") }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.act(); err == nil {
					t.Fatal("the command went through, want it refused")
				}
			})
		}
	})
}

func tagNamed(t *testing.T, r *repo, name string) vcs.Tag {
	t.Helper()
	list, err := r.Runner.Tags(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Tags() error = %v", err)
	}
	for _, tag := range list {
		if tag.Name == name {
			return tag
		}
	}
	t.Fatalf("Tags() = %+v, want %s among them", list, name)
	return vcs.Tag{}
}

func TestIntegrationFormatPatch(t *testing.T) {
	ctx := t.Context()
	r := history(t)
	out := filepath.Join(filepath.Dir(r.Dir), "patches")
	if err := os.Mkdir(out, 0o750); err != nil {
		t.Fatal(err)
	}

	t.Run("a range of commits", func(t *testing.T) {
		files, err := r.Runner.FormatPatch(ctx, r.Dir, Patch{Revs: []string{"main~2..main"}, Dir: out})
		if err != nil {
			t.Fatalf("FormatPatch() error = %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("FormatPatch() wrote %v, want two files", files)
		}
		for _, f := range files {
			if filepath.Dir(f) != out {
				t.Fatalf("FormatPatch() wrote %s, outside %s", f, out)
			}
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "diff --git") {
				t.Fatalf("%s holds no patch", f)
			}
		}
	})
	t.Run("what the branch holds that its upstream has not got", func(t *testing.T) {
		remote := filepath.Join(filepath.Dir(r.Dir), "origin.git")
		r.Git("init", "-q", "--bare", remote)
		r.Git("remote", "add", "origin", remote)
		r.Git("push", "-q", "-u", "origin", "main~1:refs/heads/main")
		r.Git("branch", "--set-upstream-to=origin/main", "main")
		files, err := r.Runner.FormatPatch(ctx, r.Dir, Patch{Dir: out, Numbered: true})
		if err != nil {
			t.Fatalf("FormatPatch() error = %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("FormatPatch() wrote %v, want the one commit that is not pushed", files)
		}
	})
	t.Run("the refusals", func(t *testing.T) {
		cases := []struct {
			name string
			p    Patch
		}{
			{name: "a directory that is not there", p: Patch{Dir: filepath.Join(out, "nowhere"), Revs: []string{"main"}}},
			{name: "a path that is a file", p: Patch{Dir: filepath.Join(r.Dir, "a.txt"), Revs: []string{"main"}}},
			{name: "a path that is relative", p: Patch{Dir: "patches", Revs: []string{"main"}}},
			{name: "a directory of nothing", p: Patch{Revs: []string{"main"}}},
			{name: "a revision that would be an option", p: Patch{Dir: out, Revs: []string{"--exec=id"}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got, err := r.Runner.FormatPatch(ctx, r.Dir, tc.p); err == nil {
					t.Fatalf("FormatPatch() = %v, want it refused", got)
				}
			})
		}
	})
}

// lmuxBin builds the binary git runs as the sequence editor, once per test
// run, so the history edits are driven the way they are in a workspace.
var lmuxBin = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "lmux-bin-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "lmux")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/bayoudhdev/lyna-claude-tmux/cmd/lmux").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("building lmux: %w\n%s", err, out)
	}
	return bin, nil
})

func sequenceEditorBin(t *testing.T) string {
	t.Helper()
	bin, err := lmuxBin()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// series is a repository of commits in a line, each adding a file of its own.
func series(t *testing.T, subjects ...string) *repo {
	t.Helper()
	r := newRepo(t)
	for i, s := range subjects {
		r.Commit(s, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("%d\n", i))
	}
	return r
}

// subjects reads the history of the working tree back, newest first.
func subjects(t *testing.T, r *repo) []string {
	t.Helper()
	commits, err := r.Runner.Log(t.Context(), r.Dir, LogOptions{Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	out := make([]string, len(commits))
	for i, c := range commits {
		out[i] = c.Subject
	}
	return out
}

// commitNamed is the object name of the commit with that subject.
func commitNamed(t *testing.T, r *repo, subject string) string {
	t.Helper()
	commits, err := r.Runner.Log(t.Context(), r.Dir, LogOptions{Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	for _, c := range commits {
		if c.Subject == subject {
			return c.OID
		}
	}
	t.Fatalf("no commit named %q in %v", subject, subjects(t, r))
	return ""
}

func TestIntegrationHistoryEdits(t *testing.T) {
	bin := sequenceEditorBin(t)
	ctx := t.Context()
	cases := []struct {
		name string
		on   string
		act  func(r *repo, e Edit) error
		want []string
	}{
		{
			name: "a commit left out",
			on:   "two",
			act:  func(r *repo, e Edit) error { return r.Runner.DropCommit(ctx, r.Dir, e) },
			want: []string{"three", "one"},
		},
		{
			name: "the first commit left out",
			on:   "one",
			act:  func(r *repo, e Edit) error { return r.Runner.DropCommit(ctx, r.Dir, e) },
			want: []string{"three", "two"},
		},
		{
			name: "a commit folded into the one before it",
			on:   "two",
			act:  func(r *repo, e Edit) error { return r.Runner.FixupCommit(ctx, r.Dir, e) },
			want: []string{"three", "one"},
		},
		{
			name: "a commit folded with both messages kept",
			on:   "three",
			act:  func(r *repo, e Edit) error { return r.Runner.SquashCommit(ctx, r.Dir, e) },
			// The message of the commit it lands in comes first, so the
			// subject of the fold is the subject of that commit.
			want: []string{"two", "one"},
		},
		{
			name: "a commit moved earlier",
			on:   "three",
			act:  func(r *repo, e Edit) error { e.By = -1; return r.Runner.MoveCommit(ctx, r.Dir, e) },
			want: []string{"two", "three", "one"},
		},
		{
			name: "a commit moved later",
			on:   "one",
			act:  func(r *repo, e Edit) error { e.By = 1; return r.Runner.MoveCommit(ctx, r.Dir, e) },
			want: []string{"three", "one", "two"},
		},
		{
			name: "a message written again on an older commit",
			on:   "two",
			act: func(r *repo, e Edit) error {
				e.Message = "two, said better"
				return r.Runner.Reword(ctx, r.Dir, e)
			},
			want: []string{"three", "two, said better", "one"},
		},
		{
			name: "a message written again on the commit HEAD stands on",
			on:   "three",
			act: func(r *repo, e Edit) error {
				e.Message = "three, said better"
				return r.Runner.Reword(ctx, r.Dir, e)
			},
			want: []string{"three, said better", "two", "one"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := series(t, "one", "two", "three")
			e := Edit{OID: commitNamed(t, r, tc.on), Bin: bin}
			if err := tc.act(r, e); err != nil {
				t.Fatalf("the edit failed: %v", err)
			}
			if got := subjects(t, r); !slices.Equal(got, tc.want) {
				t.Fatalf("the history reads as %v, want %v", got, tc.want)
			}
			state, err := r.Runner.InProgress(ctx, r.Dir)
			if err != nil {
				t.Fatalf("InProgress() error = %v", err)
			}
			if state.Running() {
				t.Fatalf("InProgress() = %+v, want the replay through", state)
			}
		})
	}
}

// TestIntegrationSquashKeepsBothMessages proves the difference between
// folding a commit with its message and folding it without.
func TestIntegrationSquashKeepsBothMessages(t *testing.T) {
	bin := sequenceEditorBin(t)
	r := series(t, "one", "two")
	e := Edit{OID: commitNamed(t, r, "two"), Bin: bin}
	if err := r.Runner.SquashCommit(t.Context(), r.Dir, e); err != nil {
		t.Fatalf("SquashCommit() error = %v", err)
	}
	message := r.Git("log", "-1", "--format=%B")
	if !strings.Contains(message, "one") || !strings.Contains(message, "two") {
		t.Fatalf("the message reads as %q, want both", message)
	}

	r2 := series(t, "one", "two")
	e2 := Edit{OID: commitNamed(t, r2, "two"), Bin: bin}
	if err := r2.Runner.FixupCommit(t.Context(), r2.Dir, e2); err != nil {
		t.Fatalf("FixupCommit() error = %v", err)
	}
	if got := r2.Git("log", "-1", "--format=%B"); strings.Contains(got, "two") {
		t.Fatalf("the message reads as %q, want the one it landed in alone", got)
	}
}

func TestIntegrationHistoryEditRefusals(t *testing.T) {
	bin := sequenceEditorBin(t)
	ctx := t.Context()
	r := series(t, "one", "two", "three")
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	first := commitNamed(t, r, "one")
	cases := []struct {
		name string
		act  func() error
	}{
		{name: "a commit that is no commit", act: func() error { return r.Runner.DropCommit(ctx, r.Dir, Edit{OID: "nope", Bin: bin}) }},
		{
			name: "a commit that is not in the history",
			act: func() error {
				return r.Runner.DropCommit(ctx, r.Dir, Edit{OID: strings.Repeat("a", 40), Bin: bin})
			},
		},
		{
			name: "the first commit folded into nothing",
			act:  func() error { return r.Runner.SquashCommit(ctx, r.Dir, Edit{OID: first, Bin: bin}) },
		},
		{
			name: "the first commit moved earlier",
			act:  func() error { return r.Runner.MoveCommit(ctx, r.Dir, Edit{OID: first, By: -1, Bin: bin}) },
		},
		{
			name: "a commit moved nowhere",
			act:  func() error { return r.Runner.MoveCommit(ctx, r.Dir, Edit{OID: first, Bin: bin}) },
		},
		{
			name: "a message of nothing",
			act:  func() error { return r.Runner.Reword(ctx, r.Dir, Edit{OID: first, Message: "  \n", Bin: bin}) },
		},
		{
			name: "no sequence editor to hand the plan to",
			act:  func() error { return r.Runner.DropCommit(ctx, r.Dir, Edit{OID: first}) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.act(); err == nil {
				t.Fatal("the edit went through, want it refused")
			}
			if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
				t.Fatalf("the history moved to %s", got)
			}
			state, err := r.Runner.InProgress(ctx, r.Dir)
			if err != nil {
				t.Fatalf("InProgress() error = %v", err)
			}
			if state.Running() {
				t.Fatalf("InProgress() = %+v, want nothing left running", state)
			}
		})
	}
}

// TestIntegrationRemoteBranchesRead reads the refs a push leaves behind: the
// branches of two remotes, a name holding a slash, and the symbolic ref git
// writes for the branch a remote is on.
func TestIntegrationRemoteBranchesRead(t *testing.T) {
	r := series(t, "one")
	base := filepath.Dir(r.Dir)
	for _, name := range []string{"origin", "backup"} {
		path := filepath.Join(base, name+".git")
		r.Git("init", "-q", "--bare", path)
		r.Git("remote", "add", name, path)
	}
	r.Git("push", "-q", "origin", "main")
	r.Git("push", "-q", "backup", "main")
	r.Git("checkout", "-q", "-b", "feat/graph")
	r.Git("commit", "-q", "--allow-empty", "-m", "two")
	r.Git("push", "-q", "origin", "feat/graph")
	r.Git("remote", "set-head", "origin", "main")

	got, err := r.Runner.RemoteBranches(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("RemoteBranches() error = %v", err)
	}
	names := make([]string, len(got))
	for i, b := range got {
		names[i] = b.Name
		if !isHexOID(b.OID) {
			t.Fatalf("remote branch %+v points at no commit", b)
		}
	}
	want := []string{"backup/main", "origin/HEAD", "origin/feat/graph", "origin/main"}
	if !slices.Equal(names, want) {
		t.Fatalf("RemoteBranches() = %v, want %v", names, want)
	}
	for _, b := range got {
		switch b.Name {
		case "origin/HEAD":
			if b.Target != "refs/remotes/origin/main" {
				t.Fatalf("the branch origin is on stands for %q, want its main branch", b.Target)
			}
		case "backup/main":
			if b.Remote != "backup" {
				t.Fatalf("remote branch %+v belongs to %q, want backup", b, b.Remote)
			}
		default:
			if b.Symbolic() {
				t.Fatalf("remote branch %+v is symbolic, want a branch of its own", b)
			}
		}
	}

	// A directory outside any repository is a failure, not an empty list.
	outside := t.TempDir()
	if _, err := r.Runner.RemoteBranches(t.Context(), outside); err == nil {
		t.Fatal("RemoteBranches() read a directory that is no repository")
	}
}

// TestIntegrationShow reads commits of a real repository in full: a merge
// against its first parent, a commit with a body, and the refusals.
func TestIntegrationShow(t *testing.T) {
	r := newRepo(t)
	r.Write("a.txt", "a\nb\n")
	r.Write("logo.png", "\x00\x01\x02")
	r.Git("add", ".")
	r.Git("commit", "-qm", "the first commit\n\nA body over\ntwo lines.\n")
	root := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Git("checkout", "-q", "-b", "side")
	r.Write("a.txt", "a\nc\nd\n")
	r.Git("mv", "logo.png", "new logo.png")
	r.Git("commit", "-qam", "change a file and rename another")
	r.Git("checkout", "-q", "main")
	r.Commit("on main", "b.txt", "x\n")
	r.Git("merge", "-q", "--no-ff", "side", "-m", "a merge commit")

	ctx := t.Context()
	merge, err := r.Runner.Show(ctx, r.Dir, "HEAD")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if merge.Commit.Subject != "a merge commit" || len(merge.Commit.Parents) != 2 {
		t.Fatalf("Show() read %+v, want the merge and its two parents", merge.Commit)
	}
	// A merge is read against its first parent, which is what it brought in.
	paths := make([]string, len(merge.Files))
	for i, f := range merge.Files {
		paths[i] = f.Path
	}
	want := []string{"a.txt", "new logo.png"}
	if !slices.Equal(paths, want) {
		t.Fatalf("Show() read the files %v, want %v", paths, want)
	}
	if merge.Files[1].OrigPath != "logo.png" || !merge.Files[1].Binary {
		t.Fatalf("the renamed file reads as %+v, want the binary it was", merge.Files[1])
	}
	if merge.Added() != 2 || merge.Deleted() != 1 {
		t.Fatalf("the merge counts +%d -%d, want +2 -1", merge.Added(), merge.Deleted())
	}

	first, err := r.Runner.Show(ctx, r.Dir, root)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if first.Commit.Subject != "the first commit" || first.Body != "A body over\ntwo lines." {
		t.Fatalf("Show() read %q / %q, want the subject and the body apart", first.Commit.Subject, first.Body)
	}
	if len(first.Commit.Parents) != 0 {
		t.Fatalf("the first commit has the parents %v", first.Commit.Parents)
	}
	if first.Commit.Author == "" || first.AuthorEmail == "" || first.Committer == "" {
		t.Fatalf("Show() read %+v, want who wrote it and who landed it", first)
	}

	for _, rev := range []string{"", "--all", "-x", "no-such-ref"} {
		if _, err := r.Runner.Show(ctx, r.Dir, rev); err == nil {
			t.Fatalf("Show(%q) went through, want it refused", rev)
		}
	}
}
