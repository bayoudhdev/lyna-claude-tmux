package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/gittest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// lmuxBin builds the binary git runs as the sequence editor, once per test
// run, so a history is rewritten here the way it is in a workspace.
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

// opsRepo is a repository of commits in a line and the operations wired to
// it, which is what the workstation holds in a project.
func opsRepo(t *testing.T, subjects ...string) (GitOps, *gittest.Repo) {
	t.Helper()
	bin, err := lmuxBin()
	if err != nil {
		t.Fatal(err)
	}
	r := gittest.New(t)
	for i, s := range subjects {
		r.Commit(s, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("%d\n", i))
	}
	return GitOps{
		Runner:  git.Runner{Environ: r.Environ()},
		Dir:     r.Dir,
		Bin:     bin,
		Remote:  "origin",
		Patches: filepath.Join(r.Dir, "patches"),
	}, r
}

// opsState reads the repository the way the workstation reads it.
func opsState(t *testing.T, o GitOps) tui.GitState {
	t.Helper()
	commits, err := o.Runner.Log(t.Context(), o.Dir, git.LogOptions{Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	changes, err := o.Runner.Changes(t.Context(), o.Dir)
	if err != nil {
		t.Fatalf("Changes() error = %v", err)
	}
	worktrees, err := o.Runner.Worktrees(t.Context(), o.Dir)
	if err != nil {
		t.Fatalf("Worktrees() error = %v", err)
	}
	return tui.GitState{Commits: commits, Changes: changes, Refs: tui.GitRefs{Worktrees: worktrees}}
}

// opsSubjects reads the history back, newest first.
func opsSubjects(t *testing.T, o GitOps) []string {
	t.Helper()
	out := []string{}
	for _, c := range opsState(t, o).Commits {
		out = append(out, c.Subject)
	}
	return out
}

// opsCommit is the object name of the commit with that subject.
func opsCommit(t *testing.T, o GitOps, subject string) string {
	t.Helper()
	for _, c := range opsState(t, o).Commits {
		if c.Subject == subject {
			return c.OID
		}
	}
	t.Fatalf("no commit named %q in %v", subject, opsSubjects(t, o))
	return ""
}

// TestIntegrationGitOpsRewritesTheHistory runs the operations that replay a
// branch against a real repository, since what they run is a plan of ours
// carried out by git and not one command that can be read off an argv.
func TestIntegrationGitOpsRewritesTheHistory(t *testing.T) {
	cases := []struct {
		name string
		op   tui.GitOp
		on   string
		want []string
		note string
	}{
		{
			name: "a commit reworded",
			op:   tui.GitOp{Kind: tui.OpReword, Text: "said again"},
			on:   "two",
			want: []string{"three", "said again", "one"},
			note: "reworded ",
		},
		{
			name: "a commit dropped",
			op:   tui.GitOp{Kind: tui.OpDrop},
			on:   "two",
			want: []string{"three", "one"},
			note: "dropped ",
		},
		{
			name: "a commit folded into the one before",
			op:   tui.GitOp{Kind: tui.OpSquash},
			on:   "two",
			want: []string{"three", "one"},
			note: "folded ",
		},
		{
			name: "a commit folded in, keeping the other message",
			op:   tui.GitOp{Kind: tui.OpFixup},
			on:   "two",
			want: []string{"three", "one"},
			note: "folded ",
		},
		{
			name: "a commit moved earlier",
			op:   tui.GitOp{Kind: tui.OpMoveUp},
			on:   "two",
			want: []string{"three", "one", "two"},
			note: "one place earlier",
		},
		{
			name: "a commit moved later",
			op:   tui.GitOp{Kind: tui.OpMoveDown},
			on:   "two",
			want: []string{"two", "three", "one"},
			note: "one place later",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := opsRepo(t, "one", "two", "three")
			op := tc.op
			op.On, op.Rev, op.Confirmed = tui.OnCommit, opsCommit(t, o, tc.on), true
			_, _, note := opRun(t, o, op, opsState(t, o))
			if !strings.Contains(note, tc.note) {
				t.Fatalf("the bar says %q, want it to carry %q", note, tc.note)
			}
			if got := opsSubjects(t, o); !slices.Equal(got, tc.want) {
				t.Fatalf("the history is %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIntegrationGitOpsWorktrees opens a worktree of the project and removes
// it again, both of which read the list of worktrees before they act.
func TestIntegrationGitOpsWorktrees(t *testing.T) {
	o, _ := opsRepo(t, "one", "two")
	add := tui.GitOp{Kind: tui.OpWorktreeAdd, On: tui.OnCommit, Rev: opsCommit(t, o, "two"), Text: "task-a", Confirmed: true}
	if _, _, note := opRun(t, o, add, opsState(t, o)); !strings.Contains(note, "opened the worktree task-a") {
		t.Fatalf("the bar says %q", note)
	}
	st := opsState(t, o)
	var at string
	for _, w := range st.Refs.Worktrees {
		if w.Name() == "task-a" {
			at = w.Path
		}
	}
	if at == "" {
		t.Fatalf("the worktree is in no list: %+v", st.Refs.Worktrees)
	}
	if _, err := os.Stat(at); err != nil {
		t.Fatalf("the worktree is nowhere on disk: %v", err)
	}

	remove := tui.GitOp{
		Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefWorktree,
		Name: "task-a", Path: at, Index: -1, Confirmed: true,
	}
	if _, _, note := opRun(t, o, remove, st); !strings.Contains(note, "removed the worktree task-a") {
		t.Fatalf("the bar says %q", note)
	}
	if _, err := os.Stat(at); !os.IsNotExist(err) {
		t.Fatalf("the worktree is still there: %v", err)
	}
}

// TestIntegrationGitOpsAnswersWhatStopped carries on a rebase that stopped on
// a conflict, which is the command the banner offers.
func TestIntegrationGitOpsAnswersWhatStopped(t *testing.T) {
	o, r := opsRepo(t, "one")
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("theirs", "clash.txt", "theirs\n")
	r.Git("checkout", "-q", "main")
	r.Commit("ours", "clash.txt", "ours\n")
	if _, err := r.Try("rebase", "side"); err == nil {
		t.Fatal("the rebase went through, want it stopped on the clash")
	}
	st := opsState(t, o)
	st.Progress, _ = o.Runner.InProgress(t.Context(), o.Dir)
	if !st.Progress.Running() {
		t.Fatal("git says it stopped in the middle of nothing")
	}
	op := tui.GitOp{Kind: tui.OpAbort, On: tui.OnRunning, Confirmed: true}
	if _, _, note := opRun(t, o, op, st); !strings.Contains(note, "is undone") {
		t.Fatalf("the bar says %q", note)
	}
	if got := opsSubjects(t, o); !slices.Equal(got, []string{"ours", "one"}) {
		t.Fatalf("the branch is at %v, want it back where it started", got)
	}
}

// TestIntegrationGitOpsStagesAndCommits runs the everyday path end to end:
// what the working tree holds, staged, committed and read back.
func TestIntegrationGitOpsStagesAndCommits(t *testing.T) {
	o, r := opsRepo(t, "one")
	r.Write("new.txt", "new\n")
	r.Write("f0.txt", "changed\n")
	st := opsState(t, o)
	if len(st.Changes.Files) != 2 {
		t.Fatalf("the working tree holds %+v, want the two files that were written", st.Changes.Files)
	}
	if _, _, note := opRun(t, o, tui.GitOp{Kind: tui.OpStageAll}, st); note != "staged every change" {
		t.Fatalf("the bar says %q", note)
	}
	st = opsState(t, o)
	if st.Changes.Staged() != 2 {
		t.Fatalf("%d files are staged, want two", st.Changes.Staged())
	}
	commit := tui.GitOp{Kind: tui.OpCommit, Text: "what the workstation did", Confirmed: true}
	if _, _, note := opRun(t, o, commit, st); note != "committed on main" {
		t.Fatalf("the bar says %q", note)
	}
	if got := opsSubjects(t, o); !slices.Equal(got, []string{"what the workstation did", "one"}) {
		t.Fatalf("the history is %v", got)
	}
	if st = opsState(t, o); !st.Changes.Clean() {
		t.Fatalf("the working tree still holds %+v", st.Changes.Files)
	}

	// A file discarded is a file back to what HEAD holds, and an untracked
	// one is a file thrown away.
	r.Write("f0.txt", "changed again\n")
	r.Write("other.txt", "untracked\n")
	st = opsState(t, o)
	discard := tui.GitOp{Kind: tui.OpDiscard, On: tui.OnFile, Path: "f0.txt", Confirmed: true}
	opRun(t, o, discard, st)
	clean := tui.GitOp{Kind: tui.OpDiscard, On: tui.OnFile, Path: "other.txt", Confirmed: true}
	opRun(t, o, clean, st)
	if st = opsState(t, o); !st.Changes.Clean() {
		t.Fatalf("the working tree still holds %+v", st.Changes.Files)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, "other.txt")); !os.IsNotExist(err) {
		t.Fatalf("the untracked file is still there: %v", err)
	}
}

// TestIntegrationGitOpsTagsAndPatches writes a tag and a patch of a real
// commit, since both land on disk.
func TestIntegrationGitOpsTagsAndPatches(t *testing.T) {
	o, _ := opsRepo(t, "one", "two")
	rev := opsCommit(t, o, "two")
	tag := tui.GitOp{Kind: tui.OpTagAnnotated, On: tui.OnCommit, Rev: rev, Name: "v1.0.0", Text: "the first one", Confirmed: true}
	if _, _, note := opRun(t, o, tag, opsState(t, o)); !strings.Contains(note, "as v1.0.0") {
		t.Fatalf("the bar says %q", note)
	}
	tags, err := o.Runner.Tags(t.Context(), o.Dir)
	if err != nil {
		t.Fatalf("Tags() error = %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0.0" || !tags[0].Annotated {
		t.Fatalf("the tags are %+v, want the annotated one that was written", tags)
	}

	patch := tui.GitOp{Kind: tui.OpPatch, On: tui.OnCommit, Rev: rev}
	if _, _, note := opRun(t, o, patch, opsState(t, o)); !strings.Contains(note, o.Patches) {
		t.Fatalf("the bar says %q", note)
	}
	files, err := os.ReadDir(o.Patches)
	if err != nil {
		t.Fatalf("the patches are nowhere: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("the patches are %v, want the one commit", files)
	}
}

// TestIntegrationGitOpsHoldsTheLeaseToWhatWasRead pushes while the remote
// still stands where the workstation read it, and refuses once it does not.
func TestIntegrationGitOpsHoldsTheLeaseToWhatWasRead(t *testing.T) {
	o, r := opsRepo(t, "one", "two")
	remote := filepath.Join(filepath.Dir(r.Dir), "origin.git")
	r.Git("init", "-q", "--bare", remote)
	r.Git("remote", "add", "origin", remote)
	r.Git("push", "-q", "-u", "origin", "main")
	r.Git("commit", "-q", "--allow-empty", "-m", "three")

	read := func(at string) tui.GitState {
		st := opsState(t, o)
		st.Changes.Head.Upstream = "origin/main"
		st.Refs.Remotes = []vcs.RemoteBranch{{Name: "origin/main", Remote: "origin", OID: at}}
		return st
	}
	op := tui.GitOp{Kind: tui.OpPushLease, Confirmed: true}

	// A reading that says the remote stands somewhere it does not is a
	// remote that moved, and the push stops rather than writing over it.
	_, _, note := opRun(t, o, op, read(opsCommit(t, o, "one")))
	if strings.Contains(note, "wrote main over") {
		t.Fatalf("the push went through and lost what the remote held: %q", note)
	}
	if !strings.Contains(note, "push over what the remote holds: ") {
		t.Fatalf("the bar says %q, want the refusal git gave", note)
	}

	// The reading that matches is the one that goes through.
	if _, _, note = opRun(t, o, op, read(opsCommit(t, o, "two"))); note != "wrote main over the one on origin" {
		t.Fatalf("the bar says %q", note)
	}
	if got := r.Git("rev-parse", "origin/main"); got != r.Git("rev-parse", "HEAD") {
		t.Fatalf("the remote is at %q, want what was pushed", got)
	}
}
