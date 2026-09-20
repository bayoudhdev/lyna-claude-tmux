package git

import (
	"context"
	"fmt"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// Merge describes a merge to run.
type Merge struct {
	// Rev is what to merge into the branch the working tree stands on.
	Rev string
	// Message is the message of the merge commit, git's own when empty. It
	// goes through a file, as a commit message does.
	Message string
	// NoFastForward writes a merge commit although the branch could simply
	// move forward.
	NoFastForward bool
	// FastForwardOnly refuses anything but moving the branch forward.
	FastForwardOnly bool
	// Squash brings the changes in without recording the merge, leaving them
	// staged for a commit of their own.
	Squash bool
	// NoCommit merges and stops before the commit, which leaves the result to
	// be looked at first.
	NoCommit bool
}

// Merge brings a revision into the branch the working tree stands on. A merge
// that stops on a conflict leaves the repository in the middle of it, which
// InProgress reports and Continue or Abort answers.
func (r Runner) Merge(ctx context.Context, dir string, m Merge) error {
	if err := checkRev(m.Rev); err != nil {
		return err
	}
	if m.NoFastForward && m.FastForwardOnly {
		return fmt.Errorf("git merge: a merge either writes a commit or only moves forward")
	}
	if m.Squash && (m.NoFastForward || m.NoCommit) {
		return fmt.Errorf("git merge: a squash records no merge, so it neither writes one nor stops before it")
	}
	args := []string{"merge"}
	switch {
	case m.NoFastForward:
		args = append(args, "--no-ff")
	case m.FastForwardOnly:
		args = append(args, "--ff-only")
	}
	if m.Squash {
		args = append(args, "--squash")
	}
	if m.NoCommit {
		args = append(args, "--no-commit")
	}
	if m.Message != "" {
		if err := checkMessage(m.Message); err != nil {
			return err
		}
		file, clean, err := messageFile(m.Message)
		if err != nil {
			return err
		}
		defer clean()
		args = append(args, "--file="+file)
	}
	_, err := r.git(ctx, dir, append(args, m.Rev)...)
	return err
}

// Rebase describes a rebase to run.
type Rebase struct {
	// Upstream is what the commits are replayed over: a branch, a tag or a
	// commit.
	Upstream string
	// Onto replaces where they land, which moves a branch off the commits it
	// was built on.
	Onto string
	// Branch is what to replay, the branch the working tree stands on when
	// empty.
	Branch string
	// AutoStash puts the changes of the working tree away for the time of the
	// rebase and brings them back afterwards.
	AutoStash bool
	// KeepEmpty keeps the commits that end up changing nothing, which a
	// rebase drops by default.
	KeepEmpty bool
}

// Rebase replays commits over another commit. One that stops on a conflict
// leaves the repository in the middle of it, which InProgress reports.
func (r Runner) Rebase(ctx context.Context, dir string, rb Rebase) error {
	if err := checkRev(rb.Upstream); err != nil {
		return err
	}
	args := []string{"rebase"}
	if rb.Onto != "" {
		if err := checkRev(rb.Onto); err != nil {
			return err
		}
		args = append(args, "--onto", rb.Onto)
	}
	if rb.AutoStash {
		args = append(args, "--autostash")
	}
	if rb.KeepEmpty {
		args = append(args, "--empty=keep")
	}
	args = append(args, rb.Upstream)
	if rb.Branch != "" {
		if err := vcs.ValidateRefName(rb.Branch); err != nil {
			return fmt.Errorf("git rebase: %w", err)
		}
		args = append(args, rb.Branch)
	}
	_, err := r.git(ctx, dir, args...)
	return err
}

// Continue carries on with what the repository stopped in the middle of,
// once the conflicts are resolved and staged.
func (r Runner) Continue(ctx context.Context, dir string) error {
	return r.answer(ctx, dir, "--continue")
}

// Skip leaves out the commit the operation stopped on and carries on.
func (r Runner) Skip(ctx context.Context, dir string) error {
	return r.answer(ctx, dir, "--skip")
}

// Abort puts the repository back where it stood before the operation started.
func (r Runner) Abort(ctx context.Context, dir string) error {
	return r.answer(ctx, dir, "--abort")
}

// answer runs the answer to whatever is running, which only the state of the
// repository says: the same three words belong to five different commands.
func (r Runner) answer(ctx context.Context, dir, verb string) error {
	state, err := r.InProgress(ctx, dir)
	if err != nil {
		return err
	}
	cmd, err := operationCommand(state.Kind, verb)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, cmd, verb)
	return err
}

// operationCommand names the command that answers an operation, and refuses
// an answer the operation has no word for: a merge is carried on or given up,
// never skipped.
func operationCommand(kind vcs.Operation, verb string) (string, error) {
	var cmd string
	switch kind {
	case vcs.OperationMerge:
		cmd = "merge"
	case vcs.OperationRebase:
		cmd = "rebase"
	case vcs.OperationApply:
		cmd = "am"
	case vcs.OperationCherryPick:
		cmd = "cherry-pick"
	case vcs.OperationRevert:
		cmd = "revert"
	case vcs.OperationBisect, vcs.OperationNone:
		return "", fmt.Errorf("git %s: nothing is running", verb)
	default:
		return "", fmt.Errorf("git %s: nothing is running", verb)
	}
	if verb == "--skip" && cmd == "merge" {
		return "", fmt.Errorf("git merge: a merge is carried on or given up, never skipped")
	}
	return cmd, nil
}
