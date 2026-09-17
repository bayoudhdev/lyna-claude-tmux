package tui

import (
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

// AgentBarUpdate is one reading of the agents of a workspace, as the watcher
// of the rail delivers it.
type AgentBarUpdate struct {
	View team.View
	// Err is what went wrong reading them; the rail keeps the rows it has and
	// says so.
	Err error
	// At is when the reading was taken.
	At time.Time
}

// AgentBarActions are what the rail does to an agent. Each one is nil where
// there is nothing to act on, and the key then does nothing rather than
// reporting a failure the user cannot act on.
type AgentBarActions struct {
	// Focus brings the client to the agent's pane, Zoom zooms that pane, and
	// Window moves it to a window of its own.
	Focus  func(row team.Row) tea.Cmd
	Zoom   func(row team.Row) tea.Cmd
	Window func(row team.Row) tea.Cmd
}

// AgentBarOptions configure the agents rail.
type AgentBarOptions struct {
	Styles Styles
	// Updates delivers the rows; the rail waits on it for its whole life. A
	// closed channel keeps the last rows on screen.
	Updates <-chan AgentBarUpdate
	// Actions are the effects of the keys.
	Actions AgentBarActions
	// Popup makes q and esc close the rail; in a pane they do nothing, so a
	// stray key never removes the rail from a layout.
	Popup bool
	// CloseWhenEmpty ends a rail that opened by itself once the agents it
	// opened for are gone: the rail is a pane of the workspace, so leaving is
	// how it takes itself off the screen. A rail the user asked for stays
	// whatever it has to show.
	CloseWhenEmpty bool
	// Width and Height size the first frame.
	Width, Height int
	// Now is the clock ages and double clicks are measured with; time.Now when
	// nil.
	Now func() time.Time
}

type (
	agentBarUpdateMsg struct{ update AgentBarUpdate }
	agentBarClosedMsg struct{}
	// agentBarTickMsg is one animation frame. It is scheduled only while
	// something on the rail is moving, so a rail with nothing to show draws
	// once and then waits.
	agentBarTickMsg struct{ at time.Time }
)

// The animation budget of the rail.
const (
	// AgentBarFrame is how long one animation frame lasts.
	AgentBarFrame = 80 * time.Millisecond
	// AgentBarSteps is how many frames an arrival, a state change and a section
	// opening or closing take.
	AgentBarSteps = 3
	// AgentBarMove is how long each of those takes: long enough to be seen,
	// short enough that the rail is never behind the agents it draws.
	AgentBarMove = AgentBarSteps * AgentBarFrame
)

// spinnerFrames are the frames a working agent turns through, and
// asciiSpinner the same for a terminal without the braille block.
var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	asciiSpinner  = []string{"-", "\\", "|", "/"}
)

// AgentBarNoteMsg is one line for the footer of the rail: what an action the
// rail started has to say when it could not be carried out. The next key press
// clears it.
type AgentBarNoteMsg struct{ Text string }

// AgentBarNote builds the note a command reports a failure with.
func AgentBarNote(text string) tea.Msg { return AgentBarNoteMsg{Text: sanitize.Line(text)} }

// barItem is one drawn line of the rail: a section heading, or an agent.
type barItem struct {
	group  team.Group
	header bool
	row    team.Row
	// count is how many agents a heading stands for, which is not how many are
	// drawn under it while the section is opening or closing.
	count int
}

// agentBarKeys are the keys of the rail beyond the movement it shares with
// the other list views.
type agentBarKeys struct {
	Focus, Zoom, Window, Fold, Filter, Quit key.Binding
}

// AgentBarModel is the agents rail: every agent of the workspace, grouped,
// with what it is doing.
type AgentBarModel struct {
	opts   AgentBarOptions
	nav    navKeys
	keys   agentBarKeys
	width  int
	height int
	have   bool
	closed bool
	// arrived remembers that an agent of the team has been on the rail, which
	// is what a rail that closes by itself waits for before it leaves.
	arrived bool
	update  AgentBarUpdate
	items   []barItem
	list    listView
	clicks  clicks
	note    string
	// folded holds the sections the user closed, and filter the text the rows
	// are matched against while it is not empty.
	folded  map[team.Group]bool
	filter  lineInput
	editing bool
	// since remembers when a row was first seen in the state it is in, so the
	// age a row shows is the age of what it is doing.
	since map[string]stateSince
	// frame counts the animation frames drawn, ticking says a frame is already
	// scheduled, and folds holds the sections that are opening or closing.
	frame   int
	ticking bool
	folds   map[team.Group]foldAnim
}

