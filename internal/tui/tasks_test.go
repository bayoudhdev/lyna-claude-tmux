package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// sampleTasks is the list the tests draw: read in no particular order, with
// a running task, a finished one, one blocked by a task still running, one
// blocked by a blocked task and by a task that no longer exists, one nobody
// has claimed, and an identifier past nine that sorts as a number.
func sampleTasks() []team.Task {
	return []team.Task{
		{
			ID: "3", Subject: "write the migration", Owner: "db-engineer", Status: team.StatusPending,
			BlockedBy: []string{"1", "2"},
		},
		{
			ID: "1", Subject: "design the schema", ActiveForm: "designing the schema", Owner: "api-developer",
			Status: team.StatusRunning,
			Description: "Design the tables the billing service needs.\n\n" +
				"Keep every amount in cents and every timestamp in UTC, and write down each index with the query it serves.",
		},
		{ID: "2", Subject: "review the plan", Owner: "security-auditor", Status: team.StatusDone},
		{ID: "4", Subject: "wire the router", Status: team.StatusPending},
		{
			ID: "5", Subject: "document the endpoints", Owner: "docs", Status: team.StatusPending,
			BlockedBy: []string{"3", "9"},
		},
		{ID: "10", Subject: "set up the fixtures", Owner: "api-developer", Status: team.StatusDone},
	}
}

// tasksUpdate is a reading delivered the way the view's own command delivers
// it.
func tasksUpdate(tasks []team.Task, err error) tasksUpdateMsg {
	return tasksUpdateMsg{update: TasksUpdate{Team: "session-8f3c1d2a", Tasks: tasks, Err: err}}
}

// newTasks builds a list of the given size with a reading already delivered.
func newTasks(t *testing.T, opts TasksOptions, tasks []team.Task) *TasksModel {
	t.Helper()
	if opts.Now == nil {
		opts.Now = clock
	}
	if opts.Styles.Text.String() == "" {
		opts.Styles = goldenStyles(t)
	}
	m := NewTasks(opts)
	apply(m, tasksUpdate(tasks, nil))
	return m
}

// TestTasksFrames pins the list at the sizes it is drawn at: the list of a
// team, the same list with the description of a task open, and the list in
// the ascii icon set.
func TestTasksFrames(t *testing.T) {
	for _, size := range sizes {
		for _, state := range []struct {
			name string
			msgs []tea.Msg
		}{
			{name: "list"},
			{name: "details", msgs: []tea.Msg{press("enter")}},
			{name: "blocked", msgs: []tea.Msg{press("down"), press("down")}},
		} {
			name := "tasks-" + state.name + "-" + size.name
			t.Run(name, func(t *testing.T) {
				m := newTasks(t, TasksOptions{Width: size.width, Height: size.height, Popup: true}, sampleTasks())
				drive(t, m, nil, state.msgs...)
				assertFrame(t, name, m, size.width, size.height, size.ansi)
			})
		}
	}
	t.Run("tasks-ascii", func(t *testing.T) {
		m := newTasks(t, TasksOptions{Width: 60, Height: 12, Styles: testStyles(t, "lyna", theme.Depth16, "ascii")}, sampleTasks())
		drive(t, m, nil, press("down"), press("down"))
		assertFrame(t, "tasks-ascii", m, 60, 12, false)
	})
}

// TestTasksStates draws the list in the states it has nothing to list in:
// before any reading, with no task at all, and with a reading that failed
// before a list was ever read.
func TestTasksStates(t *testing.T) {
	cases := []struct {
		name string
		msgs []tea.Msg
		want string
	}{
		{name: "tasks-reading", want: "reading the task list"},
		{name: "tasks-empty", msgs: []tea.Msg{tasksUpdate(nil, nil)}, want: "no shared task list yet"},
		{
			name: "tasks-no-team",
			msgs: []tea.Msg{tasksUpdateMsg{update: TasksUpdate{}}}, want: "no shared task list yet",
		},
		{
			name: "tasks-unreadable", msgs: []tea.Msg{tasksUpdate(nil, errors.New("open tasks: permission denied"))},
			want: "open tasks: permission denied",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewTasks(TasksOptions{Styles: goldenStyles(t), Width: 60, Height: 10, Now: clock, Popup: true})
			drive(t, m, nil, tc.msgs...)
			if !strings.Contains(plain(m), tc.want) {
				t.Fatalf("frame does not show %q:\n%s", tc.want, plain(m))
			}
			assertFrame(t, tc.name, m, 60, 10, false)
		})
	}
}

