package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// fakeGit records what the operations run and answers what the test says.
type fakeGit struct {
	mu    sync.Mutex
	calls [][]string
	out   map[string]git.Result
	fail  error
	// message is what the file a command was handed a message in held, since
	// the file itself is gone by the time the test looks.
	message string
}

func (f *fakeGit) Exec(_ context.Context, _ string, args, _ []string, _ int64) (git.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slices.Clone(args))
	for _, a := range args {
		if path, ok := strings.CutPrefix(a, "--file="); ok {
			content, err := os.ReadFile(path)
			if err == nil {
				f.message = string(content)
			}
		}
	}
	if f.fail != nil {
		return git.Result{}, f.fail
	}
	return f.out[fakeGitKey(args)], nil
}

// fakeGitKey is what an answer is filed under: the subcommand, and the two
// readings of rev-parse apart from each other.
func fakeGitKey(args []string) string {
	sub := args[5]
	if sub == "rev-parse" && slices.Contains(args, "--verify") {
		return "rev-parse --verify"
	}
	return sub
}

// ran reports whether that command was one of the ones run, since the typed
// call that changes the repository also reads it before and after.
func (f *fakeGit) ran(want []string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if slices.Equal(fakeGitArgv(c), want) {
			return true
		}
	}
	return false
}

// fakeGitArgv is one command as the test reads it: the arguments of the
// runner dropped, and the file a message was handed over in named by what it
// is rather than by where it happened to be written.
func fakeGitArgv(call []string) []string {
	argv := slices.Clone(call[5:])
	for i, a := range argv {
		if strings.HasPrefix(a, "--file=") {
			argv[i] = "--file=<message>"
		}
	}
	return argv
}

// argv is every command that was run, without the arguments each of them
// carries, for a failure worth reading.
func (f *fakeGit) argv() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, fakeGitArgv(c))
	}
	return out
}

const (
	opOIDA = "1111111111111111111111111111111111111111"
	opOIDB = "2222222222222222222222222222222222222222"
	opOIDC = "3333333333333333333333333333333333333333"
)

// opState is a repository the operations are worked out against: a branch
// with a remote, another branch, a tag, a stash, a worktree and a working
// tree holding one staged file, one changed and one untracked.
func opState() tui.GitState {
	return tui.GitState{
		Commits: []vcs.Commit{
			{OID: opOIDA, Subject: "the newest commit"},
			{OID: opOIDB, Subject: "the one before"},
			{OID: opOIDC, Subject: "the oldest"},
		},
		Refs: tui.GitRefs{
			Branches: []vcs.LocalBranch{
				{Name: "main", Ref: "refs/heads/main", OID: opOIDA, Head: true, Upstream: "refs/remotes/origin/main"},
				{Name: "side", Ref: "refs/heads/side", OID: opOIDB},
			},
			Remotes:   []vcs.RemoteBranch{{Name: "origin/main", Ref: "refs/remotes/origin/main", Remote: "origin", OID: opOIDB}},
			Worktrees: []vcs.Worktree{{Path: "/w/task-a", Branch: "refs/heads/task-a", Head: opOIDB}},
			Stashes:   []vcs.Stash{{Index: 0, Ref: "stash@{0}", OID: opOIDC, Message: "what was put aside"}},
			Tags:      []vcs.Tag{{Name: "v1.0.0", Ref: "refs/tags/v1.0.0", OID: opOIDB, Commit: opOIDB}},
		},
		Changes: vcs.Changes{
			Head: vcs.Head{Name: "main", OID: opOIDA, Upstream: "origin/main", AheadBehind: true, Ahead: 2},
			Files: []vcs.File{
				{Kind: vcs.KindChanged, Path: "a.txt", Index: '.', Worktree: 'M'},
				{Kind: vcs.KindUntracked, Path: "new.txt", Index: '?', Worktree: '?'},
				{Kind: vcs.KindChanged, Path: "staged.txt", Index: 'M', Worktree: '.'},
			},
		},
	}
}

// opRunner is the operations wired to a fake git, with somewhere to write a
// patch and somewhere to copy an object name to.
func opRunner(t *testing.T) (GitOps, *fakeGit, *string) {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// What every command that reads the repository before it changes it is
	// answered with: where the repository is, and what HEAD stands on.
	f := &fakeGit{out: map[string]git.Result{
		"rev-parse":          gitResultOf(dir + "\n" + gitDir + "\n" + gitDir + "\n"),
		"rev-parse --verify": gitResultOf(opOIDA + "\n"),
		"branch":             gitResultOf("side\n"),
	}}
	var copied string
	o := GitOps{
		Runner:  git.Runner{Executor: f},
		Dir:     dir,
		Bin:     "/opt/lmux",
		Remote:  "origin",
		Patches: filepath.Join(dir, "patches"),
		Copy: func(_ context.Context, text string) error {
			copied = text
			return nil
		},
	}
	return o, f, &copied
}

// opRun drives one operation and reports what came back: the form it put up,
// the line it left on the bar, and what it ran.
func opRun(t *testing.T, o GitOps, op tui.GitOp, st tui.GitState) (tui.GitForm, tui.GitOp, string) {
	t.Helper()
	cmd := o.Run(t.Context(), op, st)
	if cmd == nil {
		t.Fatal("the operation answered with nothing at all")
	}
	switch msg := cmd().(type) {
	case tui.GitAskMsg:
		return msg.Form, msg.Op, ""
	case tui.GitWorkNoteMsg:
		return tui.GitForm{}, tui.GitOp{}, msg.Text
	default:
		t.Fatalf("the operation answered with %T", msg)
		return tui.GitForm{}, tui.GitOp{}, ""
	}
}