// stateSince is a state and the first time the rail saw a row in it.
//
// born and changed are what the rail draws a movement from: born is when a row
// arrived, changed when it last took a new state, and both are zero for the
// rows of the first reading, which the rail did not watch arrive.
type stateSince struct {
	state   string
	at      time.Time
	born    time.Time
	changed time.Time
}

// foldAnim is a section opening or closing, and when it started.
type foldAnim struct {
	at      time.Time
	opening bool
}

// NewAgentBar builds the rail.
func NewAgentBar(opts AgentBarOptions) *AgentBarModel {
	w, h := sizeOr(opts.Width, opts.Height)
	ascii := opts.Styles.Theme.Icons.Name == "ascii"
	return &AgentBarModel{
		opts:   opts,
		nav:    newNavKeys(ascii),
		width:  w,
		height: h,
		folded: map[team.Group]bool{},
		since:  map[string]stateSince{},
		folds:  map[team.Group]foldAnim{},
		filter: lineInput{limit: 64},
		keys: agentBarKeys{
			Focus:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "focus")),
			Zoom:   key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "zoom")),
			Window: key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "window")),
			Fold:   key.NewBinding(key.WithKeys(" ", "space"), key.WithHelp("space", "fold")),
			Filter: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
			Quit:   key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		},
	}
}

func (m *AgentBarModel) now() time.Time { return nowOr(m.opts.Now)() }

// Init waits for the first reading.
func (m *AgentBarModel) Init() tea.Cmd { return m.wait() }

func (m *AgentBarModel) wait() tea.Cmd {
	ch := m.opts.Updates
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return agentBarClosedMsg{}
		}
		return agentBarUpdateMsg{update: u}
	}
}

// Update handles messages, and keeps the animation going while there is
// something to animate.
func (m *AgentBarModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.handle(msg)
	return m, tea.Batch(cmd, m.tick())
}

// tick schedules the next animation frame, or nothing at all when the rail is
// still: an idle rail costs no redraw, and a frame is never asked for twice.
func (m *AgentBarModel) tick() tea.Cmd {
	if m.ticking || !m.animating() {
		return nil
	}
	m.ticking = true
	return tea.Tick(AgentBarFrame, func(t time.Time) tea.Msg { return agentBarTickMsg{at: t} })
}

// animating reports whether anything on the rail is moving: an agent at work
// turns its spinner, a row that just arrived is still arriving, a row that just
// changed state is still changing, and a section is opening or closing.
func (m *AgentBarModel) animating() bool {
	now := m.now()
	for _, f := range m.folds {
		if now.Sub(f.at) < AgentBarMove {
			return true
		}
	}
	for _, it := range m.items {
		if it.header {
			continue
		}
		if it.row.State == team.StateBusy {
			return true
		}
		if m.moving(it.row) {
			return true
		}
	}
	return false
}

// moving reports a row that is still arriving or still changing state.
func (m *AgentBarModel) moving(r team.Row) bool {
	return m.step(m.since[rowKey(r)].born) >= 0 || m.step(m.since[rowKey(r)].changed) >= 0
}

// step is which frame of a movement started at t is drawn, and -1 once the
// movement is over. A zero time is a movement that never started, which is what
// the rows the rail was opened on carry: they were there before it drew.
func (m *AgentBarModel) step(t time.Time) int {
	if t.IsZero() {
		return -1
	}
	elapsed := m.now().Sub(t)
	if elapsed < 0 || elapsed >= AgentBarMove {
		return -1
	}
	return int(elapsed / AgentBarFrame)
}

// handle applies one message.
func (m *AgentBarModel) handle(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case agentBarTickMsg:
		m.ticking = false
		m.frame++
		// A section that is opening shows another row on every frame.
		m.rebuild()
		m.clampList()
		return nil
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.clampList()
	case agentBarUpdateMsg:
		m.setUpdate(msg.update)
		if m.leaving() {
			return tea.Quit
		}
		return m.wait()
	case agentBarClosedMsg:
		m.closed = true
	case AgentBarNoteMsg:
		m.note = msg.Text
	case tea.KeyPressMsg:
		m.note = ""
		return m.key(msg)
	case tea.MouseClickMsg:
		return m.click(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.move(-1, len(m.items), m.bodyHeight())
		case tea.MouseWheelDown:
			m.list.move(1, len(m.items), m.bodyHeight())
		}
	}
	return nil
}

