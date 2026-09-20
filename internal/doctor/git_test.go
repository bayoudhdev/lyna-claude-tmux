package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/gittest"
)

// Fixes the git rows print, held here so a change to one is a change to the
// test that pins it.
const (
	fixGitStatus       = "git status (run it in the project directory to see what git reports)"
	fixGitWorktreeList = "git worktree list (run it in the project directory to see what git reports)"
	fixGitPrune        = "git worktree prune"
	fixGitRepair       = "git worktree repair"
	fixGitInit         = "git init"
	fixGitSwitch       = "git switch -c <name> keeps what is here on a branch of its own, git switch <branch> goes back to one"
	fixGitRebase       = "git rebase --continue (git rebase --skip leaves this commit out, git rebase --abort puts the branch back)"
	fixGitMerge        = "git merge --continue (or git merge --abort to put the branch back)"
	fixGitPick         = "git cherry-pick --continue (git cherry-pick --skip leaves this commit out, git cherry-pick --abort puts the branch back)"
	fixGitRevert       = "git revert --continue (git revert --skip leaves this commit out, git revert --abort puts the branch back)"
	fixGitBisect       = "git bisect good or git bisect bad to carry on (git bisect reset puts HEAD back where it started)"
)

func TestCheckGitProject(t *testing.T) {
	bins := map[string]string{"git": "/usr/bin/git"}
	runCheckCases(t, checkGitProject, []checkCase{
		{
			name: "git is not on PATH",
			sys:  fakeSystem{goos: "darwin"},
			// The git row above has already failed with the command that
			// installs it, so there is nothing to add here.
			want: nil,
		},
		{
			name: "git is there, and no project is open",
			sys:  fakeSystem{goos: "darwin", bins: bins},
			want: []Result{{
				ID: gitRepositoryID, Title: gitRepositoryTitle, Status: StatusSkip, Detail: "no project directory",
			}},
		},
	})
}

// TestCheckGitProjectReadsTheProjectWithGitFromPath runs the check as the
// command line does, git from PATH included, on a directory that is inside no
// repository.
func TestCheckGitProjectReadsTheProjectWithGitFromPath(t *testing.T) {
	gittest.Require(t)
	dir := t.TempDir()
	// git must read no configuration of the user's, here or anywhere else.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	d := System()
	d.ProjectDir = dir
	got := checkGitProject(t.Context(), d.withDefaults())
	if len(got) != 1 {
		t.Fatalf("got %d rows, want the repository alone: %+v", len(got), got)
	}
	want := Result{
		ID: gitRepositoryID, Title: gitRepositoryTitle, Status: StatusWarn,
		Detail: dir + " is inside no git working tree, so the changes pane, the review and the worktrees of the workspace stay empty",
		Fix:    fixGitInit,
	}
	if got[0] != want {
		t.Fatalf("repository row %+v, want %+v", got[0], want)
	}
}

// gitRow is what one row must say: its status, the pieces of its message and
// the exact fix it prints.
type gitRow struct {
	id        string
	status    Status
	detailHas []string
	fix       string
}

