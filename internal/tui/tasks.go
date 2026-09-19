package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// TasksUpdate is one reading of the shared task list of the team a workspace
// runs, as the watcher of the list delivers it.
type TasksUpdate struct {
	// Team is the team the workspace runs, empty while it runs none.
	Team string
	// Tasks is the list the team shares, in any order: the view orders it.
	Tasks []team.Task
	// Err is what went wrong reading it. The view keeps the tasks the last
	// reading left and says so, rather than emptying a list that was fine a
	// moment ago.
	Err error
}

// TasksOptions configure the task list.
type TasksOptions struct {
	Styles Styles
	// Updates delivers the readings; the view waits on it for its whole life.
	// A closed channel keeps the last list on screen.
	Updates <-chan TasksUpdate
	// Popup makes q and esc close the view; in a pane they do nothing, so a
	// stray key never removes the list from a layout.
	Popup bool
	// Width and Height size the first frame.
	Width, Height int
	// Now is the clock double clicks are measured with; time.Now when nil.
	Now func() time.Time
}

type (
	tasksUpdateMsg struct{ update TasksUpdate }
	tasksClosedMsg struct{}
)

// tasksKeys are the keys of the list beyond the movement it shares with the
// other list views.
type tasksKeys struct {
	Details, Quit key.Binding
}

// Task states as the list draws them. Blocked is not a state Claude Code
// writes: it is a task that waits on another one that is not finished yet.
const (
	taskPending = iota
	taskRunning
	taskDone
	taskBlocked
)

// taskGlyphs are the glyphs of the four states, in the order above, and
// asciiTaskGlyphs the same for a terminal without the geometric shapes.
var (
	taskGlyphs      = [...]string{"○", "◐", "✓", "⊘"}
	asciiTaskGlyphs = [...]string{"-", "*", "x", "!"}
)

// Column bounds of a task row.
const (
	// tasksIDWidth bounds the identifier column. Claude Code numbers its
	// tasks, so an identifier is a few digits; a longer one is cut.
	tasksIDWidth = 8
	// tasksOwnerWidth bounds the owner column, which holds an agent name.
	tasksOwnerWidth = 18
	// tasksMinLabel is the part of a subject kept on screen before a blocked
	// task gives up its note: what the task is matters more than what it
	// waits on, which the glyph says already.
	tasksMinLabel = 12
	// tasksMinNote is the shortest note drawn, which is the note up to its
	// first identifier: anything shorter says nothing the glyph does not.
	tasksMinNote = len("blocked by #1")
	// tasksMinDetails is the fewest lines the description takes when it is
	// open: a rule and a few lines of text.
	tasksMinDetails = 4
)

// TasksModel is the shared task list of a team: every task, where it stands,
// who holds it and what it waits on.
type TasksModel struct {
	opts   TasksOptions
	nav    navKeys
	keys   tasksKeys
	width  int
	height int
	have   bool
	closed bool
	team   string
	// tasks is the list of the last reading that succeeded, in the order the
	// team works through it.
	tasks []team.Task
	err   error
	list  listView
	click clicks
	// details shows the description of the selected task under the list.
	details bool
}

// NewTasks builds the task list.
func NewTasks(opts TasksOptions) *TasksModel {
	w, h := sizeOr(opts.Width, opts.Height)
	return &TasksModel{
		opts:   opts,
		nav:    newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		width:  w,
		height: h,
		keys: tasksKeys{
			Details: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details")),
			Quit:    key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		},
	}
}

func (m *TasksModel) now() time.Time { return nowOr(m.opts.Now)() }

// Init waits for the first reading.
func (m *TasksModel) Init() tea.Cmd { return m.wait() }

func (m *TasksModel) wait() tea.Cmd {
	ch := m.opts.Updates
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return tasksClosedMsg{}
		}
		return tasksUpdateMsg{update: u}
	}
}

// Update handles messages. Nothing here schedules a frame: the list redraws
// on a reading, a key or the pointer, and on nothing else.
func (m *TasksModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.clampList()
	case tasksUpdateMsg:
		m.setUpdate(msg.update)
		return m, m.wait()
	case tasksClosedMsg:
		m.closed = true
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		m.clicked(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.move(-1, len(m.tasks), m.listHeight())
		case tea.MouseWheelDown:
			m.list.move(1, len(m.tasks), m.listHeight())
		}
	}
	return m, nil
}

