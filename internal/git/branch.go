package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// Checkout describes what to open in the working tree.
type Checkout struct {
	// Branch is the branch to switch to, the one created when New.
	Branch string
	// New creates Branch rather than switching to one that is already there.
	New bool
	// Start is where a new branch starts, or the commit to open with no
	// branch at all. HEAD when empty.
	Start string
	// Track is the branch of a remote a new branch follows, which is how a
	// branch of someone else's is opened.
	Track string
	// Detach opens Start with no branch on it.
	Detach bool
	// Discard throws away the changes of the working tree standing in the
	// way. Without it a checkout that would lose work is refused by git.
	Discard bool
}

// Checkout opens a branch or a commit in the working tree containing dir.
func (r Runner) Checkout(ctx context.Context, dir string, c Checkout) error {
	args, err := checkoutArgs(c)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, args...)
	return err
}

func checkoutArgs(c Checkout) ([]string, error) {
	args := []string{"switch"}
	switch {
	case c.Detach:
		if c.Branch != "" || c.New || c.Track != "" {
			return nil, fmt.Errorf("git switch: a commit is opened with no branch, so none is named")
		}
		if err := checkRev(c.Start); err != nil {
			return nil, err
		}
		args = append(args, "--detach")
	case c.New:
		if err := vcs.ValidateRefName(c.Branch); err != nil {
			return nil, fmt.Errorf("git switch: %w", err)
		}
		if c.Track != "" {
			if c.Start != "" {
				return nil, fmt.Errorf("git switch: a branch follows %s or starts at %s, not both", c.Track, c.Start)
			}
			if err := vcs.ValidateRefName(c.Track); err != nil {
				return nil, fmt.Errorf("git switch: %w", err)
			}
		}
		if c.Start != "" {
			if err := checkRev(c.Start); err != nil {
				return nil, err
			}
		}
		args = append(args, "--create", c.Branch)
		if c.Track != "" {
			args = append(args, "--track", c.Track)
		}
	default:
		if c.Start != "" || c.Track != "" {
			return nil, fmt.Errorf("git switch: a branch that is already there starts where it stands")
		}
		if err := vcs.ValidateRefName(c.Branch); err != nil {
			return nil, fmt.Errorf("git switch: %w", err)
		}
		// Without this git would open a branch of a remote under a name that
		// was never asked for; a branch of someone else's is followed on
		// purpose or not at all.
		args = append(args, "--no-guess")
	}
	if c.Discard {
		args = append(args, "--discard-changes")
	}
	switch {
	case c.Detach:
		args = append(args, c.Start)
	case c.New:
		if c.Start != "" {
			args = append(args, c.Start)
		}
	default:
		args = append(args, c.Branch)
	}
	return args, nil
}

// CreateBranch opens a branch at start without switching to it, HEAD when
// start is empty.
func (r Runner) CreateBranch(ctx context.Context, dir, name, start string) error {
	if err := vcs.ValidateRefName(name); err != nil {
		return fmt.Errorf("git branch: %w", err)
	}
	args := []string{"branch", "--", name}
	if start != "" {
		if err := checkRev(start); err != nil {
			return err
		}
		args = append(args, start)
	}
	_, err := r.git(ctx, dir, args...)
	return err
}

// RenameBranch renames a branch. force takes a name that is already used.
func (r Runner) RenameBranch(ctx context.Context, dir, from, to string, force bool) error {
	for _, name := range []string{from, to} {
		if err := vcs.ValidateRefName(name); err != nil {
			return fmt.Errorf("git branch: %w", err)
		}
	}
	flag := "--move"
	if force {
		flag = "-M"
	}
	_, err := r.git(ctx, dir, "branch", flag, "--", from, to)
	return err
}

// DeleteBranch removes a branch. force removes one holding commits no other
// branch holds, which loses them: the caller confirms it, this only runs it.
func (r Runner) DeleteBranch(ctx context.Context, dir, name string, force bool) error {
	if err := vcs.ValidateRefName(name); err != nil {
		return fmt.Errorf("git branch: %w", err)
	}
	flag := "--delete"
	if force {
		flag = "-D"
	}
	_, err := r.git(ctx, dir, "branch", flag, "--", name)
	return err
}

// Merged reports whether into already holds every commit of name, which is
// what tells a branch that can be deleted and lose nothing from one holding
// work of its own. It asks for the branch by name rather than by exit status,
// so a repository that has no such branch answers no instead of failing.
func (r Runner) Merged(ctx context.Context, dir, name, into string) (bool, error) {
	if err := vcs.ValidateRefName(name); err != nil {
		return false, fmt.Errorf("git branch --merged: %w", err)
	}
	if err := checkRev(into); err != nil {
		return false, fmt.Errorf("git branch --merged: %w", err)
	}
	out, err := r.git(ctx, dir, "branch", "--list", "--merged", into, "--format=%(refname:short)", name)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// SetUpstream makes branch follow upstream, a branch of a remote.
func (r Runner) SetUpstream(ctx context.Context, dir, branch, upstream string) error {
	for _, name := range []string{branch, upstream} {
		if err := vcs.ValidateRefName(name); err != nil {
			return fmt.Errorf("git branch: %w", err)
		}
	}
	_, err := r.git(ctx, dir, "branch", "--set-upstream-to="+upstream, "--", branch)
	return err
}

// UnsetUpstream leaves a branch following nothing.
func (r Runner) UnsetUpstream(ctx context.Context, dir, branch string) error {
	if err := vcs.ValidateRefName(branch); err != nil {
		return fmt.Errorf("git branch: %w", err)
	}
	_, err := r.git(ctx, dir, "branch", "--unset-upstream", "--", branch)
	return err
}