func TestGitProjectReadsRealRepositories(t *testing.T) {
	const (
		mainWorktree   = "is the main worktree of the repository"
		linkedWorktree = "is a linked worktree; the repository it shares is at "
		quiet          = "no rebase, merge, cherry pick, revert or bisection is in the middle"
		opened         = "opened under "
	)
	cases := []struct {
		name  string
		setup func(t *testing.T) (*gittest.Repo, string)
		want  []gitRow
	}{
		{
			name: "a clean repository",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				r.Commit("first", "a.txt", "one\n")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened, vcs.WorktreeDir}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a repository holding changes",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				r.Commit("first", "a.txt", "one\n")
				r.Write("a.txt", "changed\n")
				r.Write("new.txt", "new\n")
				r.Git("add", "new.txt")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a repository with no commit yet",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main, which has no commit yet"}},
			},
		},
		{
			name: "a subdirectory of the working tree",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				r.Commit("first", "sub/a.txt", "one\n")
				return r, filepath.Join(r.Dir, "sub")
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a rebase stopped on a conflict",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gitConflicting(t)
				r.Git("checkout", "-q", "side")
				r.Commit("a second change of the branch", "side.txt", "again\n")
				gitMustStop(t, r, "rebase", "main")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"a rebase is in the middle of side, at step 1 of 2, stopped on "},
					fix:       fixGitRebase,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{
					id: gitHeadID, status: StatusSkip,
					detailHas: []string{"HEAD is detached at ", "how git carries a rebase out", gitOperationTitle},
				},
			},
		},
		{
			name: "a merge stopped on a conflict",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gitConflicting(t)
				gitMustStop(t, r, "merge", "side")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"a merge is in the middle, stopped on "}, fix: fixGitMerge,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a cherry pick stopped on a conflict",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gitConflicting(t)
				gitMustStop(t, r, "cherry-pick", "side")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"a cherry pick is in the middle, stopped on "}, fix: fixGitPick,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a revert stopped on a conflict",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gitConflicting(t)
				r.Commit("a change on top", "a.txt", "on top\n")
				gitMustStop(t, r, "revert", "--no-edit", "HEAD~1")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"a revert is in the middle, stopped on "}, fix: fixGitRevert,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a bisection looking for a commit",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				for _, subject := range []string{"one", "two", "three", "four"} {
					r.Commit(subject, "a.txt", subject+"\n")
				}
				r.Git("bisect", "start", "HEAD", "HEAD~3")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"a bisection is in the middle of main"}, fix: fixGitBisect,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{
					id: gitHeadID, status: StatusSkip,
					detailHas: []string{"HEAD is detached at ", "how git carries a bisection out"},
				},
			},
		},
		{
			name: "a detached HEAD",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				r.Commit("first", "a.txt", "one\n")
				r.Git("checkout", "-q", "--detach")
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, " + opened}},
				{
					id: gitHeadID, status: StatusWarn,
					detailHas: []string{"HEAD is detached at ", "belongs to no branch"}, fix: fixGitSwitch,
				},
			},
		},
		{
			name: "a linked worktree of the project",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				r.Commit("first", "a.txt", "one\n")
				return r, gitAddWorktree(t, r, "task")
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{linkedWorktree, ".git"}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"2 worktrees, " + opened}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch task at "}},
			},
		},
		{
			name: "a worktree whose directory was removed",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.New(t)
				r.Commit("first", "a.txt", "one\n")
				path := gitAddWorktree(t, r, "task")
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				return r, r.Dir
			},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{mainWorktree}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{quiet}},
				{
					id: gitWorktreesID, status: StatusWarn,
					detailHas: []string{"git records 1 worktree whose directory is gone: ", vcs.WorktreeDir + "/task"},
					fix:       fixGitPrune,
				},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at "}},
			},
		},
		{
			name: "a directory that is no repository",
			setup: func(t *testing.T) (*gittest.Repo, string) {
				r := gittest.At(t, "plain")
				if err := os.Mkdir(r.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return r, r.Dir
			},
			want: []gitRow{{
				id: gitRepositoryID, status: StatusWarn,
				detailHas: []string{"is inside no git working tree", "the changes pane"}, fix: fixGitInit,
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, dir := tc.setup(t)
			got := gitProject(t.Context(), gitProjectDeps(dir), git.Runner{Environ: r.Environ()})
			assertGitRows(t, got, tc.want)
		})
	}
}