// setUpdate takes a new reading. A reading that failed keeps the tasks of the
// last one that did not; one that succeeded replaces them, and the cursor
// stays on the task it was on rather than on the line that task was drawn at.
func (m *TasksModel) setUpdate(u TasksUpdate) {
	m.have, m.err = true, u.Err
	if u.Err != nil {
		return
	}
	selected, had := m.selected()
	m.team = u.Team
	m.tasks = slices.Clone(u.Tasks)
	team.SortTasks(m.tasks)
	if had {
		if i := slices.IndexFunc(m.tasks, func(t team.Task) bool { return t.ID == selected.ID }); i >= 0 {
			m.list.cursor = i
		}
	}
	m.clampList()
}

// selected is the task the cursor is on.
func (m *TasksModel) selected() (team.Task, bool) {
	if m.list.cursor < 0 || m.list.cursor >= len(m.tasks) {
		return team.Task{}, false
	}
	return m.tasks[m.list.cursor], true
}

// key handles one key press. Movement and the description work in a pane as
// well as in a popup; only closing is held back to the popup.
func (m *TasksModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" || (m.opts.Popup && key.Matches(msg, m.keys.Quit)) {
		return tea.Quit
	}
	if d, ok := m.nav.delta(msg, len(m.tasks), m.listHeight()); ok {
		m.list.move(d, len(m.tasks), m.listHeight())
		return nil
	}
	if key.Matches(msg, m.keys.Details) {
		m.toggleDetails()
	}
	return nil
}

// toggleDetails opens or closes the description of the selected task. With no
// task there is nothing to describe, and the key does nothing.
func (m *TasksModel) toggleDetails() {
	if _, ok := m.selected(); !ok {
		return
	}
	m.details = !m.details
	m.clampList()
}

// clicked moves the cursor to the task under the pointer; a double click on
// the same task opens or closes its description.
func (m *TasksModel) clicked(msg tea.MouseClickMsg) {
	if msg.Button != tea.MouseLeft {
		return
	}
	// The first task is drawn under the header, and the description under
	// the list is not a row.
	row := msg.Y - 1
	if row < 0 || row >= m.listHeight() {
		return
	}
	idx := m.list.offset + row
	if idx >= len(m.tasks) {
		return
	}
	double := m.click.click(m.now(), idx)
	m.list.move(idx-m.list.cursor, len(m.tasks), m.listHeight())
	if double {
		m.toggleDetails()
	}
}

// bodyHeight is the number of lines between the header and the footer.
func (m *TasksModel) bodyHeight() int { return max(m.height-2, 1) }

// detailsHeight is the number of lines the description takes under the list:
// what it has to show, up to half the body, and never the line the selected
// task is drawn on.
func (m *TasksModel) detailsHeight() int {
	t, ok := m.selected()
	body := m.bodyHeight()
	if !m.details || !ok || body < tasksMinDetails {
		return 0
	}
	want := 1 + max(len(m.describe(t)), 1)
	return min(want, max(body/2, tasksMinDetails), body-1)
}

// listHeight is the number of lines the tasks are drawn in.
func (m *TasksModel) listHeight() int { return max(m.bodyHeight()-m.detailsHeight(), 1) }

func (m *TasksModel) clampList() { m.list.clamp(len(m.tasks), m.listHeight()) }

// View draws the list.
func (m *TasksModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *TasksModel) render() string {
	lines := []string{m.header()}
	lines = append(lines, m.body()...)
	tail := m.height - 1 - m.detailsHeight()
	for len(lines) < tail {
		lines = append(lines, "")
	}
	lines = append(lines[:max(min(len(lines), tail), 0)], m.detailsBlock()...)
	return screen(append(lines, m.footer()), m.width, m.height)
}

func (m *TasksModel) header() string {
	s := m.opts.Styles
	left := []segment{seg(" "+s.Theme.Icons.Menu+" tasks ", s.Title)}
	if m.team != "" {
		left = append(left, seg(" "+sanitize.Line(m.team)+" ", s.Muted))
	}
	var right []segment
	if c := team.Count(m.tasks); c.Total > 0 {
		right = append(right, seg(strconv.Itoa(c.Done)+" of "+strconv.Itoa(c.Total)+" done", s.Text))
		if c.Blocked > 0 {
			right = append(right, seg(", "+strconv.Itoa(c.Blocked)+" blocked", s.Warning))
		}
		right = append(right, seg(" ", s.Text))
	}
	return s.bar(m.width, left, right)
}

