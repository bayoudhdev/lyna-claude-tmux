package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// GitOps carries out the operations the git workstation asks for. The
// workstation says what was asked for and what it applies to; this decides
// which git command that is and which form stands before it, so the view runs
// nothing and the commands are all written down in one place.
type GitOps struct {
	Runner git.Runner
	// Dir is the working tree every command runs in.
	Dir string
	// Bin is what git runs as the sequence editor of a rewritten history,
	// which is this binary under the command that writes the plan.
	Bin string
	// Remote is what fetch, pull and push reach.
	Remote string
	// Patches is the directory a patch is written to.
	Patches string
	// Copy puts text where the user can paste it. It is nil where there is
	// nowhere to put it, and the operation then says so rather than
	// reporting that it worked.
	Copy func(ctx context.Context, text string) error
	// Refresh asks the reader for a new reading, once an operation that
	// changed the repository has finished.
	Refresh func()
}

// gitAction is one operation worked out: what it runs, the form that stands
// before it and what the bar says once it has run.
type gitAction struct {
	run  func(ctx context.Context, r git.Runner) error
	form tui.GitForm
	// again is the operation the form is answered for, which is the one that
	// was asked for unless the answer only takes it one step further.
	again *tui.GitOp
	done  string
	// steps says the operation runs more than one command, so no single one
	// of them stands for it on the form that asks.
	steps bool
}

// Run answers one operation of the workstation: the form that stands before
// it while it has not been answered, the command itself once it has.
func (o GitOps) Run(ctx context.Context, op tui.GitOp, st tui.GitState) tea.Cmd {
	a, err := o.action(ctx, op, st)
	if err != nil {
		return gitNote("%s: %s", op.Kind, err)
	}
	if a.form.Title != "" {
		form, next := a.form, op
		if a.again != nil {
			next = *a.again
		}
		// A form that collects a line is asked before the command can be
		// built, so there is nothing yet to show it running.
		if form.Ask != tui.AskText && a.run != nil && !a.steps {
			form.Command = o.display(ctx, a.run)
		}
		return func() tea.Msg { return tui.GitAskMsg{Form: form, Op: next} }
	}
	if a.run == nil {
		return gitNote("%s", a.done)
	}
	run, done := a.run, a.done
	return func() tea.Msg {
		if err := run(ctx, o.Runner); err != nil {
			return tui.GitWorkNote(fmt.Sprintf("%s: %s", op.Kind, gitFirstLine(err.Error())))
		}
		if o.Refresh != nil {
			o.Refresh()
		}
		return tui.GitWorkNote(done)
	}
}

// errGitDisplay stops a command that is only being built, so nothing is run
// to find out what would be.
var errGitDisplay = errors.New("the command was built, not run")

// gitGlobals are what the runner puts before every command of its own,
// followed by the directory. A form leaves them out: they say nothing about
// the operation.
var gitGlobals = []string{"--no-pager", "-c", "core.fsmonitor=false", "-C"}

// display is the command an operation runs, for the form that stands before
// it. The runner builds it against an executor that records the arguments and
// runs nothing, so what the form shows cannot drift from what the operation
// then does.
func (o GitOps) display(ctx context.Context, run func(ctx context.Context, r git.Runner) error) []string {
	var argv []string
	r := o.Runner
	r.Executor = git.ExecutorFunc(func(_ context.Context, _ string, args, _ []string, _ int64) (git.Result, error) {
		if argv == nil {
			argv = slices.Clone(args)
		}
		return git.Result{}, errGitDisplay
	})
	_ = run(ctx, r)
	if argv == nil {
		return nil
	}
	name := "git"
	if o.Runner.Bin != "" {
		name = filepath.Base(o.Runner.Bin)
	}
	return append([]string{name}, gitStripGlobals(argv)...)
}