// TestGitOpsCommands holds every operation of the workstation to the command
// it runs and to the line the bar then says. The operations that rewrite a
// history and the ones that open a worktree read a real repository first, so
// they are held by TestIntegrationGitOps instead.
func TestGitOpsCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		op   tui.GitOp
		want []string
		note string
	}{
		{
			name: "a file staged",
			op:   tui.GitOp{Kind: tui.OpStage, On: tui.OnFile, Path: "a.txt"},
			want: []string{"add", "-A", "--", "a.txt"},
			note: "staged a.txt",
		},
		{
			name: "everything staged",
			op:   tui.GitOp{Kind: tui.OpStageAll},
			want: []string{"add", "-A", "--", ":/"},
			note: "staged every change",
		},
		{
			name: "a file unstaged",
			op:   tui.GitOp{Kind: tui.OpUnstage, On: tui.OnFile, Path: "a.txt"},
			want: []string{"reset", "-q", "--", "a.txt"},
			note: "unstaged a.txt",
		},
		{
			name: "everything unstaged",
			op:   tui.GitOp{Kind: tui.OpUnstageAll},
			want: []string{"reset", "-q"},
			note: "unstaged everything",
		},
		{
			name: "a tracked file discarded",
			op:   tui.GitOp{Kind: tui.OpDiscard, On: tui.OnFile, Path: "a.txt", Confirmed: true},
			want: []string{"restore", "--worktree", "--", "a.txt"},
			note: "discarded a.txt",
		},
		{
			name: "an untracked file discarded, which is a file thrown away",
			op:   tui.GitOp{Kind: tui.OpDiscard, On: tui.OnFile, Path: "new.txt", Confirmed: true},
			want: []string{"clean", "-f", "-d", "-q", "--", "new.txt"},
			note: "discarded new.txt",
		},
		{
			name: "every change discarded",
			op:   tui.GitOp{Kind: tui.OpDiscardAll, Confirmed: true},
			want: []string{"restore", "--worktree", "--", ":/"},
			note: "the tracked files are back to what HEAD holds",
		},
		{
			name: "what is staged committed",
			op:   tui.GitOp{Kind: tui.OpCommit, Text: "a message", Confirmed: true},
			want: []string{"commit", "--no-status", "--file=<message>", "--cleanup=whitespace", "--"},
			note: "committed on main",
		},
		{
			name: "the last commit amended",
			op:   tui.GitOp{Kind: tui.OpAmend, Confirmed: true},
			want: []string{"commit", "--no-status", "--no-edit", "--amend", "--"},
			note: "amended 1111111",
		},
		{
			name: "the message of HEAD written again",
			op:   tui.GitOp{Kind: tui.OpRewordHead, Text: "a new message", Confirmed: true},
			want: []string{"commit", "--no-status", "--amend", "--file=<message>", "--cleanup=whitespace", "--"},
			note: "reworded 1111111",
		},
		{
			name: "a branch checked out",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Rev: "refs/heads/side"},
			want: []string{"switch", "--no-guess", "side"},
			note: "on side",
		},
		{
			name: "a branch of a remote that is not here yet",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnRef, RefKind: tui.GitRefRemote, Name: "origin/theirs", Rev: "refs/remotes/origin/theirs"},
			want: []string{"switch", "--create", "theirs", "--track", "origin/theirs"},
			note: "on theirs, following origin/theirs",
		},
		{
			name: "a branch of a remote already followed here",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnRef, RefKind: tui.GitRefRemote, Name: "origin/main", Rev: "refs/remotes/origin/main"},
			want: []string{"switch", "--no-guess", "main"},
			note: "on main, which already follows origin/main",
		},
		{
			name: "a tag checked out",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnRef, RefKind: tui.GitRefTag, Name: "v1.0.0", Rev: "refs/tags/v1.0.0"},
			want: []string{"switch", "--detach", "refs/tags/v1.0.0"},
			note: "the working tree is on v1.0.0, on no branch",
		},
		{
			name: "a commit checked out",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnCommit, Rev: opOIDB},
			want: []string{"switch", "--detach", opOIDB},
			note: "the working tree is on 22222222, on no branch",
		},
		{
			name: "a branch opened at a commit",
			op:   tui.GitOp{Kind: tui.OpBranchHere, On: tui.OnCommit, Rev: opOIDB, Text: "task-b", Confirmed: true},
			want: []string{"switch", "--create", "task-b", opOIDB},
			note: "on task-b, from 22222222",
		},
		{
			name: "a branch renamed",
			op: tui.GitOp{
				Kind: tui.OpRename, On: tui.OnRef, RefKind: tui.GitRefBranch,
				Name: "side", Rev: "refs/heads/side", Text: "renamed", Confirmed: true,
			},
			want: []string{"branch", "--move", "--", "side", "renamed"},
			note: "side is now renamed",
		},
		{
			name: "a branch deleted",
			op: tui.GitOp{
				Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefBranch,
				Name: "side", Rev: "refs/heads/side", Confirmed: true,
			},
			want: []string{"branch", "--delete", "--", "side"},
			note: "deleted side",
		},
		{
			name: "a tag deleted",
			op: tui.GitOp{
				Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefTag,
				Name: "v1.0.0", Rev: "refs/tags/v1.0.0", Confirmed: true,
			},
			want: []string{"tag", "--delete", "--", "v1.0.0"},
			note: "deleted v1.0.0",
		},
		{
			name: "a stash dropped",
			op: tui.GitOp{
				Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefStash,
				Name: "stash@{0}", Index: 0, Confirmed: true,
			},
			want: []string{"stash", "drop", "stash@{0}"},
			note: "dropped stash@{0}",
		},
		{
			name: "a branch made to follow one of a remote",
			op: tui.GitOp{
				Kind: tui.OpSetUpstream, On: tui.OnRef, RefKind: tui.GitRefRemote,
				Name: "origin/main", Rev: "refs/remotes/origin/main",
			},
			want: []string{"branch", "--set-upstream-to=origin/main", "--", "main"},
			note: "main follows origin/main",
		},
		{
			name: "a branch merged in",
			op:   tui.GitOp{Kind: tui.OpMerge, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Rev: "refs/heads/side"},
			want: []string{"merge", "refs/heads/side"},
			note: "merged side into main",
		},
		{
			name: "the branch replayed onto another",
			op:   tui.GitOp{Kind: tui.OpRebase, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Rev: "refs/heads/side"},
			want: []string{"rebase", "--autostash", "refs/heads/side"},
			note: "main is replayed onto side",
		},
		{
			name: "a stash popped",
			op:   tui.GitOp{Kind: tui.OpStashPop, On: tui.OnRef, RefKind: tui.GitRefStash, Name: "stash@{0}", Index: 0},
			want: []string{"stash", "pop", "stash@{0}"},
			note: "popped stash@{0}",
		},
		{
			name: "a stash applied, which leaves it where it is",
			op:   tui.GitOp{Kind: tui.OpStashApply, On: tui.OnRef, RefKind: tui.GitRefStash, Name: "stash@{0}", Index: 0},
			want: []string{"stash", "apply", "stash@{0}"},
			note: "applied stash@{0}, which is still there",
		},
		{
			name: "a commit cherry picked",
			op:   tui.GitOp{Kind: tui.OpCherryPick, On: tui.OnCommit, Rev: opOIDB},
			want: []string{"cherry-pick", opOIDB},
			note: "cherry picked 22222222 onto main",
		},
		{
			name: "a commit reverted",
			op:   tui.GitOp{Kind: tui.OpRevert, On: tui.OnCommit, Rev: opOIDB},
			want: []string{"revert", opOIDB, "--no-edit"},
			note: "reverted 22222222 on main",
		},
		{
			name: "the branch moved, keeping what it held staged",
			op:   tui.GitOp{Kind: tui.OpResetSoft, On: tui.OnCommit, Rev: opOIDB},
			want: []string{"reset", "-q", "--soft", opOIDB, "--"},
			note: "main is at 22222222, with what it held staged",
		},
		{
			name: "the branch moved, keeping what it held in the working tree",
			op:   tui.GitOp{Kind: tui.OpResetMixed, On: tui.OnCommit, Rev: opOIDB},
			want: []string{"reset", "-q", "--mixed", opOIDB, "--"},
			note: "main is at 22222222, with what it held in the working tree",
		},
		{
			name: "the branch moved and the working tree written over",
			op:   tui.GitOp{Kind: tui.OpResetHard, On: tui.OnCommit, Rev: opOIDB, Confirmed: true},
			want: []string{"reset", "-q", "--hard", opOIDB, "--"},
			note: "main is at 22222222, and the working tree with it",
		},
		{
			name: "a commit tagged",
			op:   tui.GitOp{Kind: tui.OpTag, On: tui.OnCommit, Rev: opOIDB, Text: "v2.0.0", Confirmed: true},
			want: []string{"tag", "--", "v2.0.0", opOIDB},
			note: "tagged 22222222 as v2.0.0",
		},
		{
			name: "everything fetched and what is gone pruned",
			op:   tui.GitOp{Kind: tui.OpFetch},
			want: []string{"fetch", "--prune", "--tags", "--", "origin"},
			note: "fetched origin",
		},
		{
			name: "the branch pulled",
			op:   tui.GitOp{Kind: tui.OpPull},
			want: []string{"pull", "--no-rebase", "--no-edit", "--", "origin"},
			note: "pulled origin",
		},
		{
			name: "the branch pushed",
			op:   tui.GitOp{Kind: tui.OpPush},
			want: []string{"push", "--", "origin", "main"},
			note: "pushed main to origin",
		},
		{
			name: "the branch pushed over the one the remote was read at",
			op:   tui.GitOp{Kind: tui.OpPushLease, Confirmed: true},
			want: []string{"push", "--force-with-lease=main:" + opOIDB, "--", "origin", "main"},
			note: "wrote main over the one on origin",
		},
		{
			name: "the branch pushed and followed",
			op:   tui.GitOp{Kind: tui.OpPushUpstream},
			want: []string{"push", "--set-upstream", "--", "origin", "main"},
			note: "pushed main to origin, which it now follows",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, f, _ := opRunner(t)
			form, _, note := opRun(t, o, tc.op, opState())
			if form.Title != "" {
				t.Fatalf("a form stood before it: %q", form.Title)
			}
			if !f.ran(tc.want) {
				t.Fatalf("it ran %v, none of them %v", f.argv(), tc.want)
			}
			if note != tc.note {
				t.Fatalf("the bar says %q, want %q", note, tc.note)
			}
		})
	}
}

