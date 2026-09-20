package vcs

import (
	"fmt"
	"strings"
)

// MaxTodoSteps bounds a todo list. A rebase of more commits than this is not
// something a view lets anyone plan by hand.
const MaxTodoSteps = 4096

// TodoAction is what an interactive rebase does with a commit.
type TodoAction int

const (
	// TodoPick replays the commit as it is, TodoReword replays it with
	// another message, TodoEdit stops on it, TodoSquash and TodoFixup fold it
	// into the commit before it (keeping both messages, or only the first),
	// and TodoDrop leaves it out.
	TodoPick TodoAction = iota
	TodoReword
	TodoEdit
	TodoSquash
	TodoFixup
	TodoDrop
	// TodoOther is a command this does not model, kept as it was read so a
	// todo written by git survives a reading.
	TodoOther
)

// todoWords names every action git accepts, long form first.
var todoWords = map[TodoAction][2]string{
	TodoPick:   {"pick", "p"},
	TodoReword: {"reword", "r"},
	TodoEdit:   {"edit", "e"},
	TodoSquash: {"squash", "s"},
	TodoFixup:  {"fixup", "f"},
	TodoDrop:   {"drop", "d"},
}

// String is the word git reads the action as.
func (a TodoAction) String() string {
	if w, ok := todoWords[a]; ok {
		return w[0]
	}
	return "other"
}

// ParseTodoAction reads the word of an action, long or short.
func ParseTodoAction(word string) (TodoAction, bool) {
	for action, w := range todoWords {
		if word == w[0] || word == w[1] {
			return action, true
		}
	}
	return TodoOther, false
}

// TodoStep is one line of a todo list.
type TodoStep struct {
	Action TodoAction
	// OID is the commit the step acts on, empty for a step that names none.
	OID string
	// Subject is what the line says about the commit. Untrusted display data.
	Subject string
	// Raw is the line as it was read, kept for an action this does not model
	// so writing the list back changes nothing else.
	Raw string
}

// Todo is the plan an interactive rebase follows, oldest commit first.
type Todo struct {
	Steps []TodoStep
}

// NewTodoFromHistory builds a todo list from commits as a history reads them,
// newest first, since a todo list is written oldest first.
func NewTodoFromHistory(commits []Commit) Todo {
	steps := make([]TodoStep, 0, len(commits))
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		steps = append(steps, TodoStep{Action: TodoPick, OID: c.OID, Subject: c.Subject})
	}
	return Todo{Steps: steps}
}

// ParseTodo reads a todo list as git writes one: one command per line,
// comments and blank lines between them.
func ParseTodo(data []byte) (Todo, error) {
	var t Todo
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(t.Steps) == MaxTodoSteps {
			return Todo{}, fmt.Errorf("%w: a todo list of more than %d commands", ErrMalformed, MaxTodoSteps)
		}
		step, err := parseTodoStep(trimmed)
		if err != nil {
			return Todo{}, err
		}
		t.Steps = append(t.Steps, step)
	}
	return t, nil
}

func parseTodoStep(line string) (TodoStep, error) {
	word, rest, _ := strings.Cut(line, " ")
	action, known := ParseTodoAction(word)
	if !known {
		return TodoStep{Action: TodoOther, Raw: line}, nil
	}
	oid, subject, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if !IsObjectName(oid) {
		return TodoStep{}, fmt.Errorf("%w: todo command %q names %q", ErrMalformed, word, oid)
	}
	// git writes the subject behind a hash sign, and has not always.
	subject = strings.TrimPrefix(strings.TrimSpace(subject), "# ")
	return TodoStep{Action: action, OID: oid, Subject: subject}, nil
}