// setUpdate takes a new reading: the rows are rebuilt, the ages of the rows
// that kept their state are kept, and the cursor stays on the agent it was on
// rather than on the line that agent used to be drawn at.
func (m *AgentBarModel) setUpdate(u AgentBarUpdate) {
	selected, hadSelection := m.selected()
	watched := m.have
	m.update, m.have = u, true
	m.arrived = m.arrived || u.View.Count(team.GroupTeammates)+u.View.Count(team.GroupSubagents) > 0
	m.stampStates(u, watched)
	m.rebuild()
	if hadSelection {
		for i, it := range m.items {
			if !it.header && it.row.Name == selected.Name && it.row.Group == selected.Group {
				m.list.cursor = i
				break
			}
		}
	}
	m.clampList()
}

// leaving reports a rail that opened by itself and has nothing left to show.
//
// The lead alone is nothing to show: a workspace has one whatever happens, and
// the rail opened for the agents that joined it. It leaves once it has seen
// them, so a rail that opens while the first of them is still starting waits
// for it rather than closing on the reading that came first.
func (m *AgentBarModel) leaving() bool {
	if !m.opts.CloseWhenEmpty || !m.arrived {
		return false
	}
	return m.update.View.Count(team.GroupTeammates)+m.update.View.Count(team.GroupSubagents) == 0
}

// stampStates keeps, for every row, the time it was first seen in the state it
// is in now, and what the rail has to draw a movement for: a row it watched
// arrive, and a row it watched take a new state. The rows of the first reading
// were there before the rail drew anything, so watched is false for them: a
// rail that opens on a running team opens on it, it does not play it back.
// A row that changed state is stamped again, and rows that are gone
// are forgotten, so the map holds the agents of the workspace and no more.
func (m *AgentBarModel) stampStates(u AgentBarUpdate, watched bool) {
	now := m.now()
	next := make(map[string]stateSince, len(u.View.Rows))
	for _, r := range u.View.Rows {
		key := rowKey(r)
		was, known := m.since[key]
		switch {
		case known && was.state == r.State:
			next[key] = was
		case known:
			// The same agent in a new state: the age starts again and the row is
			// drawn changing.
			next[key] = stateSince{state: r.State, at: now, born: was.born, changed: now}
		default:
			born := time.Time{}
			if watched {
				born = now
			}
			next[key] = stateSince{state: r.State, at: now, born: born}
		}
	}
	m.since = next
}

// rowKey identifies a row between two readings.
func rowKey(r team.Row) string {
	return strconv.Itoa(int(r.Group)) + "\x00" + r.Name + "\x00" + r.Pane
}