func (m *TasksModel) body() []string {
	s := m.opts.Styles
	switch {
	case !m.have:
		return []string{"", s.Muted.Render(center("reading the task list", m.width, s.Ellipsis))}
	case m.err != nil && len(m.tasks) == 0:
		return []string{"", " " + s.Danger.Render(truncate(sanitize.Line(m.err.Error()), m.width-2, s.Ellipsis))}
	case len(m.tasks) == 0:
		return []string{"", s.Idle.Render(center("no shared task list yet", m.width, s.Ellipsis))}
	}
	cols := m.columns()
	end := min(m.list.offset+m.listHeight(), len(m.tasks))
	lines := make([]string, 0, end-m.list.offset)
	for i, t := range m.tasks[m.list.offset:end] {
		lines = append(lines, m.rowLine(t, m.list.offset+i == m.list.cursor, cols))
	}
	return lines
}

// taskColumns are the widths of the columns every row shares, so identifiers
// and owners line up down the list.
type taskColumns struct {
	id, owner, middle int
}

// columns measures the rows: the identifier column fits the longest
// identifier, the owner column the longest owner, and the subject takes what
// is left. A list too narrow for an owner column drops it before the subject
// gives up its last cells.
func (m *TasksModel) columns() taskColumns {
	var c taskColumns
	for _, t := range m.tasks {
		c.id = max(c.id, ansi.StringWidth("#"+sanitize.Line(t.ID)))
		c.owner = max(c.owner, ansi.StringWidth(sanitize.Line(t.Owner)))
	}
	c.id = min(c.id, tasksIDWidth)
	c.owner = min(c.owner, tasksOwnerWidth, m.width/4)
	// The marker, the glyph with a space on each side, the identifier and a
	// space after it; then the owner between two spaces.
	fixed := 4 + c.id + 1
	if c.owner > 0 {
		fixed += c.owner + 2
	}
	c.middle = m.width - fixed
	if c.middle < tasksMinLabel && c.owner > 0 {
		c.middle += c.owner + 2
		c.owner = 0
	}
	c.middle = max(c.middle, 1)
	return c
}

// state is where a task stands as the list draws it. A task waiting on an
// unfinished one is blocked whatever Claude Code wrote, which is what the
// count in the header says too.
func (m *TasksModel) state(t team.Task) int {
	switch {
	case t.Done():
		return taskDone
	case len(team.WaitingOn(t, m.tasks)) > 0:
		return taskBlocked
	case t.Status == team.StatusRunning:
		return taskRunning
	}
	return taskPending
}

// glyph is the style and the glyph of a state.
func (m *TasksModel) glyph(state int) (lipgloss.Style, string) {
	s := m.opts.Styles
	glyphs := taskGlyphs
	if s.Theme.Icons.Name == "ascii" {
		glyphs = asciiTaskGlyphs
	}
	switch state {
	case taskRunning:
		return s.Busy, glyphs[taskRunning]
	case taskDone:
		return s.Success, glyphs[taskDone]
	case taskBlocked:
		return s.Warning, glyphs[taskBlocked]
	}
	return s.Muted, glyphs[taskPending]
}

// blockedBy is the note of a blocked task: the unfinished tasks it waits on.
func (m *TasksModel) blockedBy(t team.Task) string {
	waiting := team.WaitingOn(t, m.tasks)
	if len(waiting) == 0 {
		return ""
	}
	ids := make([]string, len(waiting))
	for i, id := range waiting {
		ids[i] = "#" + sanitize.Line(id)
	}
	return "blocked by " + strings.Join(ids, ", ")
}