// Render writes the list the way git reads it.
func (t Todo) Render() []byte {
	var b strings.Builder
	for _, s := range t.Steps {
		if s.Action == TodoOther {
			b.WriteString(s.Raw + "\n")
			continue
		}
		b.WriteString(s.Action.String() + " " + s.OID)
		if s.Subject != "" {
			b.WriteString(" # " + s.Subject)
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// Index is where a commit sits in the list, -1 when it is not in it.
func (t Todo) Index(oid string) int {
	for i, s := range t.Steps {
		if s.OID != "" && s.OID == oid {
			return i
		}
	}
	return -1
}

// With returns the list with another action on one commit.
func (t Todo) With(oid string, action TodoAction) (Todo, error) {
	i := t.Index(oid)
	if i < 0 {
		return Todo{}, fmt.Errorf("%w: commit %q is not in the list", ErrMalformed, oid)
	}
	if action == TodoOther {
		return Todo{}, fmt.Errorf("%w: a command this does not model cannot be asked for", ErrMalformed)
	}
	steps := make([]TodoStep, len(t.Steps))
	copy(steps, t.Steps)
	steps[i].Action = action
	next := Todo{Steps: steps}
	if err := next.Validate(); err != nil {
		return Todo{}, err
	}
	return next, nil
}

// Moved returns the list with one commit moved by that many places, up the
// list when negative, which is what moving a commit earlier means.
func (t Todo) Moved(oid string, by int) (Todo, error) {
	i := t.Index(oid)
	if i < 0 {
		return Todo{}, fmt.Errorf("%w: commit %q is not in the list", ErrMalformed, oid)
	}
	to := i + by
	if to < 0 || to >= len(t.Steps) {
		return Todo{}, fmt.Errorf("%w: commit %q cannot move %d places from %d", ErrMalformed, oid, by, i)
	}
	// The step is taken out, then put back where it belongs, on a list of its
	// own so the one given is left as it was.
	rest := make([]TodoStep, 0, len(t.Steps)-1)
	rest = append(rest, t.Steps[:i]...)
	rest = append(rest, t.Steps[i+1:]...)
	moved := make([]TodoStep, 0, len(t.Steps))
	moved = append(moved, rest[:to]...)
	moved = append(moved, t.Steps[i])
	moved = append(moved, rest[to:]...)
	next := Todo{Steps: moved}
	if err := next.Validate(); err != nil {
		return Todo{}, err
	}
	return next, nil
}

// Validate refuses a list git would refuse or read as something else: a
// commit folded into nothing, a commit named twice, a list that replays
// nothing at all.
func (t Todo) Validate() error {
	if len(t.Steps) == 0 {
		return fmt.Errorf("%w: a todo list with no command in it", ErrMalformed)
	}
	if len(t.Steps) > MaxTodoSteps {
		return fmt.Errorf("%w: a todo list of %d commands", ErrMalformed, len(t.Steps))
	}
	seen := make(map[string]bool, len(t.Steps))
	var kept, replayed int
	for _, s := range t.Steps {
		if s.Action == TodoOther {
			if s.Raw == "" {
				return fmt.Errorf("%w: a command with nothing on the line", ErrMalformed)
			}
			kept++
			continue
		}
		if !IsObjectName(s.OID) {
			return fmt.Errorf("%w: todo command %s names %q", ErrMalformed, s.Action, s.OID)
		}
		if seen[s.OID] {
			return fmt.Errorf("%w: commit %q is in the list twice", ErrMalformed, s.OID)
		}
		seen[s.OID] = true
		switch s.Action {
		case TodoSquash, TodoFixup:
			if replayed == 0 {
				return fmt.Errorf("%w: %s %q has no commit before it to fold into", ErrMalformed, s.Action, s.OID)
			}
		case TodoDrop:
		case TodoPick, TodoReword, TodoEdit, TodoOther:
			replayed++
		}
		kept++
	}
	if replayed == 0 {
		return fmt.Errorf("%w: a todo list that replays no commit at all", ErrMalformed)
	}
	if kept == 0 {
		return fmt.Errorf("%w: a todo list with no command in it", ErrMalformed)
	}
	return nil
}
