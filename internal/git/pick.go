package git

import (
	"context"
	"fmt"
	"strconv"
)

// maxParents bounds the parent a revert or a pick of a merge names. A merge
// of more branches than this is not something a view offers to undo.
const maxParents = 16

// Pick describes commits to replay onto the branch the working tree stands
// on, or to undo.
type Pick struct {
	// Revs are the commits, replayed in the order given.
	Revs []string
	// NoCommit leaves the changes staged instead of writing a commit, which
	// is how several commits are folded into one.
	NoCommit bool
	// Mainline is the parent a merge commit is replayed or undone against,
	// counting from one. It is needed for a merge and refused for anything
	// else, which git itself checks.
	Mainline int
	// Reference records where a picked commit came from, as a line in its
	// message.
	Reference bool
}

// CherryPick replays commits onto the branch the working tree stands on. One
// that stops on a conflict leaves the repository in the middle of it.
func (r Runner) CherryPick(ctx context.Context, dir string, p Pick) error {
	args, err := pickArgs("cherry-pick", p)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, args...)
	return err
}

// Revert writes commits that undo the ones named. It never opens an editor:
// the message git writes is kept, and amending it afterwards is a commit like
// any other.
func (r Runner) Revert(ctx context.Context, dir string, p Pick) error {
	if p.Reference {
		return fmt.Errorf("git revert: a revert names the commit it undoes already")
	}
	args, err := pickArgs("revert", p)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, append(args, "--no-edit")...)
	return err
}

func pickArgs(cmd string, p Pick) ([]string, error) {
	if len(p.Revs) == 0 {
		return nil, fmt.Errorf("git %s: no commit named", cmd)
	}
	args := []string{cmd}
	if p.NoCommit {
		args = append(args, "--no-commit")
	}
	if p.Mainline != 0 {
		if p.Mainline < 1 || p.Mainline > maxParents {
			return nil, fmt.Errorf("git %s: parent %d", cmd, p.Mainline)
		}
		args = append(args, "--mainline", strconv.Itoa(p.Mainline))
	}
	if p.Reference {
		args = append(args, "-x")
	}
	for _, rev := range p.Revs {
		if err := checkRev(rev); err != nil {
			return nil, err
		}
	}
	return append(args, p.Revs...), nil
}

// ResetMode is what a reset does with the index and the working tree.
type ResetMode int

const (
	// ResetMixed moves the branch and leaves the changes as changes of the
	// working tree. It is what a reset does when nothing else is said.
	ResetMixed ResetMode = iota
	// ResetSoft moves the branch alone, leaving everything staged.
	ResetSoft
	// ResetHard moves the branch and throws away everything that is not in
	// the commit, which cannot be undone from here.
	ResetHard
)

// String names a mode the way the view says it.
func (m ResetMode) String() string {
	switch m {
	case ResetMixed:
		return "mixed"
	case ResetSoft:
		return "soft"
	case ResetHard:
		return "hard"
	}
	return "unknown"
}

func (m ResetMode) flag() (string, error) {
	switch m {
	case ResetMixed:
		return "--mixed", nil
	case ResetSoft:
		return "--soft", nil
	case ResetHard:
		return "--hard", nil
	}
	return "", fmt.Errorf("git reset: mode %d", int(m))
}

// Reset moves the branch the working tree stands on to another commit. A hard
// reset throws away every change that is not in that commit: the caller
// confirms it, this only runs it.
func (r Runner) Reset(ctx context.Context, dir, rev string, mode ResetMode) error {
	if err := checkRev(rev); err != nil {
		return err
	}
	flag, err := mode.flag()
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, "reset", "-q", flag, rev, "--")
	return err
}
