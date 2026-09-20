package git

import (
	"context"
	"fmt"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// wholeTree is the pathspec for everything in the project, whatever directory
// the command runs in: a pane opened in a subdirectory stages the project and
// not the subdirectory it happens to sit in.
const wholeTree = ":/"

// Stage adds the paths to the index, deletions included.
func (r Runner) Stage(ctx context.Context, dir string, paths ...string) error {
	return r.overPaths(ctx, dir, paths, "add", "-A")
}

// StageAll adds every change of the project to the index.
func (r Runner) StageAll(ctx context.Context, dir string) error {
	return r.overPaths(ctx, dir, []string{wholeTree}, "add", "-A")
}

// Unstage takes the paths out of the index, leaving the working tree alone.
// It resets rather than restores, which is the one form that also works
// before the first commit, when there is no HEAD to restore from.
func (r Runner) Unstage(ctx context.Context, dir string, paths ...string) error {
	return r.overPaths(ctx, dir, paths, "reset", "-q")
}

// UnstageAll empties the index of every change, leaving the working tree
// alone.
func (r Runner) UnstageAll(ctx context.Context, dir string) error {
	_, err := r.git(ctx, dir, "reset", "-q")
	return err
}

// Discard throws the changes of the paths away, back to what the index holds.
// A file that is staged keeps what was staged: unstage it first to go back to
// the commit.
func (r Runner) Discard(ctx context.Context, dir string, paths ...string) error {
	return r.overPaths(ctx, dir, paths, "restore", "--worktree")
}

// DiscardAll throws every change of the working tree away.
func (r Runner) DiscardAll(ctx context.Context, dir string) error {
	return r.overPaths(ctx, dir, []string{wholeTree}, "restore", "--worktree")
}

// CleanUntracked deletes the untracked files under the paths, directories
// included. Files a project ignores are left where they are, since a build
// output or a local environment file is not a change to throw away.
func (r Runner) CleanUntracked(ctx context.Context, dir string, paths ...string) error {
	return r.overPaths(ctx, dir, paths, "clean", "-f", "-d", "-q")
}

// CleanAllUntracked deletes every untracked file of the project, those it
// ignores excepted.
func (r Runner) CleanAllUntracked(ctx context.Context, dir string) error {
	return r.overPaths(ctx, dir, []string{wholeTree}, "clean", "-f", "-d", "-q")
}

// overPaths runs one command over paths. The paths are checked and passed
// after the separator, so none of them is ever read as an option, and a
// command with no path at all is refused: it would act on the whole project
// where one file was meant.
func (r Runner) overPaths(ctx context.Context, dir string, paths []string, args ...string) error {
	if len(paths) == 0 {
		return fmt.Errorf("git %s: no path given", args[0])
	}
	for _, p := range paths {
		if p == wholeTree {
			continue
		}
		if err := vcs.ValidatePath(p); err != nil {
			return fmt.Errorf("git %s: %w", args[0], err)
		}
	}
	_, err := r.git(ctx, dir, append(append(args, "--"), paths...)...)
	return err
}