// gitStripGlobals drops the arguments every command of the runner carries.
func gitStripGlobals(argv []string) []string {
	n := len(gitGlobals)
	if len(argv) <= n || !slices.Equal(argv[:n], gitGlobals) {
		return argv
	}
	return argv[n+1:]
}

// gitNote is one line for the command bar.
func gitNote(format string, args ...any) tea.Cmd {
	text := fmt.Sprintf(format, args...)
	return func() tea.Msg { return tui.GitWorkNote(text) }
}

// gitFirstLine is the first line of what git had to say, so a failure is one
// line of the bar rather than a page of output.
func gitFirstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return s
}

// gitAsk puts a form before an action while it has not been answered.
func gitAsk(op tui.GitOp, form tui.GitForm, a gitAction) gitAction {
	if !op.Confirmed {
		a.form = form
	}
	return a
}

// remote is what the operations that reach out use.
func (o GitOps) remote() string {
	if o.Remote == "" {
		return git.DefaultRemote
	}
	return o.Remote
}

// action works out one operation. The groups are the ones the keys are
// grouped in, so a key and what it runs are read side by side.
func (o GitOps) action(ctx context.Context, op tui.GitOp, st tui.GitState) (gitAction, error) {
	switch op.Kind {
	case tui.OpStage, tui.OpStageAll, tui.OpUnstage, tui.OpUnstageAll,
		tui.OpDiscard, tui.OpDiscardAll, tui.OpCommit, tui.OpAmend, tui.OpRewordHead:
		return o.treeAction(op, st)
	case tui.OpCheckout, tui.OpBranchHere, tui.OpRename, tui.OpDelete, tui.OpSetUpstream,
		tui.OpMerge, tui.OpRebase, tui.OpStashPop, tui.OpStashApply, tui.OpWorktreeAdd:
		return o.refAction(ctx, op, st)
	case tui.OpCherryPick, tui.OpRevert, tui.OpResetSoft, tui.OpResetMixed, tui.OpResetHard,
		tui.OpTag, tui.OpTagAnnotated, tui.OpPatch, tui.OpCopyOID:
		return o.commitAction(op, st)
	case tui.OpReword, tui.OpDrop, tui.OpSquash, tui.OpFixup, tui.OpMoveUp, tui.OpMoveDown:
		return o.historyAction(op, st)
	case tui.OpFetch, tui.OpPull, tui.OpPush, tui.OpPushLease, tui.OpPushUpstream:
		return o.remoteAction(op, st)
	case tui.OpContinue, tui.OpSkip, tui.OpAbort:
		return o.progressAction(op, st)
	}
	return gitAction{}, errors.New("there is no such operation")
}