// TestGitProjectReadingsThatFail covers the answers a real repository cannot
// be made to give: a rev-parse that prints something else, a state file that
// cannot be read, a worktree list that fails and a working tree git does not
// record.
func TestGitProjectReadingsThatFail(t *testing.T) {
	const root = "/p/repo"
	repo := git.Repo{Root: root, GitDir: root + "/.git", CommonDir: root + "/.git"}
	here := []vcs.Worktree{{Path: root, Head: "9a75a07d74c612a14fdf11cb4ddb4cac9ea031bf", Branch: "refs/heads/main"}}
	cases := []struct {
		name   string
		reader gitFake
		exists bool
		want   []gitRow
	}{
		{
			name:   "rev-parse prints something else",
			reader: gitFake{repoErr: fmt.Errorf("%w: rev-parse printed 2 lines", vcs.ErrMalformed)},
			want: []gitRow{{
				id: gitRepositoryID, status: StatusWarn,
				detailHas: []string{"the working tree of " + root + " cannot be read", "rev-parse printed 2 lines"},
				fix:       fixGitStatus,
			}},
		},
		{
			name:   "a state file that is not a plain file",
			reader: gitFake{repo: repo, list: here, progErr: errors.New("git state: /p/repo/.git/MERGE_HEAD is not a plain file")},
			exists: true,
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{"is the main worktree"}},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"what git is in the middle of cannot be read", "is not a plain file"},
					fix:       fixGitStatus,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, opened under "}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at 9a75a07"}},
			},
		},
		{
			name:   "the worktree list fails",
			reader: gitFake{repo: repo, listErr: errors.New("git worktree: exit status 128: fatal: not a git repository")},
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{"is the main worktree"}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{"no rebase, merge"}},
				{
					id: gitWorktreesID, status: StatusWarn,
					detailHas: []string{"the worktrees of the repository cannot be read", "exit status 128"},
					fix:       fixGitWorktreeList,
				},
				{id: gitHeadID, status: StatusSkip, detailHas: []string{"where HEAD stands is unknown"}},
			},
		},
		{
			name:   "the working tree is not among the worktrees",
			reader: gitFake{repo: repo, list: []vcs.Worktree{{Path: "/p/other", Branch: "refs/heads/main"}}},
			exists: true,
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{"is the main worktree"}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{"no rebase, merge"}},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, opened under "}},
				{
					id: gitHeadID, status: StatusWarn,
					detailHas: []string{root + " is not among the worktrees git records"}, fix: fixGitRepair,
				},
			},
		},
		{
			name: "a worktree git itself calls prunable",
			reader: gitFake{
				repo: repo,
				list: append(here, vcs.Worktree{
					Path: root + "/.claude/worktrees/task", Branch: "refs/heads/task",
					Prunable: true, PruneReason: "gitdir file points to non-existent location",
				}),
			},
			exists: true,
			want: []gitRow{
				{id: gitRepositoryID, status: StatusOK, detailHas: []string{"is the main worktree"}},
				{id: gitOperationID, status: StatusOK, detailHas: []string{"no rebase, merge"}},
				{
					id: gitWorktreesID, status: StatusWarn,
					detailHas: []string{"git records 1 worktree whose directory is gone: " + root + "/.claude/worktrees/task"},
					fix:       fixGitPrune,
				},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at 9a75a07"}},
			},
		},
		{
			name: "a linked worktree in the middle of a merge during a bisection",
			reader: gitFake{
				repo: git.Repo{Root: root, GitDir: "/p/main/.git/worktrees/repo", CommonDir: "/p/main/.git"},
				list: here,
				prog: vcs.InProgress{
					Kind:      vcs.OperationMerge,
					Heads:     []string{"5f2c0a1b3d4e5f60718293a4b5c6d7e8f9012345"},
					Bisecting: true,
				},
			},
			exists: true,
			want: []gitRow{
				{
					id: gitRepositoryID, status: StatusOK,
					detailHas: []string{"is a linked worktree; the repository it shares is at /p/main/.git"},
				},
				{
					id: gitOperationID, status: StatusWarn,
					detailHas: []string{"a merge is in the middle, stopped on 5f2c0a1", "a bisection is running as well"},
					fix:       fixGitMerge,
				},
				{id: gitWorktreesID, status: StatusOK, detailHas: []string{"1 worktree, opened under "}},
				{id: gitHeadID, status: StatusOK, detailHas: []string{"on branch main at 9a75a07"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := gitProjectDeps(root)
			d.Exists = func(string) bool { return tc.exists }
			assertGitRows(t, gitProject(t.Context(), d, tc.reader), tc.want)
		})
	}
}