// rebuild lays the view out: a heading per section that has rows, then the
// rows of that section unless it is folded or filtered away.
func (m *AgentBarModel) rebuild() {
	terms := strings.Fields(strings.ToLower(m.filter.value))
	m.items = m.items[:0]
	for _, g := range team.Groups() {
		var rows []team.Row
		for _, r := range m.update.View.Rows {
			if r.Group == g && matchRow(r, terms) {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			continue
		}
		m.items = append(m.items, barItem{group: g, header: true, count: len(rows)})
		for _, r := range rows[:m.shown(g, len(rows))] {
			m.items = append(m.items, barItem{group: g, row: r})
		}
	}
}

// shown is how many rows of a section are drawn: all of them, none of an open
// section that was closed, and a share of them while the section is moving, so
// a fold takes the rows away a few at a time instead of in one jump.
func (m *AgentBarModel) shown(g team.Group, rows int) int {
	f, moving := m.folds[g]
	step := m.step(f.at)
	if !moving || step < 0 {
		if m.folded[g] {
			return 0
		}
		return rows
	}
	if f.opening {
		return rows * (step + 1) / AgentBarSteps
	}
	return rows * (AgentBarSteps - 1 - step) / AgentBarSteps
}

// matchRow reports whether a row carries every term of the filter, in its name,
// its type or the task it holds. The workspace an agent runs in is matched for
// the agents of the other workspaces, where it is what tells them apart; every
// row of this workspace carries the same one, which would match all of them.
func matchRow(r team.Row, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(r.Name + " " + r.Type + " " + r.Task)
	if r.Group == team.GroupElsewhere {
		hay += " " + strings.ToLower(r.Session)
	}
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// selected is the agent the cursor is on; a heading is not one.
func (m *AgentBarModel) selected() (team.Row, bool) {
	if m.list.cursor < 0 || m.list.cursor >= len(m.items) {
		return team.Row{}, false
	}
	it := m.items[m.list.cursor]
	if it.header {
		return team.Row{}, false
	}
	return it.row, true
}

// Selected is the agent the cursor is on, for a caller that acts on it.
func (m *AgentBarModel) Selected() (team.Row, bool) { return m.selected() }

// key handles one key press. Movement and folding work in a pane as well as in
// a popup; only closing is held back to the popup, so a stray q never takes
// the rail out of a layout.
func (m *AgentBarModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.editing {
		return m.filterKey(msg)
	}
	if d, ok := m.nav.delta(msg, len(m.items), m.bodyHeight()); ok {
		m.list.move(d, len(m.items), m.bodyHeight())
		return nil
	}
	switch {
	case m.opts.Popup && key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Fold):
		m.fold()
		return nil
	case key.Matches(msg, m.keys.Filter):
		m.editing = true
		return nil
	case key.Matches(msg, m.keys.Focus):
		return m.act(m.opts.Actions.Focus)
	case key.Matches(msg, m.keys.Zoom):
		return m.act(m.opts.Actions.Zoom)
	case key.Matches(msg, m.keys.Window):
		return m.act(m.opts.Actions.Window)
	}
	return nil
}

// filterKey edits the filter: every key belongs to the field while it is open,
// so an agent called "z" is typed rather than zooming a pane.
func (m *AgentBarModel) filterKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "enter", "esc":
		m.editing = false
		if msg.String() == "esc" {
			m.filter.value = ""
			m.rebuild()
			m.clampList()
		}
		return nil
	}
	if m.filter.update(msg) {
		m.rebuild()
		m.clampList()
	}
	return nil
}

// act runs an action on the selected agent.
func (m *AgentBarModel) act(run func(team.Row) tea.Cmd) tea.Cmd {
	row, ok := m.selected()
	if !ok || run == nil {
		return nil
	}
	return run(row)
}

// fold closes or opens the section the cursor is in, heading or agent alike.
func (m *AgentBarModel) fold() {
	if m.list.cursor < 0 || m.list.cursor >= len(m.items) {
		return
	}
	g := m.items[m.list.cursor].group
	m.folded[g] = !m.folded[g]
	m.folds[g] = foldAnim{at: m.now(), opening: !m.folded[g]}
	m.rebuild()
	// The heading of the section the user folded is where the cursor belongs:
	// the rows under it are gone.
	for i, it := range m.items {
		if it.header && it.group == g {
			m.list.cursor = i
			break
		}
	}
	m.clampList()
}

// click moves the cursor to the line under the pointer; a double click on an
// agent focuses its pane, and on a heading folds the section.
func (m *AgentBarModel) click(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	// The first body line is drawn under the header.
	row := msg.Y - 1
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	idx := m.list.offset + row
	if idx >= len(m.items) {
		return nil
	}
	double := m.clicks.click(m.now(), idx)
	m.list.move(idx-m.list.cursor, len(m.items), m.bodyHeight())
	if !double {
		return nil
	}
	if m.items[idx].header {
		m.fold()
		return nil
	}
	return m.act(m.opts.Actions.Focus)
}

// bodyHeight is the number of lines the rows are drawn in: the header and the
// footer take one each, and the note takes one while there is something to say.
func (m *AgentBarModel) bodyHeight() int { return max(m.height-2-m.notes(), 1) }

// notes is 1 while the rail has a line to show under the rows.
func (m *AgentBarModel) notes() int {
	if m.height < 6 {
		return 0
	}
	if m.note != "" || m.editing {
		return 1
	}
	if row, ok := m.selected(); ok && (row.Task != "" || row.Type != "") {
		return 1
	}
	return 0
}

func (m *AgentBarModel) clampList() { m.list.clamp(len(m.items), m.bodyHeight()) }