// TestTasksReadsItsChannel drives the list the way its program does: the
// first command waits for a reading, every reading is followed by another
// wait, and a watcher that stops is reported rather than waited on forever.
func TestTasksReadsItsChannel(t *testing.T) {
	ch := make(chan TasksUpdate, 1)
	m := NewTasks(TasksOptions{Styles: goldenStyles(t), Updates: ch, Width: 60, Height: 16, Now: clock})
	ch <- TasksUpdate{Team: "crew", Tasks: sampleTasks()}
	msg, ok := m.Init()().(tasksUpdateMsg)
	if !ok {
		t.Fatalf("the first command returned %T", msg)
	}
	next, cmd := m.Update(msg)
	if !strings.Contains(plain(next.(viewer)), "designing the schema") {
		t.Fatalf("the reading was not drawn:\n%s", plain(next.(viewer)))
	}
	close(ch)
	if cmd == nil {
		t.Fatal("the list stopped waiting after its first reading")
	}
	closed, ok := cmd().(tasksClosedMsg)
	if !ok {
		t.Fatalf("a closed channel is reported as %T", closed)
	}
	apply(m, closed)
	if !strings.Contains(plain(m), "watcher stopped") {
		t.Fatalf("a stopped watcher is not reported:\n%s", plain(m))
	}
	// A list with nothing to wait on waits for nothing.
	alone := NewTasks(TasksOptions{Styles: goldenStyles(t), Width: 60, Height: 16, Now: clock})
	if cmd := alone.Init(); cmd != nil {
		t.Fatalf("a list with no channel waits: %v", cmd())
	}
}

// TestTasksOrder draws the tasks in the order the team works through them,
// whatever order they were read in.
func TestTasksOrder(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 30}, sampleTasks())
	var ids []string
	for _, task := range m.tasks {
		ids = append(ids, task.ID)
	}
	if want := []string{"1", "2", "3", "4", "5", "10"}; !slices.Equal(ids, want) {
		t.Fatalf("the list reads %v, want %v", ids, want)
	}
}

// TestTasksHeader counts the list in the header: how many tasks are done, of
// how many, and how many wait on one that is not.
func TestTasksHeader(t *testing.T) {
	cases := []struct {
		name  string
		tasks []team.Task
		want  string
		lacks string
	}{
		{name: "a team with blocked tasks", tasks: sampleTasks(), want: "2 of 6 done, 2 blocked"},
		{
			name:  "a team with nothing blocked",
			tasks: []team.Task{{ID: "1", Status: team.StatusDone}, {ID: "2", Status: team.StatusPending}},
			want:  "1 of 2 done", lacks: "blocked",
		},
		{name: "a team with no task", want: "session-8f3c1d2a", lacks: "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTasks(t, TasksOptions{Width: 100, Height: 12}, tc.tasks)
			header := strings.SplitN(plain(m), "\n", 2)[0]
			if !strings.Contains(header, tc.want) || !strings.Contains(header, "session-8f3c1d2a") {
				t.Fatalf("the header reads %q, want %q and the team", header, tc.want)
			}
			if tc.lacks != "" && strings.Contains(header, tc.lacks) {
				t.Fatalf("the header reads %q, which says %q", header, tc.lacks)
			}
		})
	}
}

