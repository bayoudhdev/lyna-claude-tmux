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