// rowLine draws one task: the cursor marker, the state, the identifier, what
// the task is and what it waits on, and who holds it.
func (m *TasksModel) rowLine(t team.Task, selected bool, cols taskColumns) string {
	s := m.opts.Styles
	var bg *lipgloss.Style
	marker := " "
	if selected {
		marker = "▌"
		if s.Theme.Icons.Name == "ascii" {
			marker = ">"
		}
		bg = &s.Selected
	}
	state := m.state(t)
	style, glyph := m.glyph(state)
	labelStyle := s.Text
	if state == taskDone {
		labelStyle = s.Muted
	}
	segs := []segment{
		seg(marker, s.Accent),
		seg(" "+glyph+" ", style),
		seg(fit("#"+sanitize.Line(t.ID), cols.id, s.Ellipsis)+" ", s.Muted),
	}
	// The note is whole while the subject keeps its first cells beside it.
	// Past that the subject takes the row, and the note the room it leaves.
	note := m.blockedBy(t)
	labelWidth := cols.middle
	if rest := cols.middle - 2 - ansi.StringWidth(note); note != "" && rest >= tasksMinLabel {
		labelWidth = rest
	}
	label := truncate(sanitize.Line(t.Label()), labelWidth, s.Ellipsis)
	used := ansi.StringWidth(label)
	segs = append(segs, seg(label, labelStyle))
	if room := cols.middle - used - 2; note != "" && room >= tasksMinNote {
		note = truncate(note, room, s.Ellipsis)
		segs = append(segs, seg("  "+note, s.Warning))
		used += 2 + ansi.StringWidth(note)
	}
	if cols.owner > 0 {
		segs = append(segs,
			seg(strings.Repeat(" ", max(cols.middle-used, 0)+1), s.Text),
			seg(fit(sanitize.Line(t.Owner), cols.owner, s.Ellipsis)+" ", s.Accent2))
	}
	return s.line(m.width, bg, segs...)
}

// describe is the description of a task as lines of the width it is drawn
// at. The text is the lead's, written for another agent: it is sanitized, its
// own line breaks are kept, and each of its lines is wrapped.
func (m *TasksModel) describe(t team.Task) []string {
	text := strings.TrimSpace(sanitize.Plain(t.Description))
	if text == "" {
		return nil
	}
	width := max(m.width-2, 1)
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\t", "    "), "\n") {
		out = append(out, strings.Split(ansi.Wrap(strings.TrimRight(line, " "), width, ""), "\n")...)
	}
	return out
}

// detailsBlock draws the description of the selected task: a rule naming the
// task, then its text, cut with the ellipsis when it has more lines than the
// block.
func (m *TasksModel) detailsBlock() []string {
	h := m.detailsHeight()
	t, ok := m.selected()
	if h == 0 || !ok {
		return nil
	}
	s := m.opts.Styles
	lines := []string{s.rule(m.width, "#"+sanitize.Line(t.ID)+" "+sanitize.Line(t.Subject))}
	text := m.describe(t)
	if len(text) == 0 {
		return append(lines, " "+s.Muted.Render("no description"))
	}
	if len(text) > h-1 {
		text = text[:h-1]
		last := &text[len(text)-1]
		*last = truncate(*last+" "+s.Ellipsis, max(m.width-2, 1), s.Ellipsis)
	}
	for _, l := range text {
		lines = append(lines, " "+s.Text.Render(l))
	}
	return lines
}

func (m *TasksModel) footer() string {
	s := m.opts.Styles
	var segs []segment
	// A reading that failed is what the footer is for: the tasks on screen are
	// the ones the last reading left.
	if m.err != nil && len(m.tasks) > 0 {
		segs = append(segs, seg(" "+truncate(sanitize.Line(m.err.Error()), max(m.width-2, 0), s.Ellipsis), s.Danger))
	}
	if m.closed {
		segs = append(segs, seg("  watcher stopped", s.Warning))
	}
	keys := m.helpKeys()
	if len(keys) == 0 {
		return s.line(m.width, &s.Bar, segs...)
	}
	// The keys take what the note leaves, and never less than half the line.
	help := s.helpLine(max(m.width-segsWidth(segs)-1, m.width/2, 12), keys...)
	hw := ansi.StringWidth(help)
	if hw >= m.width {
		return help
	}
	return s.line(m.width-hw, &s.Bar, segs...) + help
}

// helpKeys are the keys the footer offers: the ones that do something here
// and now.
func (m *TasksModel) helpKeys() []key.Binding {
	var keys []key.Binding
	if len(m.tasks) > 1 {
		keys = append(keys, m.nav.Down)
	}
	if len(m.tasks) > 0 {
		keys = append(keys, m.keys.Details)
	}
	if m.opts.Popup {
		keys = append(keys, m.keys.Quit)
	}
	return keys
}