// treeAction is the working tree and the commit in front of you.
func (o GitOps) treeAction(op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	switch op.Kind {
	case tui.OpStage:
		path, err := gitPath(op)
		if err != nil {
			return gitAction{}, err
		}
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Stage(ctx, dir, path) },
			done: "staged " + path,
		}, nil
	case tui.OpStageAll:
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.StageAll(ctx, dir) },
			done: "staged every change",
		}, nil
	case tui.OpUnstage:
		path, err := gitPath(op)
		if err != nil {
			return gitAction{}, err
		}
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Unstage(ctx, dir, path) },
			done: "unstaged " + path,
		}, nil
	case tui.OpUnstageAll:
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.UnstageAll(ctx, dir) },
			done: "unstaged everything",
		}, nil
	case tui.OpDiscard:
		path, err := gitPath(op)
		if err != nil {
			return gitAction{}, err
		}
		run := func(ctx context.Context, r git.Runner) error { return r.Discard(ctx, dir, path) }
		if gitUntracked(st, path) {
			run = func(ctx context.Context, r git.Runner) error { return r.CleanUntracked(ctx, dir, path) }
		}
		return gitAsk(op, tui.DiscardFileForm(path, nil), gitAction{
			run: run, done: "discarded " + path,
		}), nil
	case tui.OpDiscardAll:
		if st.Changes.Clean() {
			return gitAction{}, errors.New("the working tree holds no change")
		}
		return gitAsk(op, tui.DiscardAllForm(gitHead(st), len(st.Changes.Files), nil), gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.DiscardAll(ctx, dir) },
			done: "the tracked files are back to what HEAD holds",
		}), nil
	case tui.OpCommit:
		if st.Changes.Staged() == 0 {
			return gitAction{}, errors.New("nothing is staged")
		}
		message := op.Text
		return gitAsk(op, tui.TextForm("commit", "Commit what is staged",
			gitCount(st.Changes.Staged(), "file is", "files are")+" staged on "+gitHead(st)+".",
			"a message", true, nil), gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				_, err := r.Commit(ctx, dir, git.CommitOptions{Message: message})
				return err
			},
			done:  "committed on " + gitHead(st),
			steps: true,
		}), nil
	case tui.OpAmend:
		if len(st.Commits) == 0 {
			return gitAction{}, errors.New("there is no commit to amend")
		}
		last := st.Commits[0]
		return gitAsk(op, tui.ConfirmForm("amend", "Amend the last commit",
			"what is staged goes into "+last.Short()+" "+last.Subject+".",
			[]string{"the commit it replaces is reachable only through the reflog"}, nil), gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				_, err := r.Commit(ctx, dir, git.CommitOptions{Amend: true, Keep: true})
				return err
			},
			done:  "amended " + last.Short(),
			steps: true,
		}), nil
	case tui.OpRewordHead:
		if len(st.Commits) == 0 {
			return gitAction{}, errors.New("there is no commit to reword")
		}
		last, message := st.Commits[0], op.Text
		form := tui.TextForm("reword-head", "Edit the message of HEAD",
			last.Short()+" keeps everything it holds and takes a new message.", "a message", true, nil)
		form.Value = last.Subject
		return gitAsk(op, form, gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				_, err := r.Commit(ctx, dir, git.CommitOptions{Message: message, Amend: true})
				return err
			},
			done:  "reworded " + last.Short(),
			steps: true,
		}), nil
	}
	return gitAction{}, errors.New("there is no such operation")
}

// refAction is a row of the refs pane, and the commit under the cursor for
// the keys that mean the same thing in the history.
func (o GitOps) refAction(ctx context.Context, op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	switch op.Kind {
	case tui.OpCheckout:
		return o.checkoutAction(op, st)
	case tui.OpBranchHere:
		if op.Rev == "" {
			return gitAction{}, errors.New("there is nothing to branch from")
		}
		start, at, name := op.Rev, gitWhere(op), op.Text
		return gitAsk(op, tui.TextForm("branch", "Open a branch here",
			"the branch starts at "+at+" and the working tree moves onto it.",
			"a name", true, vcs.ValidateRefName), gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Checkout(ctx, dir, git.Checkout{Branch: name, New: true, Start: start})
			},
			done: "on " + name + ", from " + at,
		}), nil
	case tui.OpRename:
		if op.On != tui.OnRef || op.RefKind != tui.GitRefBranch {
			return gitAction{}, errors.New("only a branch is renamed here")
		}
		from, to := op.Name, op.Text
		form := tui.TextForm("rename-branch", "Rename a branch",
			from+" is renamed; what it points at does not move.", "a name", true, vcs.ValidateRefName)
		form.Value = from
		return gitAsk(op, form, gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.RenameBranch(ctx, dir, from, to, false) },
			done: from + " is now " + to,
		}), nil
	case tui.OpDelete:
		return o.deleteAction(ctx, op, st)
	case tui.OpSetUpstream:
		if op.On != tui.OnRef || op.RefKind != tui.GitRefRemote {
			return gitAction{}, errors.New("a branch follows a branch of a remote, so stand on that one")
		}
		head, up := gitHead(st), op.Name
		if st.Changes.Head.Name == "" {
			return gitAction{}, errors.New("HEAD is on no branch")
		}
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.SetUpstream(ctx, dir, head, up) },
			done: head + " follows " + up,
		}, nil
	case tui.OpMerge:
		rev, err := gitRev(op)
		if err != nil {
			return gitAction{}, err
		}
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Merge(ctx, dir, git.Merge{Rev: rev}) },
			done: "merged " + gitWhere(op) + " into " + gitHead(st),
		}, nil
	case tui.OpRebase:
		rev, err := gitRev(op)
		if err != nil {
			return gitAction{}, err
		}
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Rebase(ctx, dir, git.Rebase{Upstream: rev, AutoStash: true})
			},
			done: gitHead(st) + " is replayed onto " + gitWhere(op),
		}, nil
	case tui.OpStashPop, tui.OpStashApply:
		if op.On != tui.OnRef || op.RefKind != tui.GitRefStash || op.Index < 0 {
			return gitAction{}, errors.New("stand on a stash first")
		}
		entry := op.Index
		if op.Kind == tui.OpStashApply {
			return gitAction{
				run:  func(ctx context.Context, r git.Runner) error { return r.StashApply(ctx, dir, entry, false) },
				done: "applied " + op.Name + ", which is still there",
			}, nil
		}
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.StashPop(ctx, dir, entry, false) },
			done: "popped " + op.Name,
		}, nil
	case tui.OpWorktreeAdd:
		return o.worktreeAction(op)
	}
	return gitAction{}, errors.New("there is no such operation")
}

