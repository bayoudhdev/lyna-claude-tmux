package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// rootRebase replays a branch from its first commit, which has no parent to
// start after.
const rootRebase = "--root"

// Replan describes a history to rewrite: the commits after Upstream are
// replayed through the plan.
type Replan struct {
	// Upstream is the commit the replay starts after, or rootRebase to replay
	// a branch from its first commit.
	Upstream string
	// Todo is the plan, oldest commit first.
	Todo vcs.Todo
	// Bin is the binary git runs as the sequence editor: our own, which puts
	// the plan where git reads it.
	Bin string
	// AutoStash puts the changes of the working tree away for the time of the
	// rebase.
	AutoStash bool
}

// Replay rewrites a history through a plan. git asks a sequence editor what
// to do; the plan is written before the rebase starts and our own command
// hands it over, so nothing opens and nothing is decided while git waits.
func (r Runner) Replay(ctx context.Context, dir string, p Replan) error {
	if err := p.Todo.Validate(); err != nil {
		return fmt.Errorf("git rebase: %w", err)
	}
	if p.Bin == "" {
		return fmt.Errorf("git rebase: no sequence editor was given")
	}
	if p.Upstream != rootRebase {
		if err := checkRev(p.Upstream); err != nil {
			return err
		}
	}
	plan, clean, err := planFile(p.Todo)
	if err != nil {
		return err
	}
	defer clean()
	args := []string{"rebase", "--interactive"}
	if p.AutoStash {
		args = append(args, "--autostash")
	}
	args = append(args, p.Upstream)
	_, err = r.gitEnv(ctx, dir, []string{"GIT_SEQUENCE_EDITOR=" + SequenceEditor(p.Bin, plan)}, args...)
	return err
}

// planFile writes a plan where only its owner reads it, and removes it as
// soon as the rebase is over.
func planFile(todo vcs.Todo) (string, func(), error) {
	dir, err := os.MkdirTemp("", "lmux-rebase-")
	if err != nil {
		return "", nil, fmt.Errorf("git rebase: %w", err)
	}
	clean := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "todo")
	if err := os.WriteFile(path, todo.Render(), 0o600); err != nil {
		clean()
		return "", nil, fmt.Errorf("git rebase: %w", err)
	}
	return path, clean, nil
}

// Edit describes one change to a history.
type Edit struct {
	// OID is the commit to act on.
	OID string
	// Bin is the binary git runs as the sequence editor.
	Bin string
	// Message is the message a reword writes.
	Message string
	// By is how many places a move shifts the commit, up the history when
	// negative, which is earlier.
	By int
	// AutoStash puts the changes of the working tree away for the time of it.
	AutoStash bool
}

// Reword writes another message on a commit of the history. The commit HEAD
// stands on is amended; an older one is replayed, stopped on, amended and the
// replay carried on, so no editor is ever opened.
func (r Runner) Reword(ctx context.Context, dir string, e Edit) error {
	if err := checkMessage(e.Message); err != nil {
		return err
	}
	head, err := r.Head(ctx, dir)
	if err != nil {
		return err
	}
	if head == e.OID {
		_, err := r.Commit(ctx, dir, CommitOptions{Message: e.Message, Amend: true})
		return err
	}
	if err := r.replayWith(ctx, dir, e, vcs.TodoEdit); err != nil {
		return err
	}
	// The replay stopped on the commit, or on something that went wrong; only
	// the state of the repository says which.
	state, err := r.InProgress(ctx, dir)
	if err != nil {
		return err
	}
	if state.Kind != vcs.OperationRebase || len(state.Heads) != 1 || state.Heads[0] != e.OID {
		return fmt.Errorf("git rebase: stopped on %+v rather than on %s", state, e.OID)
	}
	if _, err := r.Commit(ctx, dir, CommitOptions{Message: e.Message, Amend: true}); err != nil {
		return err
	}
	return r.Continue(ctx, dir)
}