// TestGitOpsWritesAPatch writes it where it was told to and says where.
func TestGitOpsWritesAPatch(t *testing.T) {
	t.Parallel()
	o, f, _ := opRunner(t)
	op := tui.GitOp{Kind: tui.OpPatch, On: tui.OnCommit, Rev: opOIDB}
	_, _, note := opRun(t, o, op, opState())
	if !f.ran([]string{"format-patch", "--output-directory", o.Patches, opOIDB + "^!"}) {
		t.Fatalf("it ran %v", f.argv())
	}
	if want := "wrote the patch of 22222222 into " + o.Patches; note != want {
		t.Fatalf("the bar says %q, want %q", note, want)
	}
	info, err := os.Stat(o.Patches)
	if err != nil {
		t.Fatalf("the directory of the patches was not made: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("the directory of the patches is %v, want 0700", perm)
	}
}

// TestGitOpsCopiesTheObjectName runs no command at all: it hands the name to
// whoever can hold it.
func TestGitOpsCopiesTheObjectName(t *testing.T) {
	t.Parallel()
	o, f, copied := opRunner(t)
	_, _, note := opRun(t, o, tui.GitOp{Kind: tui.OpCopyOID, On: tui.OnCommit, Rev: opOIDB}, opState())
	if *copied != opOIDB {
		t.Fatalf("it copied %q, want the whole object name", *copied)
	}
	if len(f.argv()) != 0 {
		t.Fatalf("it ran %v, want no command at all", f.argv())
	}
	if note != "copied 22222222" {
		t.Fatalf("the bar says %q", note)
	}
	o.Copy = nil
	if _, _, note = opRun(t, o, tui.GitOp{Kind: tui.OpCopyOID, On: tui.OnCommit, Rev: opOIDB}, opState()); !strings.Contains(note, "nowhere to copy") {
		t.Fatalf("with nowhere to copy to the bar says %q", note)
	}
}

// TestGitOpsAnswersWhatGitStopped reads the state of the repository for the
// command, since the same three words belong to five operations.
func TestGitOpsAnswersWhatGitStopped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind tui.GitOpKind
		want []string
		note string
	}{
		{name: "carried on", kind: tui.OpContinue, want: []string{"rebase", "--continue"}, note: "carried the rebase on"},
		{name: "one commit left out", kind: tui.OpSkip, want: []string{"rebase", "--skip"}, note: "left one commit of the rebase out"},
		{name: "put back", kind: tui.OpAbort, want: []string{"rebase", "--abort"}, note: "the rebase is undone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, f, _ := opRunner(t)
			rebaseRunning(t, o.Dir)
			st := opState()
			st.Progress = vcs.InProgress{Kind: vcs.OperationRebase, Branch: "main"}
			_, _, note := opRun(t, o, tui.GitOp{Kind: tc.kind, On: tui.OnRunning, Confirmed: true}, st)
			if !f.ran(tc.want) {
				t.Fatalf("it ran %v, none of them %v", f.argv(), tc.want)
			}
			if note != tc.note {
				t.Fatalf("the bar says %q, want %q", note, tc.note)
			}
		})
	}
}

