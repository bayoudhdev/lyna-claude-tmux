package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// ErrNothingToStash reports a push that found no change to put away. git says
// so and exits 0, which would otherwise read as a stash that was made.
var ErrNothingToStash = errors.New("git stash: nothing to put away")

// nothingToStash is what git prints in that case, in English since the
// environment forces it.
const nothingToStash = "No local changes to save"

// StashPush describes what to put away.
type StashPush struct {
	// Message is what the entry says, the commit HEAD stands on when empty.
	Message string
	// Untracked puts untracked files away as well, the ones the project
	// ignores excepted.
	Untracked bool
	// KeepIndex leaves what is staged in the working tree, so a partial
	// commit can be tested with the rest put away.
	KeepIndex bool
	// StagedOnly puts away what is staged and nothing else.
	StagedOnly bool
	// Paths puts away those paths alone.
	Paths []string
}

// StashPush puts the changes of the working tree away.
func (r Runner) StashPush(ctx context.Context, dir string, opt StashPush) error {
	args := []string{"stash", "push"}
	if opt.Message != "" {
		if err := checkStashMessage(opt.Message); err != nil {
			return err
		}
		args = append(args, "--message", opt.Message)
	}
	if opt.Untracked {
		args = append(args, "--include-untracked")
	}
	if opt.KeepIndex {
		args = append(args, "--keep-index")
	}
	if opt.StagedOnly {
		if opt.Untracked || len(opt.Paths) > 0 {
			return fmt.Errorf("git stash: what is staged is put away on its own")
		}
		args = append(args, "--staged")
	}
	args = append(args, "--")
	for _, p := range opt.Paths {
		if err := vcs.ValidatePath(p); err != nil {
			return fmt.Errorf("git stash: %w", err)
		}
		args = append(args, p)
	}
	out, err := r.git(ctx, dir, args...)
	if err != nil {
		return err
	}
	if strings.Contains(string(out), nothingToStash) {
		return ErrNothingToStash
	}
	return nil
}

// StashPop takes the entry back into the working tree and removes it. index
// restores what was staged as staged; without it everything comes back as a
// change of the working tree.
func (r Runner) StashPop(ctx context.Context, dir string, entry int, index bool) error {
	return r.stashEntry(ctx, dir, "pop", entry, index)
}

// StashApply takes the entry back into the working tree and keeps it in the
// list.
func (r Runner) StashApply(ctx context.Context, dir string, entry int, index bool) error {
	return r.stashEntry(ctx, dir, "apply", entry, index)
}

// StashDrop removes the entry from the list, which throws away what it holds.
func (r Runner) StashDrop(ctx context.Context, dir string, entry int) error {
	return r.stashEntry(ctx, dir, "drop", entry, false)
}

// StashClear removes every entry, which throws away everything they hold.
func (r Runner) StashClear(ctx context.Context, dir string) error {
	_, err := r.git(ctx, dir, "stash", "clear")
	return err
}

// StashFiles lists what an entry changes, with the lines it adds and removes,
// untracked files included.
func (r Runner) StashFiles(ctx context.Context, dir string, entry int) ([]vcs.NumStat, error) {
	ref, err := stashRef(entry)
	if err != nil {
		return nil, err
	}
	out, err := r.git(ctx, dir, "stash", "show", "--numstat", "-z", "--include-untracked",
		"--no-ext-diff", "--no-textconv", "--no-color", ref)
	if err != nil {
		return nil, err
	}
	return vcs.ParseNumstat(out)
}

func (r Runner) stashEntry(ctx context.Context, dir, verb string, entry int, index bool) error {
	ref, err := stashRef(entry)
	if err != nil {
		return err
	}
	args := []string{"stash", verb}
	if index {
		args = append(args, "--index")
	}
	_, err = r.git(ctx, dir, append(args, ref)...)
	return err
}

// stashRef names an entry the way git does, built from a number so no text of
// anyone's ever names what a command acts on.
func stashRef(entry int) (string, error) {
	if entry < 0 || entry >= vcs.MaxStashes {
		return "", fmt.Errorf("git stash: entry %d", entry)
	}
	return "stash@{" + strconv.Itoa(entry) + "}", nil
}

// checkStashMessage refuses what a message must never hold: a NUL byte, which
// no argument can carry, and more text than anyone types.
func checkStashMessage(msg string) error {
	if strings.ContainsRune(msg, 0) {
		return fmt.Errorf("git stash: the message holds a NUL byte")
	}
	if len(msg) > maxMessage {
		return fmt.Errorf("git stash: the message holds %d bytes, more than %d", len(msg), maxMessage)
	}
	return nil
}