// View draws the rail.
func (m *AgentBarModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *AgentBarModel) render() string {
	lines := []string{m.header()}
	lines = append(lines, m.body()...)
	tail := m.height - 1 - m.notes()
	for len(lines) < tail {
		lines = append(lines, "")
	}
	lines = lines[:max(min(len(lines), tail), 0)]
	if m.notes() == 1 {
		lines = append(lines, m.noteLine())
	}
	return screen(append(lines, m.footer()), m.width, m.height)
}

func (m *AgentBarModel) header() string {
	s := m.opts.Styles
	ic := s.Theme.Icons
	left := []segment{seg(" "+ic.Agents+" agents ", s.Title)}
	var right []segment
	if n := m.agents(); n > 0 {
		right = append(right, seg(strconv.Itoa(n)+" ", s.Text))
	}
	return s.bar(m.width, left, right)
}

// agents counts the drawn agents, which is what the header shows: with a
// filter open it is what the filter kept.
func (m *AgentBarModel) agents() int {
	n := 0
	for _, it := range m.items {
		if !it.header {
			n++
		}
	}
	return n
}

func (m *AgentBarModel) body() []string {
	s := m.opts.Styles
	h := m.bodyHeight()
	switch {
	case !m.have:
		return []string{"", s.Muted.Render(center("reading the team", m.width, s.Ellipsis))}
	case m.update.Err != nil && len(m.items) == 0:
		return []string{"", " " + s.Danger.Render(truncate(sanitize.Line(m.update.Err.Error()), m.width-2, s.Ellipsis))}
	case len(m.items) == 0 && m.filter.value != "":
		return []string{"", s.Idle.Render(center("no agent matches", m.width, s.Ellipsis))}
	case len(m.items) == 0:
		return []string{"", s.Idle.Render(center("no agent yet", m.width, s.Ellipsis))}
	}
	end := min(m.list.offset+h, len(m.items))
	lines := make([]string, 0, h)
	for i, it := range m.items[m.list.offset:end] {
		selected := m.list.offset+i == m.list.cursor
		if it.header {
			lines = append(lines, m.headingLine(it, selected))
			continue
		}
		lines = append(lines, m.rowLine(it.row, selected))
	}
	return lines
}

// headingLine draws a section: the fold marker, the name and how many agents
// it holds.
func (m *AgentBarModel) headingLine(it barItem, selected bool) string {
	s := m.opts.Styles
	g := it.group
	open, shut := "▾", "▸"
	if s.Theme.Icons.Name == "ascii" {
		open, shut = "v", ">"
	}
	marker := open
	if m.folded[g] {
		marker = shut
	}
	// What the section holds, which is not what it is drawing while it opens or
	// closes, and the whole section when a filter is hiding part of it.
	n := it.count
	if m.folded[g] {
		n = m.update.View.Count(g)
	}
	var bg *lipgloss.Style
	if selected {
		bg = &s.Selected
	}
	return s.line(m.width, bg,
		seg(marker+" ", s.Border),
		seg(g.String(), s.Accent2),
		seg(" "+strconv.Itoa(n), s.Muted),
	)
}

// rowLine draws one agent: the cursor marker, the state, the name, and how
// long it has been doing what it is doing.
func (m *AgentBarModel) rowLine(r team.Row, selected bool) string {
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
	style, glyph := m.state(r)
	age := m.age(r)
	name := sanitize.Line(r.Name)
	// The pane the user is on is the one they are looking at: it is named in
	// the color the workspace marks its own with.
	nameStyle := s.Text
	if r.Active {
		nameStyle = s.Accent2
	}
	// A row the rail watched arrive is drawn coming up through the colors it
	// settles in, and one that has just taken a new state has that state
	// picked out for as long as the change is worth noticing.
	if step := m.step(m.since[rowKey(r)].born); step >= 0 {
		nameStyle = arrivingStyle(s, step, nameStyle)
	}
	if m.step(m.since[rowKey(r)].changed) >= 0 {
		style = s.Accent
	}
	// The marker, the state glyph with a space on each side, a space before
	// the age and one after it: what is left is the name.
	width := max(m.width-6-ansi.StringWidth(age), 4)
	segs := []segment{
		seg(marker, s.Accent),
		seg(" "+glyph+" ", style),
		seg(fit(name, width, s.Ellipsis), nameStyle),
		seg(" "+age+" ", s.Muted),
	}
	return s.line(m.width, bg, segs...)
}