// rebaseRunning leaves the state files behind that say a rebase stopped in
// the middle, which is what the answers to it read.
func rebaseRunning(t *testing.T, dir string) {
	t.Helper()
	at := filepath.Join(dir, ".git", "rebase-merge")
	if err := os.MkdirAll(at, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"head-name": "refs/heads/main\n",
		"onto":      opOIDB + "\n",
		"msgnum":    "2\n",
		"end":       "5\n",
	} {
		if err := os.WriteFile(filepath.Join(at, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGitOpsAsksBeforeItRuns holds every operation that asks to the form it
// puts up, and the command that form shows to the command the answer then
// runs, so what the user agreed to is what happens.
func TestGitOpsAsksBeforeItRuns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		op    tui.GitOp
		id    string
		ask   tui.GitAsk
		value string
		// shows is the command the form puts in front of the user, starting
		// with the binary; nil for an operation that runs more than one
		// command or is asked for before its command can be built.
		shows []string
		// merged is what git answers when the form asks whether a branch is
		// held by another.
		merged string
	}{
		{
			name:  "a file discarded",
			op:    tui.GitOp{Kind: tui.OpDiscard, On: tui.OnFile, Path: "a.txt"},
			id:    "discard-file",
			ask:   tui.AskConfirm,
			shows: []string{"git", "restore", "--worktree", "--", "a.txt"},
		},
		{
			name:  "every change discarded",
			op:    tui.GitOp{Kind: tui.OpDiscardAll},
			id:    "discard-all",
			ask:   tui.AskName,
			shows: []string{"git", "restore", "--worktree", "--", ":/"},
		},
		{
			name: "what is staged committed",
			op:   tui.GitOp{Kind: tui.OpCommit},
			id:   "commit",
			ask:  tui.AskText,
		},
		{
			name: "the last commit amended",
			op:   tui.GitOp{Kind: tui.OpAmend},
			id:   "amend",
			ask:  tui.AskConfirm,
		},
		{
			name:  "the message of HEAD written again",
			op:    tui.GitOp{Kind: tui.OpRewordHead},
			id:    "reword-head",
			ask:   tui.AskText,
			value: "the newest commit",
		},
		{
			name: "a branch opened at a commit",
			op:   tui.GitOp{Kind: tui.OpBranchHere, On: tui.OnCommit, Rev: opOIDB},
			id:   "branch",
			ask:  tui.AskText,
		},
		{
			name:  "a branch renamed",
			op:    tui.GitOp{Kind: tui.OpRename, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Rev: "refs/heads/side"},
			id:    "rename-branch",
			ask:   tui.AskText,
			value: "side",
		},
		{
			name:   "a branch another one already holds",
			op:     tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Rev: "refs/heads/side"},
			id:     "delete-branch",
			ask:    tui.AskConfirm,
			shows:  []string{"git", "branch", "--delete", "--", "side"},
			merged: "side\n",
		},
		{
			name:  "a branch holding commits of its own",
			op:    tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Rev: "refs/heads/side"},
			id:    "delete-branch",
			ask:   tui.AskName,
			shows: []string{"git", "branch", "--delete", "--", "side"},
		},
		{
			name:  "a tag deleted",
			op:    tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefTag, Name: "v1.0.0", Rev: "refs/tags/v1.0.0"},
			id:    "delete-tag",
			ask:   tui.AskConfirm,
			shows: []string{"git", "tag", "--delete", "--", "v1.0.0"},
		},
		{
			name:  "a stash dropped",
			op:    tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefStash, Name: "stash@{0}", Index: 0},
			id:    "drop-stash",
			ask:   tui.AskConfirm,
			shows: []string{"git", "stash", "drop", "stash@{0}"},
		},
		{
			name: "a worktree removed",
			op:   tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefWorktree, Name: "task-a", Path: "/w/task-a"},
			id:   "remove-worktree",
			ask:  tui.AskConfirm,
		},
		{
			name:  "the branch moved and the working tree written over",
			op:    tui.GitOp{Kind: tui.OpResetHard, On: tui.OnCommit, Rev: opOIDB},
			id:    "reset-hard",
			ask:   tui.AskName,
			shows: []string{"git", "reset", "-q", "--hard", opOIDB, "--"},
		},
		{
			name: "a commit tagged",
			op:   tui.GitOp{Kind: tui.OpTag, On: tui.OnCommit, Rev: opOIDB},
			id:   "tag",
			ask:  tui.AskText,
		},
		{
			name: "a commit tagged with a message",
			op:   tui.GitOp{Kind: tui.OpTagAnnotated, On: tui.OnCommit, Rev: opOIDB},
			id:   "tag-name",
			ask:  tui.AskText,
		},
		{
			name: "a worktree opened",
			op:   tui.GitOp{Kind: tui.OpWorktreeAdd, On: tui.OnCommit, Rev: opOIDB},
			id:   "worktree",
			ask:  tui.AskText,
		},
		{
			name:  "the branch pushed over the one the remote holds",
			op:    tui.GitOp{Kind: tui.OpPushLease},
			id:    "force-push",
			ask:   tui.AskName,
			shows: []string{"git", "push", "--force-with-lease=main:" + opOIDB, "--", "origin", "main"},
		},
		{
			name:  "a commit reworded",
			op:    tui.GitOp{Kind: tui.OpReword, On: tui.OnCommit, Rev: opOIDB},
			id:    "reword",
			ask:   tui.AskText,
			value: "the one before",
		},
		{
			name: "a commit dropped",
			op:   tui.GitOp{Kind: tui.OpDrop, On: tui.OnCommit, Rev: opOIDB},
			id:   "drop-commit",
			ask:  tui.AskConfirm,
		},
		{
			name: "a commit folded into the one before",
			op:   tui.GitOp{Kind: tui.OpSquash, On: tui.OnCommit, Rev: opOIDB},
			id:   "squash-commit",
			ask:  tui.AskConfirm,
		},
		{
			name: "a commit folded in, keeping the other message",
			op:   tui.GitOp{Kind: tui.OpFixup, On: tui.OnCommit, Rev: opOIDB},
			id:   "fixup-commit",
			ask:  tui.AskConfirm,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, f, _ := opRunner(t)
			f.out["branch"] = gitResultOf(tc.merged)
			form, again, note := opRun(t, o, tc.op, opState())
			if note != "" {
				t.Fatalf("it ran without asking and said %q", note)
			}
			if form.ID != tc.id || form.Ask != tc.ask {
				t.Fatalf("the form is %q asking %v, want %q asking %v", form.ID, form.Ask, tc.id, tc.ask)
			}
			if form.Value != tc.value {
				t.Fatalf("the form holds %q, want %q", form.Value, tc.value)
			}
			if !slices.Equal(form.Command, tc.shows) {
				t.Fatalf("the form shows %v, want %v", form.Command, tc.shows)
			}
			if len(f.argv()) != 0 && tc.shows == nil {
				t.Fatalf("asking ran %v", f.argv())
			}
			if tc.shows == nil {
				return
			}
			// What the form showed is what the answer runs.
			again.Confirmed = true
			if _, _, note = opRun(t, o, again, opState()); note == "" {
				t.Fatal("the answer put another form up")
			}
			if !f.ran(tc.shows[1:]) {
				t.Fatalf("the answer ran %v, none of them what the form showed: %v", f.argv(), tc.shows[1:])
			}
		})
	}
}