// checkoutAction opens whatever the cursor stands on, which is a different
// command for every kind of row.
func (o GitOps) checkoutAction(op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	if op.On != tui.OnRef {
		if op.Rev == "" {
			return gitAction{}, errors.New("there is nothing to check out")
		}
		rev := op.Rev
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Checkout(ctx, dir, git.Checkout{Start: rev, Detach: true})
			},
			done: "the working tree is on " + gitShort(rev) + ", on no branch",
		}, nil
	}
	switch op.RefKind {
	case tui.GitRefBranch:
		name := op.Name
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Checkout(ctx, dir, git.Checkout{Branch: name}) },
			done: "on " + name,
		}, nil
	case tui.GitRefRemote:
		remote, local := op.Name, gitLocalName(op.Name)
		// A branch of that name is already here, so it is the one to stand
		// on: creating it again is refused, and following it twice is not
		// what was asked for.
		if gitHasBranch(st, local) {
			return gitAction{
				run: func(ctx context.Context, r git.Runner) error {
					return r.Checkout(ctx, dir, git.Checkout{Branch: local})
				},
				done: "on " + local + ", which already follows " + remote,
			}, nil
		}
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Checkout(ctx, dir, git.Checkout{Branch: local, New: true, Track: remote})
			},
			done: "on " + local + ", following " + remote,
		}, nil
	case tui.GitRefTag:
		name, rev := op.Name, op.Rev
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Checkout(ctx, dir, git.Checkout{Start: rev, Detach: true})
			},
			done: "the working tree is on " + name + ", on no branch",
		}, nil
	case tui.GitRefStash:
		return gitAction{}, errors.New("a stash is popped with p or applied with a")
	case tui.GitRefWorktree:
		return gitAction{}, errors.New("a worktree is a directory of its own, which this does not move into")
	}
	return gitAction{}, errors.New("there is nothing to check out")
}

