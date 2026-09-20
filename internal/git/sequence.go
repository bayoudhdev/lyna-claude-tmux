package git

import (
	"fmt"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// maxTodo caps the todo list read from a file. A list of a few thousand
// commands is a long rebase; more than this is not one.
const maxTodo = 1 << 20

// WriteTodo puts the plan at planPath into the todo file git is waiting on.
// git runs the sequence editor with the todo file as its last argument, so
// this is the whole of what the editor has to do: read a plan that was
// written before the rebase started, check it, and put it where git reads it.
func WriteTodo(planPath, todoPath string) error {
	plan, err := fsx.ReadFileNoFollow(planPath, maxTodo)
	if err != nil {
		return fmt.Errorf("rebase todo: read the plan: %w", err)
	}
	todo, err := vcs.ParseTodo(plan)
	if err != nil {
		return fmt.Errorf("rebase todo: %w", err)
	}
	if err := todo.Validate(); err != nil {
		return fmt.Errorf("rebase todo: %w", err)
	}
	// The file git wrote is read first, so a plan is never put over something
	// that is not the todo list of a rebase.
	current, err := fsx.ReadFileNoFollow(todoPath, maxTodo)
	if err != nil {
		return fmt.Errorf("rebase todo: read the list git wrote: %w", err)
	}
	waiting, err := vcs.ParseTodo(current)
	if err != nil {
		return fmt.Errorf("rebase todo: %s is no todo list: %w", todoPath, err)
	}
	var commits int
	for _, step := range waiting.Steps {
		if step.OID != "" {
			commits++
		}
	}
	if commits == 0 {
		return fmt.Errorf("rebase todo: %s names no commit, so it is not a list git is waiting on", todoPath)
	}
	if err := fsx.WriteFileAtomic(todoPath, todo.Render(), 0o600); err != nil {
		return fmt.Errorf("rebase todo: %w", err)
	}
	return nil
}

// SequenceEditor is the value of GIT_SEQUENCE_EDITOR that makes git hand the
// todo list to our own command. git runs the editor through a shell, so the
// paths are quoted for one, and nothing else is ever put in the string.
func SequenceEditor(bin, planPath string) string {
	return shellQuote(bin) + " rebase-todo " + shellQuote(planPath)
}

// shellQuote wraps a string so a POSIX shell reads it as the one word it is.
func shellQuote(s string) string {
	// Single quotes turn everything off but the single quote itself, which is
	// closed, escaped and opened again.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