// TestGitOpsAsksTwiceForATagWithAMessage collects the name, then the message,
// and writes the tag once both are in.
func TestGitOpsAsksTwiceForATagWithAMessage(t *testing.T) {
	t.Parallel()
	o, f, _ := opRunner(t)
	st := opState()
	form, op, _ := opRun(t, o, tui.GitOp{Kind: tui.OpTagAnnotated, On: tui.OnCommit, Rev: opOIDB}, st)
	if form.ID != "tag-name" {
		t.Fatalf("it asks %q first, want the name", form.ID)
	}
	op.Confirmed, op.Text = true, "v2.0.0"
	form, op, note := opRun(t, o, op, st)
	if form.ID != "tag-message" {
		t.Fatalf("it asks %q next, want the message (%q)", form.ID, note)
	}
	if op.Name != "v2.0.0" || op.Text != "" || op.Confirmed {
		t.Fatalf("the operation carried on as %+v, want the name held and the message still to come", op)
	}
	if !strings.Contains(form.Question, "v2.0.0") {
		t.Fatalf("the second form asks %q, want the name it already has", form.Question)
	}
	op.Confirmed, op.Text = true, "what this release is"
	if _, _, note = opRun(t, o, op, st); note != "tagged 22222222 as v2.0.0" {
		t.Fatalf("the bar says %q", note)
	}
	if !f.ran([]string{"tag", "--annotate", "--file=<message>", "--cleanup=whitespace", "--", "v2.0.0", opOIDB}) {
		t.Fatalf("it ran %v", f.argv())
	}
	if f.message != "what this release is" {
		t.Fatalf("the tag carries %q", f.message)
	}
}