// deleteAction gets rid of the row the cursor stands on, whatever it is.
func (o GitOps) deleteAction(ctx context.Context, op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	if op.On != tui.OnRef {
		return gitAction{}, errors.New("stand on a branch, a tag, a stash or a worktree first")
	}
	switch op.RefKind {
	case tui.GitRefBranch:
		name := op.Name
		if name == st.Changes.Head.Name {
			return gitAction{}, errors.New("the working tree is on it, so move off it first")
		}
		merged, err := o.Runner.Merged(ctx, dir, name, "HEAD")
		if err != nil {
			merged = false
		}
		return gitAsk(op, tui.DeleteBranchForm(name, merged, nil), gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.DeleteBranch(ctx, dir, name, false) },
			done: "deleted " + name,
		}), nil
	case tui.GitRefTag:
		name := op.Name
		return gitAsk(op, tui.ConfirmForm("delete-tag", "Delete a tag",
			name+" is deleted here; a tag of the same name on a remote is left alone.",
			nil, nil), gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.DeleteTag(ctx, dir, name) },
			done: "deleted " + name,
		}), nil
	case tui.GitRefStash:
		if op.Index < 0 {
			return gitAction{}, errors.New("stand on a stash first")
		}
		entry := op.Index
		return gitAsk(op, tui.DropStashForm(entry, gitStashSubject(st, entry), nil), gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.StashDrop(ctx, dir, entry) },
			done: "dropped " + op.Name,
		}), nil
	case tui.GitRefWorktree:
		name, path := op.Name, op.Path
		if name == "" {
			return gitAction{}, errors.New("this worktree has no name of its own")
		}
		return gitAsk(op, tui.RemoveWorktreeForm(name, path, false, nil), gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.RemoveWorktree(ctx, dir, name, false) },
			done:  "removed the worktree " + name,
			steps: true,
		}), nil
	case tui.GitRefRemote:
		return gitAction{}, errors.New("a branch of a remote goes with a push that deletes it")
	}
	return gitAction{}, errors.New("there is nothing to get rid of")
}

// worktreeAction opens a worktree on the branch or the commit under the
// cursor, under the worktree directory of the project.
func (o GitOps) worktreeAction(op tui.GitOp) (gitAction, error) {
	dir := o.Dir
	if op.Rev == "" {
		return gitAction{}, errors.New("there is nothing to open a worktree on")
	}
	req := git.AddWorktree{Start: op.Rev, Detach: true}
	if op.On == tui.OnRef && op.RefKind == tui.GitRefBranch {
		req = git.AddWorktree{Branch: op.Name, Checkout: true}
	}
	at := gitWhere(op)
	form := tui.TextForm("worktree", "Open a worktree",
		"a directory of its own, on "+at+".", "a name", true, vcs.ValidateWorktreeName)
	if op.Confirmed {
		req.Name = op.Text
	}
	return gitAsk(op, form, gitAction{
		run: func(ctx context.Context, r git.Runner) error {
			_, err := r.AddWorktree(ctx, dir, req)
			return err
		},
		done:  "opened the worktree " + op.Text + " on " + at,
		steps: true,
	}), nil
}