// TestGitChecksWriteNothing holds the whole point of the git rows: they are a
// reading. Every check runs against a repository in the middle of a rebase,
// with a worktree beside it, and the repository must come out of it byte for
// byte as it went in.
func TestGitChecksWriteNothing(t *testing.T) {
	r := gitConflicting(t)
	r.Git("checkout", "-q", "side")
	gitMustStop(t, r, "rebase", "main")
	gitAddWorktree(t, r, "task")
	// The check runs git from PATH, so the process environment becomes the
	// repository's own: no configuration of the user's is read here either.
	for _, kv := range r.Env {
		switch name, value, _ := strings.Cut(kv, "="); name {
		case "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL":
			t.Setenv(name, value)
		}
	}
	// A status settles the index once, so what the checks are compared
	// against is a repository that has nothing left to write.
	r.Git("status", "--porcelain=v2", "--branch")
	head, refs := r.Git("rev-parse", "HEAD"), r.Git("for-each-ref")
	status := r.Git("status", "--porcelain=v2", "--branch")
	before := gitSnapshot(t, r.Dir)

	d := System()
	d.ProjectDir = r.Dir
	rows := checkGitProject(t.Context(), d.withDefaults())
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want the four of the project: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Action != nil {
			t.Fatalf("row %s carries an action (%+v); doctor never repairs a repository", row.ID, row.Action)
		}
	}

	after := gitSnapshot(t, r.Dir)
	for name, sum := range after {
		switch was, known := before[name]; {
		case !known:
			t.Errorf("%s was created", name)
		case was != sum:
			t.Errorf("%s changed", name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("%s was removed", name)
		}
	}
	if got := r.Git("rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD is %q, was %q", got, head)
	}
	if got := r.Git("for-each-ref"); got != refs {
		t.Errorf("refs are\n%s\nwere\n%s", got, refs)
	}
	if got := r.Git("status", "--porcelain=v2", "--branch"); got != status {
		t.Errorf("status is\n%s\nwas\n%s", got, status)
	}
}

func TestGitOperationNamesAndFixes(t *testing.T) {
	cases := []struct {
		name      string
		kind      vcs.Operation
		wantName  string
		wantFix   string
		wantVerbs []string
	}{
		{name: "a merge", kind: vcs.OperationMerge, wantName: "a merge", wantFix: fixGitMerge},
		{name: "a rebase", kind: vcs.OperationRebase, wantName: "a rebase", wantFix: fixGitRebase},
		{name: "a patch application", kind: vcs.OperationApply, wantName: "a patch application", wantFix: "git am --continue (git am --skip leaves this patch out, git am --abort puts the branch back)"},
		{name: "a cherry pick", kind: vcs.OperationCherryPick, wantName: "a cherry pick", wantFix: fixGitPick},
		{name: "a revert", kind: vcs.OperationRevert, wantName: "a revert", wantFix: fixGitRevert},
		{name: "a bisection", kind: vcs.OperationBisect, wantName: "a bisection", wantFix: fixGitBisect},
		{name: "an operation this git does not know", kind: vcs.Operation(42), wantName: "an operation (unknown)", wantFix: fixGitStatus},
		{name: "nothing running", kind: vcs.OperationNone, wantName: "an operation (none)", wantFix: fixGitStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, fix := gitOperation(tc.kind)
			if name != tc.wantName || fix != tc.wantFix {
				t.Fatalf("gitOperation(%v) = %q, %q; want %q, %q", tc.kind, name, fix, tc.wantName, tc.wantFix)
			}
		})
	}
}