// TestGitOpsWritesTheMessageItWasGiven holds a commit to the message that was
// typed, which goes over a file rather than over the command line.
func TestGitOpsWritesTheMessageItWasGiven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		op   tui.GitOp
		want string
	}{
		{
			name: "a commit",
			op:   tui.GitOp{Kind: tui.OpCommit, Text: "what this change does", Confirmed: true},
			want: "what this change does",
		},
		{
			name: "the message of HEAD",
			op:   tui.GitOp{Kind: tui.OpRewordHead, Text: "what it should have said", Confirmed: true},
			want: "what it should have said",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, f, _ := opRunner(t)
			opRun(t, o, tc.op, opState())
			if f.message != tc.want {
				t.Fatalf("the message written is %q, want %q", f.message, tc.want)
			}
		})
	}
}

// TestGitOpsRefuses says why an operation does not stand rather than running
// something else, and runs nothing at all while it says so.
func TestGitOpsRefuses(t *testing.T) {
	t.Parallel()
	detached := opState()
	detached.Changes.Head = vcs.Head{OID: opOIDA, Detached: true}
	clean := opState()
	clean.Changes.Files = nil
	cases := []struct {
		name string
		op   tui.GitOp
		st   tui.GitState
		want string
		bare bool
	}{
		{
			name: "a file operation with no file under the cursor",
			op:   tui.GitOp{Kind: tui.OpStage, On: tui.OnFile},
			want: "stand on a file first",
		},
		{
			name: "a stash checked out",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnRef, RefKind: tui.GitRefStash, Name: "stash@{0}", Index: 0},
			want: "a stash is popped with p",
		},
		{
			name: "a worktree checked out",
			op:   tui.GitOp{Kind: tui.OpCheckout, On: tui.OnRef, RefKind: tui.GitRefWorktree, Name: "task-a", Path: "/w/task-a"},
			want: "a worktree is a directory of its own",
		},
		{
			name: "a branch of a remote deleted",
			op:   tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefRemote, Name: "origin/main"},
			want: "goes with a push that deletes it",
		},
		{
			name: "the branch the working tree stands on deleted",
			op:   tui.GitOp{Kind: tui.OpDelete, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "main", Rev: "refs/heads/main"},
			want: "move off it first",
		},
		{
			name: "a tag renamed",
			op:   tui.GitOp{Kind: tui.OpRename, On: tui.OnRef, RefKind: tui.GitRefTag, Name: "v1.0.0"},
			want: "only a branch is renamed here",
		},
		{
			name: "a branch of this project followed",
			op:   tui.GitOp{Kind: tui.OpSetUpstream, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side"},
			want: "a branch follows a branch of a remote",
		},
		{
			name: "a stash merged in",
			op:   tui.GitOp{Kind: tui.OpMerge, On: tui.OnRef, RefKind: tui.GitRefStash, Name: "stash@{0}", Index: 0},
			want: "a stash is applied, not merged",
		},
		{
			name: "a worktree rebased onto",
			op:   tui.GitOp{Kind: tui.OpRebase, On: tui.OnRef, RefKind: tui.GitRefWorktree, Name: "task-a"},
			want: "a worktree is a directory, not a commit",
		},
		{
			name: "a stash popped with no stash under the cursor",
			op:   tui.GitOp{Kind: tui.OpStashPop, On: tui.OnRef, RefKind: tui.GitRefBranch, Name: "side", Index: -1},
			want: "stand on a stash first",
		},
		{
			name: "a commit with nothing staged",
			op:   tui.GitOp{Kind: tui.OpCommit, Text: "a message", Confirmed: true},
			st:   clean,
			want: "nothing is staged",
		},
		{
			name: "everything discarded in a working tree holding nothing",
			op:   tui.GitOp{Kind: tui.OpDiscardAll, Confirmed: true},
			st:   clean,
			want: "the working tree holds no change",
		},
		{
			name: "a push with HEAD on no branch",
			op:   tui.GitOp{Kind: tui.OpPush},
			st:   detached,
			want: "HEAD is on no branch",
		},
		{
			name: "an operation carried on that is not running",
			op:   tui.GitOp{Kind: tui.OpContinue, On: tui.OnRunning},
			want: "git is in the middle of nothing",
		},
		{
			name: "a commit operation with no commit under the cursor",
			op:   tui.GitOp{Kind: tui.OpCherryPick, On: tui.OnCommit},
			want: "stand on a commit first",
		},
		{
			name: "a history rewritten with no binary to write the plan",
			op:   tui.GitOp{Kind: tui.OpDrop, On: tui.OnCommit, Rev: opOIDB, Confirmed: true},
			bare: true,
			want: "no binary here",
		},
		{
			name: "a patch with nowhere to write it",
			op:   tui.GitOp{Kind: tui.OpPatch, On: tui.OnCommit, Rev: opOIDB},
			bare: true,
			want: "nowhere to write a patch",
		},
		{
			name: "an operation that does not exist",
			op:   tui.GitOp{Kind: tui.GitOpKind(-1)},
			want: "there is no such operation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, f, _ := opRunner(t)
			if tc.bare {
				o.Bin, o.Patches = "", ""
			}
			st := tc.st
			if st.At.IsZero() && st.Changes.Head.OID == "" {
				st = opState()
			}
			form, _, note := opRun(t, o, tc.op, st)
			if form.Title != "" {
				t.Fatalf("it put a form up: %q", form.Title)
			}
			if !strings.Contains(note, tc.want) {
				t.Fatalf("the bar says %q, want it to carry %q", note, tc.want)
			}
			if len(f.argv()) != 0 {
				t.Fatalf("it ran %v, want nothing run at all", f.argv())
			}
		})
	}
}