// DropCommit leaves a commit out of the history.
func (r Runner) DropCommit(ctx context.Context, dir string, e Edit) error {
	return r.replayWith(ctx, dir, e, vcs.TodoDrop)
}

// SquashCommit folds a commit into the one before it, keeping both messages.
func (r Runner) SquashCommit(ctx context.Context, dir string, e Edit) error {
	return r.replayWith(ctx, dir, e, vcs.TodoSquash)
}

// FixupCommit folds a commit into the one before it, keeping the message of
// the one it lands in.
func (r Runner) FixupCommit(ctx context.Context, dir string, e Edit) error {
	return r.replayWith(ctx, dir, e, vcs.TodoFixup)
}

// MoveCommit replays a commit at another place of the history, earlier when
// By is negative.
func (r Runner) MoveCommit(ctx context.Context, dir string, e Edit) error {
	if e.By == 0 {
		return fmt.Errorf("git rebase: a commit moved nowhere")
	}
	base, todo, err := r.planFor(ctx, dir, e.OID)
	if err != nil {
		return err
	}
	// A move earlier in the history is a move up the list, which is written
	// oldest first.
	moved, err := todo.Moved(e.OID, e.By)
	if err != nil {
		return err
	}
	return r.Replay(ctx, dir, Replan{Upstream: base, Todo: moved, Bin: e.Bin, AutoStash: e.AutoStash})
}

func (r Runner) replayWith(ctx context.Context, dir string, e Edit, action vcs.TodoAction) error {
	base, todo, err := r.planFor(ctx, dir, e.OID)
	if err != nil {
		return err
	}
	next, err := todo.With(e.OID, action)
	if err != nil {
		return err
	}
	return r.Replay(ctx, dir, Replan{Upstream: base, Todo: next, Bin: e.Bin, AutoStash: e.AutoStash})
}

// planFor builds the plan that replays a history unchanged, with the commit
// the replay starts after. It reaches one commit further back than the one
// asked for, so folding a commit into the one before it and moving a commit
// earlier both have that commit in the list.
func (r Runner) planFor(ctx context.Context, dir, oid string) (string, vcs.Todo, error) {
	if !vcs.IsObjectName(oid) {
		return "", vcs.Todo{}, fmt.Errorf("git rebase: %q is no commit", oid)
	}
	commit, err := r.commitAt(ctx, dir, oid)
	if err != nil {
		return "", vcs.Todo{}, err
	}
	anchor := commit
	if len(commit.Parents) > 0 {
		if anchor, err = r.commitAt(ctx, dir, commit.Parents[0]); err != nil {
			return "", vcs.Todo{}, err
		}
	}
	commits, err := r.Log(ctx, dir, LogOptions{Revs: []string{anchor.OID + "..HEAD"}, FirstParent: true})
	if err != nil {
		return "", vcs.Todo{}, err
	}
	if len(commits) > 0 && commits[len(commits)-1].OID == anchor.OID {
		return "", vcs.Todo{}, fmt.Errorf("git rebase: %s is read twice", anchor.Short())
	}
	// The history reads newest first and the anchor is the oldest commit
	// replayed, so it goes at the end.
	todo := vcs.NewTodoFromHistory(append(commits, anchor))
	if todo.Index(oid) < 0 {
		return "", vcs.Todo{}, fmt.Errorf("git rebase: %s is not in the history of the branch", commit.Short())
	}
	base := anchor.OID + "^"
	if len(anchor.Parents) == 0 {
		base = rootRebase
	}
	return base, todo, nil
}

// commitAt reads one commit of the repository.
func (r Runner) commitAt(ctx context.Context, dir, oid string) (vcs.Commit, error) {
	commits, err := r.Log(ctx, dir, LogOptions{Max: 1, Revs: []string{oid}})
	if err != nil {
		return vcs.Commit{}, err
	}
	if len(commits) != 1 {
		return vcs.Commit{}, fmt.Errorf("git log: %s names no commit", oid)
	}
	return commits[0], nil
}