// TestTasksState reads where each task of the sample stands: a task waiting
// on an unfinished one is blocked, whatever Claude Code wrote.
func TestTasksState(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 30}, sampleTasks())
	running := team.Task{ID: "7", Status: team.StatusRunning, BlockedBy: []string{"3"}}
	cases := []struct {
		name string
		task team.Task
		want int
	}{
		{name: "a task nobody has started", task: team.Task{ID: "4", Status: team.StatusPending}, want: taskPending},
		{name: "a task at work", task: team.Task{ID: "1", Status: team.StatusRunning}, want: taskRunning},
		{name: "a finished task", task: team.Task{ID: "2", Status: team.StatusDone}, want: taskDone},
		{name: "a task waiting on a running one", task: team.Task{ID: "3", BlockedBy: []string{"1"}}, want: taskBlocked},
		{name: "a task at work that waits on another", task: running, want: taskBlocked},
		{name: "a task waiting on a finished one", task: team.Task{ID: "8", BlockedBy: []string{"2"}}, want: taskPending},
		{name: "a task waiting on one that is gone", task: team.Task{ID: "8", BlockedBy: []string{"99"}}, want: taskPending},
		{name: "a finished task that waited", task: team.Task{ID: "8", Status: team.StatusDone, BlockedBy: []string{"1"}}, want: taskDone},
		{name: "a state from a later release", task: team.Task{ID: "8", Status: "deferred"}, want: taskPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.state(tc.task); got != tc.want {
				t.Fatalf("state = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTasksGlyphs pins the glyph of every state in both icon sets.
func TestTasksGlyphs(t *testing.T) {
	cases := []struct {
		icons string
		want  []string
	}{
		{icons: "unicode", want: []string{"○", "◐", "✓", "⊘"}},
		{icons: "nerd", want: []string{"○", "◐", "✓", "⊘"}},
		{icons: "ascii", want: []string{"-", "*", "x", "!"}},
	}
	for _, tc := range cases {
		t.Run(tc.icons, func(t *testing.T) {
			m := NewTasks(TasksOptions{Styles: testStyles(t, "lyna", theme.DepthTrue, tc.icons)})
			var got []string
			for _, state := range []int{taskPending, taskRunning, taskDone, taskBlocked} {
				_, g := m.glyph(state)
				got = append(got, g)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("glyphs %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTasksBlockedBy names the tasks a task waits on: the unfinished ones, in
// the order the task names them.
func TestTasksBlockedBy(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 30}, sampleTasks())
	cases := []struct {
		name string
		task team.Task
		want string
	}{
		{name: "a task that waits on nothing", task: team.Task{ID: "4"}},
		{name: "one running task", task: team.Task{ID: "8", BlockedBy: []string{"1"}}, want: "blocked by #1"},
		{name: "two of them", task: team.Task{ID: "8", BlockedBy: []string{"4", "1"}}, want: "blocked by #4, #1"},
		{name: "a finished one is not waited on", task: team.Task{ID: "8", BlockedBy: []string{"2", "3"}}, want: "blocked by #3"},
		{name: "a task that is gone is not waited on", task: team.Task{ID: "8", BlockedBy: []string{"99"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.blockedBy(tc.task); got != tc.want {
				t.Fatalf("blockedBy = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTasksRows reads what each row carries on screen, and what a list too
// narrow for all of it gives up first.
func TestTasksRows(t *testing.T) {
	cases := []struct {
		name  string
		width int
		// line is the task whose row is read, has what it must show and lacks
		// what it must not.
		line  string
		has   []string
		lacks []string
	}{
		{
			name: "a running task shows what it is doing and who holds it", width: 100, line: "#1",
			has: []string{"◐", "designing the schema", "api-developer"}, lacks: []string{"blocked"},
		},
		{
			name: "a blocked task names what it waits on", width: 100, line: "#3",
			has: []string{"⊘", "write the migration", "blocked by #1", "db-engineer"},
		},
		{
			name: "a task blocked by a blocked one", width: 100, line: "#5",
			has: []string{"⊘", "blocked by #3", "docs"}, lacks: []string{"#9"},
		},
		{name: "a finished task", width: 100, line: "#2", has: []string{"✓", "review the plan"}},
		{name: "a task nobody holds", width: 100, line: "#4", has: []string{"○", "wire the router"}},
		{
			name: "a narrow list keeps the subject before the note", width: 40, line: "#3",
			has: []string{"write the migration", "db-engine"}, lacks: []string{"blocked", "db-engineer"},
		},
		{
			name: "the subject gives way to the note while it keeps its first cells", width: 60, line: "#5",
			has: []string{"document the endpoi…  blocked by #3", "docs"},
		},
		{
			name: "a list with no room for both drops the note", width: 40, line: "#5",
			has: []string{"document the endpoi…", "docs"}, lacks: []string{"blocked"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTasks(t, TasksOptions{Width: tc.width, Height: 30}, sampleTasks())
			row := rowOf(m, tc.line)
			for _, want := range tc.has {
				if !strings.Contains(row, want) {
					t.Fatalf("row %q lacks %q", row, want)
				}
			}
			for _, unwanted := range tc.lacks {
				if strings.Contains(row, unwanted) {
					t.Fatalf("row %q shows %q", row, unwanted)
				}
			}
		})
	}
}

// rowOf is the drawn row of the task with the identifier id: the row whose
// identifier column holds it, which a note naming the task does not.
func rowOf(m viewer, id string) string {
	for l := range strings.SplitSeq(plain(m), "\n") {
		for _, f := range strings.Fields(l) {
			if strings.HasPrefix(f, "#") {
				if f == id {
					return l
				}
				break
			}
		}
	}
	return ""
}

// TestTasksColumns measures the columns the rows share.
func TestTasksColumns(t *testing.T) {
	cases := []struct {
		name  string
		width int
		tasks []team.Task
		want  taskColumns
	}{
		{
			name: "the sample on a wide list", width: 100, tasks: sampleTasks(),
			want: taskColumns{id: 3, owner: 16, middle: 100 - 4 - 4 - 18},
		},
		{
			name: "an owner past the column is cut", width: 100,
			tasks: []team.Task{{ID: "1", Owner: strings.Repeat("o", 40)}},
			want:  taskColumns{id: 2, owner: tasksOwnerWidth, middle: 100 - 4 - 3 - tasksOwnerWidth - 2},
		},
		{
			name: "an identifier past the column is cut", width: 100,
			tasks: []team.Task{{ID: "0123456789abcdef"}},
			want:  taskColumns{id: tasksIDWidth, middle: 100 - 4 - tasksIDWidth - 1},
		},
		{
			name: "a narrow list gives the owner a quarter", width: 40, tasks: sampleTasks(),
			want: taskColumns{id: 3, owner: 10, middle: 40 - 4 - 4 - 12},
		},
		{
			name: "a list too narrow for an owner drops it", width: 28, tasks: sampleTasks(),
			want: taskColumns{id: 3, middle: 28 - 4 - 4},
		},
		{name: "no task at all", width: 60, want: taskColumns{middle: 60 - 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTasks(t, TasksOptions{Width: tc.width, Height: 20}, tc.tasks)
			if got := m.columns(); got != tc.want {
				t.Fatalf("columns = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestTasksKeys drives every key of the list and reads back where the cursor
// is, whether the description is open and whether the list closed.
func TestTasksKeys(t *testing.T) {
	cases := []struct {
		name  string
		popup bool
		msgs  []tea.Msg
		// wantCursor is the task the cursor ends on.
		wantCursor  string
		wantDetails bool
		wantQuit    bool
	}{
		{name: "the cursor starts on the first task", wantCursor: "1"},
		{name: "down moves to the next task", msgs: []tea.Msg{press("down")}, wantCursor: "2"},
		{name: "j moves down too", msgs: []tea.Msg{press("j"), press("j")}, wantCursor: "3"},
		{name: "up moves back", msgs: []tea.Msg{press("down"), press("down"), press("up")}, wantCursor: "2"},
		{name: "k moves back too", msgs: []tea.Msg{press("j"), press("k")}, wantCursor: "1"},
		{name: "up stops at the first task", msgs: []tea.Msg{press("up")}, wantCursor: "1"},
		{name: "G runs to the last task", msgs: []tea.Msg{press("G")}, wantCursor: "10"},
		{name: "g runs back to the first", msgs: []tea.Msg{press("G"), press("g")}, wantCursor: "1"},
		{name: "page down runs a page", msgs: []tea.Msg{press("pgdown")}, wantCursor: "10"},
		{name: "enter opens the description", msgs: []tea.Msg{press("enter")}, wantCursor: "1", wantDetails: true},
		{name: "enter again closes it", msgs: []tea.Msg{press("enter"), press("enter")}, wantCursor: "1"},
		{
			name: "the description follows the cursor", msgs: []tea.Msg{press("enter"), press("down")},
			wantCursor: "2", wantDetails: true,
		},
		{name: "q closes a popup", popup: true, msgs: []tea.Msg{press("q")}, wantCursor: "1", wantQuit: true},
		{name: "esc closes a popup", popup: true, msgs: []tea.Msg{press("esc")}, wantCursor: "1", wantQuit: true},
		{name: "q in a pane does nothing", msgs: []tea.Msg{press("q"), press("esc")}, wantCursor: "1"},
		{name: "ctrl+c always ends the program", msgs: []tea.Msg{press("ctrl+c")}, wantCursor: "1", wantQuit: true},
		{name: "a key of no meaning does nothing", msgs: []tea.Msg{press("x"), press("s")}, wantCursor: "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTasks(t, TasksOptions{Width: 100, Height: 12, Popup: tc.popup}, sampleTasks())
			r := drive(t, m, nil, tc.msgs...)
			got, ok := m.selected()
			if !ok || got.ID != tc.wantCursor {
				t.Fatalf("the cursor is on %+v, want #%s", got, tc.wantCursor)
			}
			if m.details != tc.wantDetails {
				t.Fatalf("details open %v, want %v", m.details, tc.wantDetails)
			}
			if r.quit != tc.wantQuit {
				t.Fatalf("quit = %v, want %v", r.quit, tc.wantQuit)
			}
		})
	}
}

// TestTasksKeysOnNothing leaves an empty list alone: there is no task to open,
// and the keys neither move nor open anything.
func TestTasksKeysOnNothing(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 60, Height: 12}, nil)
	drive(t, m, nil, press("down"), press("enter"), press("G"))
	if m.details || m.list.cursor != 0 {
		t.Fatalf("details %v, cursor %d", m.details, m.list.cursor)
	}
	if strings.Contains(plain(m), "details") {
		t.Fatalf("the footer offers a description of nothing:\n%s", plain(m))
	}
}

// pointerStep is one pointer event of a script, delivered at the time its
// clock says.
type pointerStep struct {
	msg tea.Msg
	at  func() time.Time
}

// TestTasksMouse drives the pointer: a click selects the task under it, a
// double click opens its description and another one closes it, and events
// that land on no task change nothing.
func TestTasksMouse(t *testing.T) {
	click := func(button tea.MouseButton, y int) tea.Msg { return tea.MouseClickMsg{Button: button, Y: y} }
	wheel := func(button tea.MouseButton) tea.Msg { return tea.MouseWheelMsg{Button: button} }
	later := func() time.Time { return fixedNow.Add(time.Second) }
	cases := []struct {
		name        string
		steps       []pointerStep
		wantCursor  string
		wantDetails bool
	}{
		{
			name:       "a click selects the task under the pointer",
			steps:      []pointerStep{{click(tea.MouseLeft, 3), clock}},
			wantCursor: "3",
		},
		{
			name:       "a double click opens its description",
			steps:      []pointerStep{{click(tea.MouseLeft, 2), clock}, {click(tea.MouseLeft, 2), clock}},
			wantCursor: "2", wantDetails: true,
		},
		{
			name: "a second double click closes it",
			steps: []pointerStep{
				{click(tea.MouseLeft, 2), clock},
				{click(tea.MouseLeft, 2), clock},
				{click(tea.MouseLeft, 2), later},
				{click(tea.MouseLeft, 2), later},
			},
			wantCursor: "2",
		},
		{
			name:       "two clicks far apart are two single clicks",
			steps:      []pointerStep{{click(tea.MouseLeft, 2), clock}, {click(tea.MouseLeft, 2), later}},
			wantCursor: "2",
		},
		{
			name:       "two clicks on two tasks are two single clicks",
			steps:      []pointerStep{{click(tea.MouseLeft, 2), clock}, {click(tea.MouseLeft, 3), clock}},
			wantCursor: "3",
		},
		{name: "a click on the header", steps: []pointerStep{{click(tea.MouseLeft, 0), clock}}, wantCursor: "1"},
		{name: "a click under the last task", steps: []pointerStep{{click(tea.MouseLeft, 9), clock}}, wantCursor: "1"},
		{name: "a click on the footer", steps: []pointerStep{{click(tea.MouseLeft, 11), clock}}, wantCursor: "1"},
		{name: "the right button", steps: []pointerStep{{click(tea.MouseRight, 3), clock}}, wantCursor: "1"},
		{name: "the middle button", steps: []pointerStep{{click(tea.MouseMiddle, 3), clock}}, wantCursor: "1"},
		{
			name:       "the wheel moves down",
			steps:      []pointerStep{{wheel(tea.MouseWheelDown), clock}, {wheel(tea.MouseWheelDown), clock}},
			wantCursor: "3",
		},
		{
			name:       "and back up",
			steps:      []pointerStep{{wheel(tea.MouseWheelDown), clock}, {wheel(tea.MouseWheelUp), clock}},
			wantCursor: "1",
		},
		{name: "a sideways wheel", steps: []pointerStep{{wheel(tea.MouseWheelLeft), clock}}, wantCursor: "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := clock
			m := newTasks(t, TasksOptions{Width: 100, Height: 12, Now: func() time.Time { return now() }}, sampleTasks())
			for _, s := range tc.steps {
				now = s.at
				drive(t, m, nil, s.msg)
			}
			got, ok := m.selected()
			if !ok || got.ID != tc.wantCursor {
				t.Fatalf("the cursor is on %+v, want #%s", got, tc.wantCursor)
			}
			if m.details != tc.wantDetails {
				t.Fatalf("details open %v, want %v", m.details, tc.wantDetails)
			}
		})
	}
}

// TestTasksClickOnTheDescription leaves the cursor where it is when the
// pointer lands on the description rather than on a task.
func TestTasksClickOnTheDescription(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 30}, sampleTasks())
	drive(t, m, nil, press("enter"))
	// The list takes the lines the description leaves: the first line of the
	// description is right under them.
	y := 1 + m.listHeight()
	drive(t, m, nil, tea.MouseClickMsg{Button: tea.MouseLeft, Y: y}, tea.MouseClickMsg{Button: tea.MouseLeft, Y: y})
	if got, _ := m.selected(); got.ID != "1" || !m.details {
		t.Fatalf("the cursor is on #%s with the description open %v", got.ID, m.details)
	}
}

// TestTasksKeepsSelection holds the cursor on the task it is on when the list
// changes under it, which is what a lead adding tasks does.
func TestTasksKeepsSelection(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 30}, sampleTasks())
	drive(t, m, nil, press("down"), press("down"))
	if got, _ := m.selected(); got.ID != "3" {
		t.Fatalf("selected #%s", got.ID)
	}
	more := append([]team.Task{{ID: "0", Subject: "read the brief"}}, sampleTasks()...)
	apply(m, tasksUpdate(more, nil))
	if got, _ := m.selected(); got.ID != "3" {
		t.Fatalf("the cursor moved to #%s", got.ID)
	}
	// A task that is gone takes the cursor to the line it was on.
	apply(m, tasksUpdate(sampleTasks()[3:], nil))
	if got, ok := m.selected(); !ok || got.ID != "10" {
		t.Fatalf("the cursor is on %+v, want the last task", got)
	}
}

// TestTasksKeepsTheListOnError keeps the tasks of the last reading when the
// next one fails, and says what went wrong until a reading succeeds again.
func TestTasksKeepsTheListOnError(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 12}, sampleTasks())
	apply(m, tasksUpdateMsg{update: TasksUpdate{Err: errors.New("read tasks: too many open files")}})
	out := plain(m)
	if !strings.Contains(out, "designing the schema") || !strings.Contains(out, "too many open files") {
		t.Fatalf("frame\n%s", out)
	}
	if !strings.Contains(strings.SplitN(out, "\n", 2)[0], "session-8f3c1d2a") {
		t.Fatalf("the failed reading took the team away:\n%s", out)
	}
	assertFrame(t, "tasks-error", m, 100, 12, false)
	apply(m, tasksUpdate(sampleTasks()[:1], nil))
	if out := plain(m); strings.Contains(out, "too many open files") || strings.Contains(out, "review the plan") {
		t.Fatalf("the next reading did not replace the list:\n%s", out)
	}
}

// TestTasksSanitizes draws a list whose every field carries control
// sequences: the team files are written by another program, and nothing in
// them reaches the terminal as anything but text.
func TestTasksSanitizes(t *testing.T) {
	const hostile = "\x1b]52;c;cm0gLXJmIH4=\a\x1b[2J\x1b]0;owned\a"
	m := NewTasks(TasksOptions{Styles: goldenStyles(t), Width: 100, Height: 20, Now: clock})
	apply(m, tasksUpdateMsg{update: TasksUpdate{Team: "crew" + hostile, Tasks: []team.Task{
		{ID: "1" + hostile, Subject: "a subject" + hostile, Owner: "an owner" + hostile, Description: "a line" + hostile + "\nanother"},
		{ID: "2", Subject: "blocked", BlockedBy: []string{"1" + hostile}},
	}}}, press("down"), press("enter"))
	content := m.View().Content
	for _, bad := range []string{"\x1b]", "\a", "\x1b[2J"} {
		if strings.Contains(content, bad) {
			t.Fatalf("the frame carries %q:\n%q", bad, content)
		}
	}
	for _, want := range []string{"crew", "a subject", "an owner", "a line", "another", "blocked by #1"} {
		if !strings.Contains(plain(m), want) {
			t.Fatalf("the frame lost %q:\n%s", want, plain(m))
		}
	}
}

// TestTasksDescribe lays the description out: its own line breaks kept, each
// line wrapped at the width it is drawn at, and nothing at all for a task
// with no description.
func TestTasksDescribe(t *testing.T) {
	cases := []struct {
		name  string
		width int
		text  string
		want  []string
	}{
		{name: "no description", width: 40},
		{name: "only blanks", width: 40, text: " \n\t "},
		{name: "one short line", width: 40, text: "write the tests", want: []string{"write the tests"}},
		{name: "its own lines", width: 40, text: "first\n\nthird", want: []string{"first", "", "third"}},
		{
			name: "a line wider than the list", width: 22, text: "keep every amount in cents",
			want: []string{"keep every amount in", "cents"},
		},
		{name: "a tab", width: 40, text: "a\tb", want: []string{"a    b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewTasks(TasksOptions{Styles: goldenStyles(t), Width: tc.width, Height: 20})
			if got := m.describe(team.Task{Description: tc.text}); !slices.Equal(got, tc.want) {
				t.Fatalf("describe = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTasksDetailsHeight sizes the description: what it has to show, up to
// half the list, and none at all where there is no room for one.
func TestTasksDetailsHeight(t *testing.T) {
	long := strings.Repeat("a line of the description\n", 30)
	cases := []struct {
		name   string
		height int
		task   team.Task
		open   bool
		want   int
	}{
		{name: "closed", height: 30, task: team.Task{ID: "1", Description: "short"}},
		{name: "a short description", height: 30, task: team.Task{ID: "1", Description: "short"}, open: true, want: 2},
		{name: "no description", height: 30, task: team.Task{ID: "1"}, open: true, want: 2},
		{name: "a long one takes half the list", height: 30, task: team.Task{ID: "1", Description: long}, open: true, want: 14},
		{name: "a short list gives it the fewest lines", height: 8, task: team.Task{ID: "1", Description: long}, open: true, want: 4},
		{name: "a list too short for one", height: 5, task: team.Task{ID: "1", Description: long}, open: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTasks(t, TasksOptions{Width: 60, Height: tc.height}, []team.Task{tc.task})
			if tc.open {
				drive(t, m, nil, press("enter"))
			}
			if got := m.detailsHeight(); got != tc.want {
				t.Fatalf("detailsHeight = %d, want %d", got, tc.want)
			}
			assertFrameShape(t, m, 60, tc.height)
		})
	}
}

// assertFrameShape checks a frame is exactly the size it is drawn at.
func assertFrameShape(t *testing.T, m viewer, width, height int) {
	t.Helper()
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != height {
		t.Fatalf("%d lines, want %d", len(lines), height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Fatalf("line %d is %d cells wide, over %d", i+1, w, width)
		}
	}
}

// TestTasksSizes drives the list through the sizes a popup takes: a resize
// keeps the cursor on a drawn line, and a size of nothing is still one cell.
func TestTasksSizes(t *testing.T) {
	m := newTasks(t, TasksOptions{Width: 100, Height: 30}, sampleTasks())
	drive(t, m, nil, press("G"), resize(60, 5))
	if m.width != 60 || m.height != 5 {
		t.Fatalf("size %dx%d", m.width, m.height)
	}
	if got, _ := m.selected(); got.ID != "10" || !strings.Contains(plain(m), "#10") {
		t.Fatalf("the resize lost the cursor on #%s:\n%s", got.ID, plain(m))
	}
	drive(t, m, nil, resize(0, 0))
	if m.width != 1 || m.height != 1 {
		t.Fatalf("size %dx%d, want 1x1", m.width, m.height)
	}
}

// TestTasksOffersItsKeys reads the footer: the keys that do something here
// and now, and closing only where a key closes the list.
func TestTasksOffersItsKeys(t *testing.T) {
	cases := []struct {
		name  string
		popup bool
		tasks []team.Task
		has   []string
		lacks []string
	}{
		{name: "a popup with tasks", popup: true, tasks: sampleTasks(), has: []string{"down", "details", "close"}},
		{name: "a pane with tasks", tasks: sampleTasks(), has: []string{"down", "details"}, lacks: []string{"close"}},
		{name: "a popup with one task", popup: true, tasks: sampleTasks()[:1], has: []string{"details", "close"}, lacks: []string{"down"}},
		{name: "a popup with none", popup: true, has: []string{"close"}, lacks: []string{"down", "details"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTasks(t, TasksOptions{Width: 100, Height: 12, Popup: tc.popup}, tc.tasks)
			lines := strings.Split(plain(m), "\n")
			footer := lines[len(lines)-1]
			for _, want := range tc.has {
				if !strings.Contains(footer, want) {
					t.Fatalf("the footer %q lacks %q", footer, want)
				}
			}
			for _, unwanted := range tc.lacks {
				if strings.Contains(footer, unwanted) {
					t.Fatalf("the footer %q offers %q", footer, unwanted)
				}
			}
		})
	}
}
