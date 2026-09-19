package team

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Slug is the transformation Claude Code applies to a team name or a task
// identifier before it becomes a path: everything outside letters, digits,
// underscore and dash becomes a dash. Two names that differ only in those
// characters land in the same directory, which is the product's behavior and
// not something to correct here.
func Slug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Status is where a task stands. A value from a release that knows more than
// this one is kept as it was read rather than folded into one of these.
type Status string

// The states Claude Code writes.
const (
	StatusPending Status = "pending"
	StatusRunning Status = "in_progress"
	StatusDone    Status = "completed"
)

// MaxTaskSize bounds one task file. A task is a subject, a description and two
// lists of identifiers; past this it is not a file the product wrote.
const MaxTaskSize = 1 << 20

// Task is one entry of the list the team shares.
type Task struct {
	ID      string
	Subject string
	// Description is the full text the lead wrote for whoever takes the task.
	Description string
	// ActiveForm is the present-tense label to show while the task runs.
	ActiveForm string
	// Owner is the agent holding the task, empty while nobody has claimed it.
	Owner  string
	Status Status
	// Blocks are the tasks that wait on this one, BlockedBy the tasks this one
	// waits on. Both are identifiers, and either may name a task that no longer
	// exists.
	Blocks    []string
	BlockedBy []string
}

// Label is what to show for a task: the present-tense form while it runs, the
// subject otherwise.
func (t Task) Label() string {
	if t.Status == StatusRunning && t.ActiveForm != "" {
		return t.ActiveForm
	}
	return t.Subject
}

// Done reports a task that is finished.
func (t Task) Done() bool { return t.Status == StatusDone }

// TaskID returns the identifier a task file name carries. Claude Code names a
// task file after its identifier, and keeps files of its own in the same
// directory (the high water mark of the identifiers, a lock beside a file it is
// writing), so a name that is not a plain <id>.json is refused, and so is a
// name that is a path rather than an entry of the directory.
func TaskID(file string) (string, bool) {
	if file == "" || strings.ContainsAny(file, `/\`) {
		return "", false
	}
	id, ok := strings.CutSuffix(file, ".json")
	if !ok || id == "" || strings.HasPrefix(id, ".") {
		return "", false
	}
	return id, true
}

type wireTask struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	ActiveForm  string   `json:"activeForm"`
	Owner       string   `json:"owner"`
	Status      string   `json:"status"`
	Blocks      []string `json:"blocks"`
	BlockedBy   []string `json:"blockedBy"`
}

// ParseTask decodes one task file. A task with no identifier cannot be claimed,
// blocked or completed by anything, so it is refused rather than shown.
func ParseTask(data []byte) (Task, error) {
	if len(data) > MaxTaskSize {
		return Task{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	var w wireTask
	if err := json.Unmarshal(data, &w); err != nil {
		return Task{}, fmt.Errorf("decode task: %w", err)
	}
	if w.ID == "" {
		return Task{}, ErrNoTaskID
	}
	return Task{
		ID:          w.ID,
		Subject:     w.Subject,
		Description: w.Description,
		ActiveForm:  w.ActiveForm,
		Owner:       w.Owner,
		Status:      Status(w.Status),
		Blocks:      w.Blocks,
		BlockedBy:   w.BlockedBy,
	}, nil
}

// SortTasks orders tasks the way Claude Code numbers them: by identifier read
// as a number, and by text for an identifier that is not one, which keeps the
// order of a list from a release that numbers tasks differently stable.
func SortTasks(tasks []Task) {
	slices.SortStableFunc(tasks, func(a, b Task) int {
		na, aerr := strconv.ParseInt(a.ID, 10, 64)
		nb, berr := strconv.ParseInt(b.ID, 10, 64)
		switch {
		case aerr == nil && berr == nil:
			return cmp.Compare(na, nb)
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// WaitingOn returns the tasks of the list that this one waits on and that are
// not finished, in the order the task names them. A blocker the list no longer
// holds is not one: the task it named is gone, so nothing is waited on.
func WaitingOn(t Task, list []Task) []string {
	if len(t.BlockedBy) == 0 {
		return nil
	}
	open := make(map[string]bool, len(list))
	for _, other := range list {
		if !other.Done() {
			open[other.ID] = true
		}
	}
	var out []string
	for _, id := range t.BlockedBy {
		if open[id] {
			out = append(out, id)
		}
	}
	return out
}

// Counts is what the footer of the sidebar shows.
type Counts struct {
	Total   int
	Pending int
	Running int
	Done    int
	// Blocked counts the tasks waiting on a task that is not finished. A
	// blocked task is also pending, so the four states do not add up to Total
	// and are not meant to.
	Blocked int
}

// Count summarizes a task list.
func Count(list []Task) Counts {
	c := Counts{Total: len(list)}
	open := make(map[string]bool, len(list))
	for _, t := range list {
		if !t.Done() {
			open[t.ID] = true
		}
	}
	for _, t := range list {
		switch t.Status {
		case StatusPending:
			c.Pending++
		case StatusRunning:
			c.Running++
		case StatusDone:
			c.Done++
		}
		if t.Done() {
			continue
		}
		for _, id := range t.BlockedBy {
			if open[id] {
				c.Blocked++
				break
			}
		}
	}
	return c
}