// TestGitOpsReportsWhatGitSaid keeps a failure to one line of the bar and
// asks for no new reading, since nothing changed.
func TestGitOpsReportsWhatGitSaid(t *testing.T) {
	t.Parallel()
	o, f, _ := opRunner(t)
	f.fail = errors.New("fatal: the index is locked\nand nothing was written")
	read := 0
	o.Refresh = func() { read++ }
	_, _, note := opRun(t, o, tui.GitOp{Kind: tui.OpStage, On: tui.OnFile, Path: "a.txt"}, opState())
	if !strings.HasPrefix(note, "stage the file: ") {
		t.Fatalf("the bar says %q, want the operation it was", note)
	}
	if strings.Contains(note, "\n") || !strings.Contains(note, "the index is locked") {
		t.Fatalf("the bar says %q, want one line of what git said", note)
	}
	if read != 0 {
		t.Fatalf("it asked for %d readings after a failure, want none", read)
	}
}

// TestGitOpsReadsAgainAfterAChange is how the regions catch up with what the
// operation just did.
func TestGitOpsReadsAgainAfterAChange(t *testing.T) {
	t.Parallel()
	o, _, _ := opRunner(t)
	read := 0
	o.Refresh = func() { read++ }
	opRun(t, o, tui.GitOp{Kind: tui.OpStage, On: tui.OnFile, Path: "a.txt"}, opState())
	if read != 1 {
		t.Fatalf("it asked for %d readings, want one", read)
	}
}

// TestGitOpsWithoutARemoteNamed reaches the one git reaches by itself.
func TestGitOpsWithoutARemoteNamed(t *testing.T) {
	t.Parallel()
	o, f, _ := opRunner(t)
	o.Remote = ""
	opRun(t, o, tui.GitOp{Kind: tui.OpFetch}, opState())
	if !f.ran([]string{"fetch", "--prune", "--tags", "--", "origin"}) {
		t.Fatalf("it ran %v", f.argv())
	}
}