// commitAction is one commit of the history: what is done with it without
// rewriting what follows it.
func (o GitOps) commitAction(op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	if op.Rev == "" {
		return gitAction{}, errors.New("stand on a commit first")
	}
	rev, at := op.Rev, gitShort(op.Rev)
	switch op.Kind {
	case tui.OpCherryPick:
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.CherryPick(ctx, dir, git.Pick{Revs: []string{rev}})
			},
			done: "cherry picked " + at + " onto " + gitHead(st),
		}, nil
	case tui.OpRevert:
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Revert(ctx, dir, git.Pick{Revs: []string{rev}})
			},
			done: "reverted " + at + " on " + gitHead(st),
		}, nil
	case tui.OpResetSoft:
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Reset(ctx, dir, rev, git.ResetSoft) },
			done: gitHead(st) + " is at " + at + ", with what it held staged",
		}, nil
	case tui.OpResetMixed:
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Reset(ctx, dir, rev, git.ResetMixed) },
			done: gitHead(st) + " is at " + at + ", with what it held in the working tree",
		}, nil
	case tui.OpResetHard:
		return gitAsk(op, tui.ResetHardForm(gitHead(st), at, gitLeftBehind(st, rev), nil), gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Reset(ctx, dir, rev, git.ResetHard) },
			done: gitHead(st) + " is at " + at + ", and the working tree with it",
		}), nil
	case tui.OpTag:
		name := op.Text
		return gitAsk(op, tui.TextForm("tag", "Tag a commit",
			"the tag names "+at+".", "a name", true, vcs.ValidateRefName), gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Tag(ctx, dir, git.Tag{Name: name, Rev: rev}) },
			done: "tagged " + at + " as " + name,
		}), nil
	case tui.OpTagAnnotated:
		return o.annotatedTagAction(op)
	case tui.OpPatch:
		if o.Patches == "" {
			return gitAction{}, errors.New("there is nowhere to write a patch")
		}
		out := o.Patches
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				if err := fsx.EnsurePrivateDir(out); err != nil {
					return err
				}
				_, err := r.FormatPatch(ctx, dir, git.Patch{Revs: []string{rev + "^!"}, Dir: out})
				return err
			},
			done: "wrote the patch of " + at + " into " + out,
		}, nil
	case tui.OpCopyOID:
		if o.Copy == nil {
			return gitAction{}, errors.New("there is nowhere to copy it to")
		}
		copyTo := o.Copy
		return gitAction{
			run:  func(ctx context.Context, _ git.Runner) error { return copyTo(ctx, rev) },
			done: "copied " + at,
		}, nil
	}
	return gitAction{}, errors.New("there is no such operation")
}

// annotatedTagAction asks for the name, then for the message the tag carries,
// and the second answer is what writes the tag.
func (o GitOps) annotatedTagAction(op tui.GitOp) (gitAction, error) {
	dir, rev, at := o.Dir, op.Rev, gitShort(op.Rev)
	if op.Name == "" {
		name := tui.TextForm("tag-name", "Tag a commit with a message",
			"the tag names "+at+", and carries a message of its own.", "a name", true, vcs.ValidateRefName)
		if !op.Confirmed {
			return gitAction{form: name}, nil
		}
		// The name has been given: ask for the message it carries, for the
		// same operation one step further along.
		next := op
		next.Name, next.Text, next.Confirmed = op.Text, "", false
		message := tui.TextForm("tag-message", "Tag a commit with a message",
			op.Text+" names "+at+".", "a message", true, nil)
		return gitAction{form: message, again: &next}, nil
	}
	name, message := op.Name, op.Text
	return gitAction{
		run: func(ctx context.Context, r git.Runner) error {
			return r.Tag(ctx, dir, git.Tag{Name: name, Rev: rev, Message: message})
		},
		done: "tagged " + at + " as " + name,
	}, nil
}