// arrivingStyle is the color a row that just arrived is drawn in on one frame
// of its arrival: it comes up out of the border color, through the muted one,
// into the color it keeps.
func arrivingStyle(s Styles, step int, settled lipgloss.Style) lipgloss.Style {
	switch step {
	case 0:
		return s.Border
	case 1:
		return s.Muted
	}
	return settled
}

// spinner is the frame a working agent turns on now.
func (m *AgentBarModel) spinner() string {
	frames := spinnerFrames
	if m.opts.Styles.Theme.Icons.Name == "ascii" {
		frames = asciiSpinner
	}
	return frames[m.frame%len(frames)]
}

// state is the style and the glyph of a row's state. An agent at work turns a
// spinner, which is the one thing on the rail that says a reading is live
// rather than the last one that arrived.
func (m *AgentBarModel) state(r team.Row) (lipgloss.Style, string) {
	s := m.opts.Styles
	ic := s.Theme.Icons
	switch r.State {
	case team.StateBusy:
		return s.Busy, m.spinner()
	case team.StateWaiting:
		return s.Waiting, ic.Waiting
	case team.StateIdle:
		return s.Idle, ic.Idle
	case team.StateFailed:
		return s.Danger, ic.Failed
	}
	return s.Unknown, ic.Unknown
}

// age is how long the row has been in the state it is in, as the rail saw it.
func (m *AgentBarModel) age(r team.Row) string {
	st, ok := m.since[rowKey(r)]
	if !ok {
		return ""
	}
	return formatAge(m.now().Sub(st.at))
}

// noteLine is the line under the rows: what the rail was told to say, the
// filter being typed, or the task the selected agent holds.
func (m *AgentBarModel) noteLine() string {
	s := m.opts.Styles
	switch {
	case m.note != "":
		return s.line(m.width, nil, seg(" ", s.Muted), seg(m.note, s.Danger))
	case m.editing:
		return s.line(m.width, nil, seg(" /", s.Key), seg(m.filter.value, s.Text), seg("█", s.Accent))
	}
	row, ok := m.selected()
	if !ok {
		return ""
	}
	// What the selected agent is, and what it is working on: the two things a
	// row has no room for.
	segs := []segment{seg(" ", s.Muted)}
	if row.Type != "" {
		segs = append(segs, seg(sanitize.Line(row.Type), s.Accent))
		if row.Task != "" {
			segs = append(segs, seg(" "+s.Theme.Icons.Sep+" ", s.Border))
		}
	}
	return s.line(m.width, nil, append(segs, seg(sanitize.Line(row.Task), s.Muted))...)
}

func (m *AgentBarModel) footer() string {
	s := m.opts.Styles
	var segs []segment
	t := m.update.View.Tasks
	switch {
	// A reading that failed is what the footer is for: the rows are the ones
	// the last reading left, and how old they are is the thing to say about
	// them.
	case m.have && m.update.Err != nil:
		segs = append(segs, seg(" "+truncate(sanitize.Line(m.update.Err.Error()), max(m.width-2, 0), s.Ellipsis), s.Danger))
	case t.Total > 0:
		segs = append(segs, seg(" "+strconv.Itoa(t.Done)+"/"+strconv.Itoa(t.Total)+" tasks", s.Text))
		if t.Blocked > 0 {
			segs = append(segs, seg("  "+strconv.Itoa(t.Blocked)+" blocked", s.Warning))
		}
	}
	if m.closed {
		segs = append(segs, seg("  watcher stopped", s.Warning))
	}
	keys := m.helpKeys()
	if len(keys) == 0 {
		return s.line(m.width, &s.Bar, segs...)
	}
	help := s.helpLine(max(m.width/2, 12), keys...)
	hw := ansi.StringWidth(help)
	if hw >= m.width {
		return help
	}
	return s.line(m.width-hw, &s.Bar, segs...) + help
}

// helpKeys are the keys the footer offers: the ones that do something here and
// now, so a rail with no agent in it offers nothing to do to one.
func (m *AgentBarModel) helpKeys() []key.Binding {
	var keys []key.Binding
	if len(m.items) > 1 {
		keys = append(keys, m.nav.Down)
	}
	if _, ok := m.selected(); ok {
		if m.opts.Actions.Focus != nil {
			keys = append(keys, m.keys.Focus)
		}
		if m.opts.Actions.Window != nil {
			keys = append(keys, m.keys.Window)
		}
	}
	if m.opts.Popup {
		keys = append(keys, m.keys.Quit)
	}
	return keys
}