// TestGitStripGlobals leaves a command that does not carry them alone.
func TestGitStripGlobals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "the arguments every command carries",
			argv: []string{"--no-pager", "-c", "core.fsmonitor=false", "-C", "/p", "status"},
			want: []string{"status"},
		},
		{
			name: "nothing but the arguments every command carries",
			argv: []string{"--no-pager", "-c", "core.fsmonitor=false", "-C", "/p"},
			want: []string{},
		},
		{
			name: "a command of its own",
			argv: []string{"status", "--short"},
			want: []string{"status", "--short"},
		},
		{
			name: "a long command that carries none of them",
			argv: []string{"log", "--oneline", "--graph", "-n", "20"},
			want: []string{"log", "--oneline", "--graph", "-n", "20"},
		},
		{
			name: "one that starts the same way and stops short",
			argv: []string{"--no-pager", "-c", "core.fsmonitor=false"},
			want: []string{"--no-pager", "-c", "core.fsmonitor=false"},
		},
		{name: "nothing at all", argv: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := gitStripGlobals(tc.argv); !slices.Equal(got, tc.want) {
				t.Fatalf("gitStripGlobals(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// TestGitFirstLine keeps a note to one line of the bar.
func TestGitFirstLine(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{name: "one line", in: "it went wrong", want: "it went wrong"},
		{name: "a page of output", in: "\n  first\nsecond\n", want: "first"},
		{name: "nothing but blanks", in: " \n\t\n", want: " \n\t\n"},
		{name: "nothing at all", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := gitFirstLine(tc.in); got != tc.want {
				t.Fatalf("gitFirstLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// gitResultOf is what git printed, for a command the test answers itself.
func gitResultOf(out string) git.Result { return git.Result{Stdout: []byte(out)} }

// TestGitOpsReadsTheState holds the small readings the messages and the forms
// are written from.
func TestGitOpsReadsTheState(t *testing.T) {
	t.Parallel()
	st := opState()
	detached := opState()
	detached.Changes.Head = vcs.Head{OID: opOIDA, Detached: true}
	empty := tui.GitState{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "an object name is cut down", got: gitShort(opOIDA), want: "11111111"},
		{name: "a ref is left as it stands", got: gitShort("refs/heads/main"), want: "refs/heads/main"},
		{name: "a short name is left alone", got: gitShort("1111"), want: "1111"},
		{
			name: "a row of the refs pane is named",
			got:  gitWhere(tui.GitOp{On: tui.OnRef, Name: "side", Rev: "refs/heads/side"}),
			want: "side",
		},
		{
			name: "a commit is named by its object name",
			got:  gitWhere(tui.GitOp{On: tui.OnCommit, Rev: opOIDB}),
			want: "22222222",
		},
		{
			name: "a row with no name of its own falls back to what it points at",
			got:  gitWhere(tui.GitOp{On: tui.OnRef, Rev: opOIDC}),
			want: "33333333",
		},
		{name: "the branch the working tree is on", got: gitHead(st), want: "main"},
		{name: "a working tree on no branch", got: gitHead(detached), want: "11111111"},
		{name: "a repository with no commit at all", got: gitHead(empty), want: "HEAD"},
		{name: "a branch of a remote without its remote", got: gitLocalName("origin/main"), want: "main"},
		{name: "a branch of a remote with a name in parts", got: gitLocalName("origin/feat/x"), want: "feat/x"},
		{name: "a name with no remote in front of it", got: gitLocalName("main"), want: "main"},
		{name: "one of something", got: gitCount(1, "file is", "files are"), want: "1 file is"},
		{name: "none of something", got: gitCount(0, "file is", "files are"), want: "0 files are"},
		{name: "several of something", got: gitCount(3, "file is", "files are"), want: "3 files are"},
		{name: "the subject of a commit that was read", got: gitSubject(st, opOIDB), want: "the one before"},
		{name: "the subject of a commit that was not", got: gitSubject(st, "nope"), want: ""},
		{name: "what a stash says of itself", got: gitStashSubject(st, 0), want: "what was put aside"},
		{name: "a stash that is not in the list", got: gitStashSubject(st, 7), want: ""},
		{name: "the commit the remote was read at", got: gitUpstreamOID(st), want: opOIDB},
		{name: "a branch following nothing", got: gitUpstreamOID(detached), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("it reads %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestGitOpsReadsTheWorkingTree tells a file git tracks from one it does not
// and a branch that is here from one that is not, which is what picks the
// command in both cases.
func TestGitOpsReadsTheWorkingTree(t *testing.T) {
	t.Parallel()
	st := opState()
	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{name: "a changed file is tracked", got: gitUntracked(st, "a.txt")},
		{name: "an untracked file", got: gitUntracked(st, "new.txt"), want: true},
		{name: "a file the working tree does not hold", got: gitUntracked(st, "nope.txt")},
		{name: "a branch that is here", got: gitHasBranch(st, "side"), want: true},
		{name: "a branch that is not", got: gitHasBranch(st, "theirs")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("it reads %v, want %v", tc.got, tc.want)
			}
		})
	}
}

// TestGitOpsCountsWhatIsLeftBehind is what the form that moves a branch says
// the move costs.
func TestGitOpsCountsWhatIsLeftBehind(t *testing.T) {
	t.Parallel()
	st := opState()
	cases := []struct {
		name string
		rev  string
		want int
	}{
		{name: "the commit the branch is on", rev: opOIDA},
		{name: "one commit back", rev: opOIDB, want: 1},
		{name: "two commits back", rev: opOIDC, want: 2},
		{name: "a commit the history that was read does not reach", rev: "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := gitLeftBehind(st, tc.rev); got != tc.want {
				t.Fatalf("it counts %d left behind, want %d", got, tc.want)
			}
		})
	}
}

// TestGitOpsDisplayReportsNothingItCouldNotBuild leaves the form with no
// command rather than with a wrong one.
func TestGitOpsDisplayReportsNothingItCouldNotBuild(t *testing.T) {
	t.Parallel()
	o, _, _ := opRunner(t)
	got := o.display(t.Context(), func(context.Context, git.Runner) error { return errors.New("it never got that far") })
	if got != nil {
		t.Fatalf("the form would show %v, want nothing at all", got)
	}
}

// TestGitOpsDisplayNamesTheBinaryItRuns says git, or whatever git is here.
func TestGitOpsDisplayNamesTheBinaryItRuns(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, bin, want string }{
		{name: "git from the path", want: "git"},
		{name: "a git of its own", bin: "/opt/homebrew/bin/git", want: "git"},
		{name: "one under another name", bin: "/usr/local/bin/git-2.44", want: "git-2.44"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, _, _ := opRunner(t)
			o.Runner.Bin = tc.bin
			got := o.display(t.Context(), func(ctx context.Context, r git.Runner) error {
				return r.StageAll(ctx, o.Dir)
			})
			if len(got) == 0 || got[0] != tc.want {
				t.Fatalf("the form shows %v, want it to start with %q", got, tc.want)
			}
		})
	}
}