// historyAction rewrites the history from the commit under the cursor, which
// every one of these runs as a rebase with a plan of our own.
func (o GitOps) historyAction(op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	if op.Rev == "" {
		return gitAction{}, errors.New("stand on a commit first")
	}
	if o.Bin == "" {
		return gitAction{}, errors.New("there is no binary here to write the plan of a rebase")
	}
	at := gitShort(op.Rev)
	e := git.Edit{OID: op.Rev, Bin: o.Bin, AutoStash: true}
	lose := []string{"every commit from " + at + " onwards is written again, under names of its own"}
	switch op.Kind {
	case tui.OpReword:
		e.Message = op.Text
		form := tui.TextForm("reword", "Reword a commit",
			at+" keeps everything it holds and takes a new message.", "a message", true, nil)
		form.Value = gitSubject(st, op.Rev)
		return gitAsk(op, form, gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.Reword(ctx, dir, e) },
			done:  "reworded " + at,
			steps: true,
		}), nil
	case tui.OpDrop:
		return gitAsk(op, tui.ConfirmForm("drop-commit", "Drop a commit",
			at+" "+gitSubject(st, op.Rev)+" is taken out of the history.",
			append([]string{"what that commit changed"}, lose...), nil), gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.DropCommit(ctx, dir, e) },
			done:  "dropped " + at,
			steps: true,
		}), nil
	case tui.OpSquash:
		return gitAsk(op, tui.ConfirmForm("squash-commit", "Fold a commit into the one before",
			at+" joins the commit before it, and the two messages are joined.", lose, nil), gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.SquashCommit(ctx, dir, e) },
			done:  "folded " + at + " into the commit before it",
			steps: true,
		}), nil
	case tui.OpFixup:
		return gitAsk(op, tui.ConfirmForm("fixup-commit", "Fold a commit into the one before",
			at+" joins the commit before it, which keeps its own message.", lose, nil), gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.FixupCommit(ctx, dir, e) },
			done:  "folded " + at + " into the commit before it",
			steps: true,
		}), nil
	case tui.OpMoveUp, tui.OpMoveDown:
		e.By, e.Message = -1, ""
		where := "earlier"
		if op.Kind == tui.OpMoveDown {
			e.By, where = 1, "later"
		}
		return gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.MoveCommit(ctx, dir, e) },
			done:  "moved " + at + " one place " + where,
			steps: true,
		}, nil
	}
	return gitAction{}, errors.New("there is no such operation")
}

// remoteAction reaches the remote of the project.
func (o GitOps) remoteAction(op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir, remote := o.Dir, o.remote()
	switch op.Kind {
	case tui.OpFetch:
		return gitAction{
			run: func(ctx context.Context, r git.Runner) error {
				return r.Fetch(ctx, dir, git.Fetch{Remote: remote, Prune: true, Tags: true})
			},
			done: "fetched " + remote,
		}, nil
	case tui.OpPull:
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Pull(ctx, dir, git.Pull{Remote: remote}) },
			done: "pulled " + remote,
		}, nil
	case tui.OpPush, tui.OpPushLease, tui.OpPushUpstream:
		branch := st.Changes.Head.Name
		if branch == "" {
			return gitAction{}, errors.New("HEAD is on no branch, so there is nothing to push")
		}
		p := git.Push{Remote: remote, Branch: branch}
		done := "pushed " + branch + " to " + remote
		switch op.Kind {
		case tui.OpPushUpstream:
			p.SetUpstream = true
			done += ", which it now follows"
		case tui.OpPushLease:
			p.Lease, p.LeaseExpect = true, gitUpstreamOID(st)
			return gitAsk(op, tui.ForcePushForm(remote, branch, true, nil), gitAction{
				run:  func(ctx context.Context, r git.Runner) error { return r.Push(ctx, dir, p) },
				done: "wrote " + branch + " over the one on " + remote,
			}), nil
		}
		return gitAction{
			run:  func(ctx context.Context, r git.Runner) error { return r.Push(ctx, dir, p) },
			done: done,
		}, nil
	}
	return gitAction{}, errors.New("there is no such operation")
}

// progressAction answers what git stopped in the middle of.
func (o GitOps) progressAction(op tui.GitOp, st tui.GitState) (gitAction, error) {
	dir := o.Dir
	if !st.Progress.Running() {
		return gitAction{}, errors.New("git is in the middle of nothing")
	}
	what := st.Progress.Kind.String()
	switch op.Kind {
	case tui.OpContinue:
		return gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.Continue(ctx, dir) },
			done:  "carried the " + what + " on",
			steps: true,
		}, nil
	case tui.OpSkip:
		return gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.Skip(ctx, dir) },
			done:  "left one commit of the " + what + " out",
			steps: true,
		}, nil
	case tui.OpAbort:
		return gitAsk(op, tui.ConfirmForm("abort", "Put the branch back",
			"the "+what+" is undone and the branch goes back where it started.",
			[]string{"every conflict settled since it started"}, nil), gitAction{
			run:   func(ctx context.Context, r git.Runner) error { return r.Abort(ctx, dir) },
			done:  "the " + what + " is undone",
			steps: true,
		}), nil
	}
	return gitAction{}, errors.New("there is no such operation")
}