func TestGitShortAndCounts(t *testing.T) {
	cases := []struct {
		name       string
		oid        string
		wantShort  string
		wantUnborn bool
		count      int
		wantCount  string
	}{
		{
			name: "an object name", oid: "9a75a07d74c612a14fdf11cb4ddb4cac9ea031bf", wantShort: "9a75a07",
			count: 3, wantCount: "3 worktrees",
		},
		{
			name: "an object name shorter than git abbreviates", oid: "abcd", wantShort: "abcd",
			count: 1, wantCount: "1 worktree",
		},
		{
			name: "the head of a branch with no commit", oid: strings.Repeat("0", 40), wantShort: "0000000",
			wantUnborn: true, count: 0, wantCount: "0 worktrees",
		},
		{name: "no head at all", oid: "", wantShort: "", wantUnborn: true, count: 12, wantCount: "12 worktrees"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitShort(tc.oid); got != tc.wantShort {
				t.Fatalf("gitShort(%q) = %q, want %q", tc.oid, got, tc.wantShort)
			}
			if got := gitUnborn(tc.oid); got != tc.wantUnborn {
				t.Fatalf("gitUnborn(%q) = %v, want %v", tc.oid, got, tc.wantUnborn)
			}
			if got := gitWorktrees(tc.count); got != tc.wantCount {
				t.Fatalf("gitWorktrees(%d) = %q, want %q", tc.count, got, tc.wantCount)
			}
		})
	}
}

// gitFake is a reading of git a test dictates, for what a real repository
// cannot be made to answer.
type gitFake struct {
	repo    git.Repo
	repoErr error
	prog    vcs.InProgress
	progErr error
	list    []vcs.Worktree
	listErr error
}

func (f gitFake) Repo(context.Context, string) (git.Repo, error) { return f.repo, f.repoErr }

func (f gitFake) InProgress(context.Context, string) (vcs.InProgress, error) {
	return f.prog, f.progErr
}

func (f gitFake) Worktrees(context.Context, string) ([]vcs.Worktree, error) {
	return f.list, f.listErr
}

// gitProjectDeps is a doctor bound to a project directory, reading the disk
// for the worktree directories git records.
func gitProjectDeps(dir string) Deps {
	return Deps{
		ProjectDir: dir,
		Exists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
	}.withDefaults()
}

// gitConflicting is a repository whose branch and project changed the same
// file, so merging, picking or replaying one on the other stops.
func gitConflicting(t *testing.T) *gittest.Repo {
	t.Helper()
	r := gittest.New(t)
	r.Commit("first", "a.txt", "one\n")
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("a change of the branch", "a.txt", "branch\n")
	r.Git("checkout", "-q", "main")
	r.Commit("a change of the project", "a.txt", "project\n")
	return r
}

// gitMustStop runs a command that has to stop on a conflict, which is what
// leaves the repository in the middle of the operation.
func gitMustStop(t *testing.T, r *gittest.Repo, args ...string) {
	t.Helper()
	if out, err := r.Try(args...); err == nil {
		t.Fatalf("git %v went through, want it stopped on a conflict\n%s", args, out)
	}
}

// gitAddWorktree opens a worktree where the workspace opens its own and
// returns its path.
func gitAddWorktree(t *testing.T, r *gittest.Repo, name string) string {
	t.Helper()
	path, err := vcs.WorktreePath(r.Dir, name)
	if err != nil {
		t.Fatal(err)
	}
	r.Git("worktree", "add", "-q", "-b", name, path)
	return path
}

// gitSnapshot hashes every file of a directory, the git directory included,
// so a reading that wrote anything at all is caught.
func gitSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			out[rel] = "directory"
			return nil
		case !entry.Type().IsRegular():
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "link to " + target
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertGitRows(t *testing.T, got []Result, want []gitRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		r := got[i]
		if r.ID != w.id || r.Status != w.status {
			t.Fatalf("row %d is %s/%s, want %s/%s (detail %q)", i, r.ID, r.Status, w.id, w.status, r.Detail)
		}
		if r.Title == "" {
			t.Fatalf("row %s has no title", r.ID)
		}
		for _, part := range w.detailHas {
			if !strings.Contains(r.Detail, part) {
				t.Fatalf("row %s detail %q, want it to contain %q", r.ID, r.Detail, part)
			}
		}
		if r.Fix != w.fix {
			t.Fatalf("row %s fix %q, want %q", r.ID, r.Fix, w.fix)
		}
		if r.Action != nil {
			t.Fatalf("row %s carries an action (%+v); doctor never repairs a repository", r.ID, r.Action)
		}
	}
}
