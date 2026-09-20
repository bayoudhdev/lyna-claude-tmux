package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// AddWorktree describes a worktree to open. The name is the only thing a
// caller chooses: the path is built from it under the project's own worktree
// directory, so no command ever checks a project out somewhere else.
type AddWorktree struct {
	// Name is the directory the worktree takes, and the branch when none is
	// named.
	Name string
	// Branch is the branch to create there, the name itself when empty.
	Branch string
	// Start is the revision the branch starts at, HEAD when empty.
	Start string
	// Checkout takes Branch as a branch that already exists instead of
	// creating it.
	Checkout bool
	// Detach opens the worktree on Start with no branch at all.
	Detach bool
}

// AddWorktree opens a worktree of the repository containing dir and returns
// it as git then lists it.
func (r Runner) AddWorktree(ctx context.Context, dir string, req AddWorktree) (vcs.Worktree, error) {
	repo, err := r.Repo(ctx, dir)
	if err != nil {
		return vcs.Worktree{}, err
	}
	path, err := vcs.WorktreePath(repo.Root, req.Name)
	if err != nil {
		return vcs.Worktree{}, err
	}
	args := []string{"worktree", "add"}
	branch := req.Branch
	if branch == "" {
		branch = req.Name
	}
	switch {
	case req.Detach:
		args = append(args, "--detach")
	case req.Checkout:
		if err := vcs.ValidateRefName(branch); err != nil {
			return vcs.Worktree{}, fmt.Errorf("git worktree add: %w", err)
		}
	default:
		if err := vcs.ValidateRefName(branch); err != nil {
			return vcs.Worktree{}, fmt.Errorf("git worktree add: %w", err)
		}
		args = append(args, "-b", branch)
	}
	args = append(args, "--", path)
	switch {
	case req.Detach || !req.Checkout:
		if req.Start != "" {
			if err := checkRev(req.Start); err != nil {
				return vcs.Worktree{}, err
			}
			args = append(args, req.Start)
		}
	default:
		// Checking a branch out takes the branch as the revision, so a start
		// revision beside it would name two different commits.
		if req.Start != "" {
			return vcs.Worktree{}, fmt.Errorf("git worktree add: %s is checked out, which starts where it stands", branch)
		}
		args = append(args, branch)
	}
	if _, err := r.git(ctx, dir, args...); err != nil {
		return vcs.Worktree{}, err
	}
	list, err := r.Worktrees(ctx, dir)
	if err != nil {
		return vcs.Worktree{}, err
	}
	for _, w := range list {
		if sameDir(w.Path, path) {
			return w, nil
		}
	}
	return vcs.Worktree{}, fmt.Errorf("git worktree add: %s was not opened", path)
}

// RemoveWorktree removes the worktree of that name. force removes one holding
// changes, which throws them away: the caller confirms it, this only runs it.
func (r Runner) RemoveWorktree(ctx context.Context, dir, name string, force bool) error {
	repo, err := r.Repo(ctx, dir)
	if err != nil {
		return err
	}
	path, err := vcs.WorktreePath(repo.Root, name)
	if err != nil {
		return err
	}
	// Only a worktree the project opened is removed. The path is built under
	// the project's own worktree directory, so the working tree the project
	// lives in is never one of them.
	list, err := r.Worktrees(ctx, dir)
	if err != nil {
		return err
	}
	var found bool
	for _, w := range list {
		if sameDir(w.Path, path) {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("git worktree remove: %s is not a worktree of this project", path)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err = r.git(ctx, dir, append(args, "--", path)...)
	return err
}

// PruneWorktrees forgets the worktrees whose directories are gone. It removes
// no directory of its own.
func (r Runner) PruneWorktrees(ctx context.Context, dir string) error {
	_, err := r.git(ctx, dir, "worktree", "prune")
	return err
}

// sameDir compares two paths of the same machine, cleaned, since git prints
// the path it was given and a caller builds its own.
func sameDir(a, b string) bool {
	return strings.TrimSuffix(filepath.Clean(a), string(filepath.Separator)) ==
		strings.TrimSuffix(filepath.Clean(b), string(filepath.Separator))
}