// gitOIDShort is how many characters of an object name a message shows.
const gitOIDShort = 8

// gitShort is an object name cut down to what a line has room for; anything
// that is not one is left as it stands, since a ref is already short.
func gitShort(rev string) string {
	if len(rev) > gitOIDShort && vcs.IsObjectName(rev) {
		return rev[:gitOIDShort]
	}
	return rev
}

// gitWhere is what an operation applies to, said the way the user sees it:
// the name of a row of the refs pane, the object name of a commit.
func gitWhere(op tui.GitOp) string {
	if op.On == tui.OnRef && op.Name != "" {
		return op.Name
	}
	return gitShort(op.Rev)
}

// gitRev is the revision a merge or a rebase reads, which a stash and a
// worktree are not.
func gitRev(op tui.GitOp) (string, error) {
	if op.On == tui.OnRef {
		switch op.RefKind {
		case tui.GitRefStash:
			return "", errors.New("a stash is applied, not merged")
		case tui.GitRefWorktree:
			return "", errors.New("a worktree is a directory, not a commit")
		}
	}
	if op.Rev == "" {
		return "", errors.New("there is nothing under the cursor")
	}
	return op.Rev, nil
}

// gitPath is the file an operation applies to.
func gitPath(op tui.GitOp) (string, error) {
	if op.Path == "" {
		return "", errors.New("stand on a file first")
	}
	return op.Path, nil
}

// gitHead is the branch the working tree is on, said in a way a message can
// carry when there is none.
func gitHead(st tui.GitState) string {
	if name := st.Changes.Head.Name; name != "" {
		return name
	}
	if st.Changes.Head.OID != "" {
		return gitShort(st.Changes.Head.OID)
	}
	return "HEAD"
}

// gitLocalName is what a branch of a remote is called here, which is its name
// without the remote in front of it.
func gitLocalName(name string) string {
	if _, local, ok := strings.Cut(name, "/"); ok {
		return local
	}
	return name
}

// gitHasBranch reports whether the project already holds a branch of that
// name.
func gitHasBranch(st tui.GitState, name string) bool {
	for _, b := range st.Refs.Branches {
		if b.Name == name {
			return true
		}
	}
	return false
}

// gitUntracked reports whether the working tree holds the path as a file git
// does not track, which is thrown away rather than restored.
func gitUntracked(st tui.GitState, path string) bool {
	for _, f := range st.Changes.Files {
		if f.Path == path {
			return f.Kind == vcs.KindUntracked
		}
	}
	return false
}

// gitSubject is the first line of a commit of the history that was read.
func gitSubject(st tui.GitState, rev string) string {
	for _, c := range st.Commits {
		if c.OID == rev {
			return c.Subject
		}
	}
	return ""
}

// gitStashSubject is what a stash entry says of itself.
func gitStashSubject(st tui.GitState, entry int) string {
	for _, s := range st.Refs.Stashes {
		if s.Index == entry {
			return s.Message
		}
	}
	return ""
}

// gitLeftBehind counts the commits a branch moved back to rev no longer
// reaches, from the history that was read.
func gitLeftBehind(st tui.GitState, rev string) int {
	for i, c := range st.Commits {
		if c.OID == rev {
			return i
		}
	}
	return 0
}

// gitUpstreamOID is the commit the remote branch was last read at, which is
// what a push holds its lease to.
func gitUpstreamOID(st tui.GitState) string {
	up := st.Changes.Head.Upstream
	if up == "" {
		return ""
	}
	for _, b := range st.Refs.Remotes {
		if b.Name == up || b.Ref == up {
			return b.OID
		}
	}
	return ""
}

// gitCount says how many of something there are, in a sentence.
func gitCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
