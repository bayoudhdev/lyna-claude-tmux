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

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// maxGraphLanes is how many lanes of a history are drawn before the drawing
// is cut: a project with more lines of descent than this in one page is read
// by its subjects, not by its shape.
const maxGraphLanes = 8

// The columns of the history and the widths where each one appears. A pane
// narrower than one of them drops that column rather than squeezing the
// subject, which is what the history is read by.
const (
	graphOIDWidth    = 7
	graphAuthorWidth = 12
	graphAgeWidth    = 4
	// graphRefsMin and graphRefsMax bound the column the branch and tag
	// chips are drawn in, which is a fifth of the pane between them.
	graphRefsMin = 14
	graphRefsMax = 26
	// The widths where the chips, the author and the object name fit beside
	// a subject still worth reading.
	graphRefsEnough   = 76
	graphAuthorEnough = 56
	graphOIDEnough    = 36
)

// GitCommitSelectedMsg is the commit the cursor moved to, which the detail
// pane follows.
type GitCommitSelectedMsg struct{ Commit vcs.Commit }

// GitCommitChosenMsg is the commit the user opened.
type GitCommitChosenMsg struct{ Commit vcs.Commit }

// GitGraphMoreMsg asks for the page after the one on screen. The host reads
// it and calls SetCommits with the longer history.
type GitGraphMoreMsg struct{}

// GitGraphOptions configure the history graph.
type GitGraphOptions struct {
	Styles Styles
	// Commits is the page of history drawn, newest first, as git reads it.
	Commits []vcs.Commit
	// More says the history goes on past the page: the graph asks for the
	// next one when the cursor reaches the end.
	More bool
	// Title names what the history is filtered to ("main", "origin/main"),
	// empty for the whole project.
	Title string
	// Width and Height size the first frame.
	Width, Height int
	// Popup makes q and esc close the view.
	Popup bool
	// Now is the clock ages and double clicks are measured with.
	Now func() time.Time
}

// GitGraphModel draws a history with its lanes, its refs and its subjects.
type GitGraphModel struct {
	opts    GitGraphOptions
	nav     navKeys
	quitK   key.Binding
	openK   key.Binding
	searchK key.Binding
	nextK   key.Binding
	prevK   key.Binding
	width   int
	height  int
	graph   vcs.Graph
	// lines are what is drawn, a gap line before every commit the lines of
	// the history have to be drawn into; cursor is the commit the user is on
	// and top the first line on screen.
	lines   []graphLine
	cursor  int
	top     int
	search  lineInput
	seeking bool
	asked   bool
	clicks  clicks
	// focused says the keys reach this region, which a workstation holding
	// several of them turns off for the ones it is not typing at. A region
	// standing on its own is always the one being typed at.
	focused bool
}

// graphLine is one drawn line: a commit, or the gap above it where the lines
// of the history move between lanes.
type graphLine struct {
	row vcs.GraphRow
	// commit is the index of the commit the line draws, -1 for a gap.
	commit int
}

// NewGitGraph builds the history graph.
func NewGitGraph(opts GitGraphOptions) *GitGraphModel {
	w, h := sizeOr(opts.Width, opts.Height)
	m := &GitGraphModel{
		opts:    opts,
		nav:     newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		quitK:   key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		openK:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		searchK: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		nextK:   key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next hit")),
		prevK:   key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "previous hit")),
		width:   w,
		height:  h,
		search:  lineInput{limit: 64},
		focused: true,
	}
	m.lay()
	return m
}

// Typing reports whether the search line is open, which is when every
// printable key belongs to it rather than to the workstation around it.
func (m *GitGraphModel) Typing() bool { return m.seeking }

// SetFocused says whether the keys reach this region. A region of a
// workstation that is not the one being typed at draws its cursor quietly and
// its title muted, so which region answers the keys is never a guess.
func (m *GitGraphModel) SetFocused(on bool) { m.focused = on }

// titleStyle is the heading of the region, muted while the keys are elsewhere.
func (m *GitGraphModel) titleStyle() lipgloss.Style {
	if m.focused {
		return m.opts.Styles.Title
	}
	return m.opts.Styles.Muted
}

// SetCommits puts a new reading on screen, keeping the cursor on the commit
// it was on: a refresh and a longer page must not move the selection.
func (m *GitGraphModel) SetCommits(commits []vcs.Commit, more bool) {
	var on string
	if c, ok := m.Selected(); ok {
		on = c.OID
	}
	m.opts.Commits, m.opts.More, m.asked = commits, more, false
	m.lay()
	if on == "" {
		return
	}
	for i, c := range commits {
		if c.OID == on {
			m.cursor = i
			m.clamp()
			return
		}
	}
}

// SetTitle says what the history on screen is filtered to.
func (m *GitGraphModel) SetTitle(title string) { m.opts.Title = title }

// lay lays the history out and builds the lines drawn from it: a commit
// takes one line, and the gap where lines move between lanes takes one more
// above it.
func (m *GitGraphModel) lay() {
	m.graph = vcs.LayGraph(m.opts.Commits)
	lines := make([]graphLine, 0, 2*len(m.graph.Rows))
	for i, row := range m.graph.Rows {
		if len(row.Link) > 0 {
			lines = append(lines, graphLine{row: row, commit: -1})
		}
		lines = append(lines, graphLine{row: row, commit: i})
	}
	m.lines = lines
	m.clamp()
}

// clamp keeps the cursor on a commit and the commit on screen.
func (m *GitGraphModel) clamp() {
	n := len(m.opts.Commits)
	if n == 0 {
		m.cursor, m.top = 0, 0
		return
	}
	m.cursor = min(max(m.cursor, 0), n-1)
	h := m.bodyHeight()
	line := m.lineOf(m.cursor)
	if line < m.top {
		m.top = line
	}
	if line >= m.top+h {
		m.top = line - h + 1
	}
	m.top = min(max(m.top, 0), max(len(m.lines)-h, 0))
}

// lineOf is the line a commit is drawn on.
func (m *GitGraphModel) lineOf(commit int) int {
	for i, l := range m.lines {
		if l.commit == commit {
			return i
		}
	}
	return 0
}

// GoTo puts the cursor on a commit and brings it on screen. rev is an object
// name or the full name of a ref, which is how a branch chosen in the refs
// pane is followed into the history. It reports false when the history read
// so far reaches no such commit, which is what happens to a branch older than
// the page on screen.
func (m *GitGraphModel) GoTo(rev string) bool {
	if rev == "" {
		return false
	}
	for i, c := range m.opts.Commits {
		if c.OID == rev || hasRef(c.Refs, rev) {
			m.cursor = i
			m.clamp()
			return true
		}
	}
	return false
}

// hasRef reports whether one of the refs of a commit is the one named, by its
// full name so that a branch and a tag of the same name stay apart.
func hasRef(refs []vcs.Ref, full string) bool {
	for _, r := range refs {
		if r.Full == full {
			return true
		}
	}
	return false
}

// Selected is the commit the cursor is on, false on an empty history.
func (m *GitGraphModel) Selected() (vcs.Commit, bool) {
	if m.cursor < 0 || m.cursor >= len(m.opts.Commits) {
		return vcs.Commit{}, false
	}
	return m.opts.Commits[m.cursor], true
}

func (m *GitGraphModel) now() time.Time { return nowOr(m.opts.Now)() }

// Init has nothing to wait for: the graph draws what it is given.
func (m *GitGraphModel) Init() tea.Cmd { return nil }

// Update handles messages.
func (m *GitGraphModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.clamp()
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		return m, m.click(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			return m, m.move(-1)
		case tea.MouseWheelDown:
			return m, m.move(1)
		}
	}
	return m, nil
}

func (m *GitGraphModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.seeking {
		return m.searchKey(msg)
	}
	if d, ok := m.nav.delta(msg, len(m.opts.Commits), m.bodyHeight()); ok {
		return m.move(d)
	}
	switch {
	case key.Matches(msg, m.searchK):
		m.seeking = true
		return nil
	case key.Matches(msg, m.nextK) && m.search.value != "":
		return m.hop(1)
	case key.Matches(msg, m.prevK) && m.search.value != "":
		return m.hop(-1)
	case key.Matches(msg, m.openK):
		return m.open()
	case m.opts.Popup && key.Matches(msg, m.quitK):
		return tea.Quit
	case msg.String() == "esc" && m.search.value != "":
		m.search.value = ""
		return nil
	}
	return nil
}

// searchKey handles the keys of the search line: every letter moves the
// cursor to the first commit that matches from the top, esc gives the search
// up, enter keeps it so n and N walk the hits.
func (m *GitGraphModel) searchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.seeking, m.search.value = false, ""
		return nil
	case "enter":
		m.seeking = false
		return nil
	}
	if !m.search.update(msg) {
		return nil
	}
	if m.search.value == "" {
		return nil
	}
	if i := m.match(0, 1); i >= 0 {
		return m.move(i - m.cursor)
	}
	return nil
}

// hop moves to the hit after the one the cursor is on, wrapping round the
// history.
func (m *GitGraphModel) hop(dir int) tea.Cmd {
	if i := m.match(m.cursor+dir, dir); i >= 0 {
		return m.move(i - m.cursor)
	}
	return nil
}

// match is the first commit from start in the direction dir whose subject,
// author or object name holds what was typed, -1 for no hit at all.
func (m *GitGraphModel) match(start, dir int) int {
	n := len(m.opts.Commits)
	if n == 0 || m.search.value == "" {
		return -1
	}
	for step := range n {
		i := ((start+dir*step)%n + n) % n
		if m.hit(m.opts.Commits[i]) {
			return i
		}
	}
	return -1
}

func (m *GitGraphModel) hit(c vcs.Commit) bool {
	needle := strings.ToLower(m.search.value)
	return strings.Contains(strings.ToLower(c.Subject), needle) ||
		strings.Contains(strings.ToLower(c.Author), needle) ||
		strings.HasPrefix(strings.ToLower(c.OID), needle)
}

// move walks the cursor and reports the commit it landed on, asking for the
// next page when it reaches the end of the one on screen.
func (m *GitGraphModel) move(delta int) tea.Cmd {
	before := m.cursor
	m.cursor += delta
	m.clamp()
	if m.cursor == before {
		return nil
	}
	c, ok := m.Selected()
	if !ok {
		return nil
	}
	cmd := func() tea.Msg { return GitCommitSelectedMsg{Commit: c} }
	if m.wantsMore() {
		m.asked = true
		return tea.Batch(cmd, func() tea.Msg { return GitGraphMoreMsg{} })
	}
	return cmd
}

// wantsMore is true once the cursor is inside the last screen of the page and
// the history goes on past it.
func (m *GitGraphModel) wantsMore() bool {
	if !m.opts.More || m.asked {
		return false
	}
	return m.cursor >= len(m.opts.Commits)-m.bodyHeight()
}

func (m *GitGraphModel) open() tea.Cmd {
	c, ok := m.Selected()
	if !ok {
		return nil
	}
	return func() tea.Msg { return GitCommitChosenMsg{Commit: c} }
}

// click moves the cursor to the row under the pointer; a double click opens
// the commit.
func (m *GitGraphModel) click(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	row := msg.Y - 1
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	line := m.top + row
	if line >= len(m.lines) || m.lines[line].commit < 0 {
		return nil
	}
	idx := m.lines[line].commit
	double := m.clicks.click(m.now(), idx)
	cmd := m.move(idx - m.cursor)
	if double {
		return tea.Batch(cmd, m.open())
	}
	return cmd
}

// bodyHeight is what is left once the title bar, the column header and the
// footer have their line.
func (m *GitGraphModel) bodyHeight() int { return max(m.height-3, 1) }

// View draws the graph.
func (m *GitGraphModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *GitGraphModel) render() string {
	lines := []string{m.header(), m.columns()}
	lines = append(lines, m.body()...)
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], m.footer())
	return screen(lines, m.width, m.height)
}

func (m *GitGraphModel) header() string {
	s := m.opts.Styles
	node := "●"
	if s.Theme.Icons.Name == "ascii" {
		node = "*"
	}
	left := []segment{seg(" "+node+" history ", m.titleStyle())}
	if m.opts.Title != "" {
		left = append(left, seg(" "+sanitize.Line(m.opts.Title)+" ", s.Accent2))
	}
	var right []segment
	if n := len(m.opts.Commits); n > 0 {
		at := strconv.Itoa(m.cursor+1) + "/" + strconv.Itoa(n)
		if m.opts.More {
			at += "+"
		}
		right = append(right, seg(at+" ", s.Muted))
	}
	return s.bar(m.width, left, right)
}

func (m *GitGraphModel) body() []string {
	s := m.opts.Styles
	h := m.bodyHeight()
	if len(m.lines) == 0 {
		return []string{"", s.Muted.Render(center("no commit yet", m.width, s.Ellipsis))}
	}
	end := min(m.top+h, len(m.lines))
	lines := make([]string, 0, h)
	for _, l := range m.lines[m.top:end] {
		if l.commit < 0 {
			lines = append(lines, m.gap(l.row))
			continue
		}
		lines = append(lines, m.row(l.row, l.commit))
	}
	return lines
}

// columns names what each column of the history holds, the way a table says
// what its rows are made of.
func (m *GitGraphModel) columns() string {
	s := m.opts.Styles
	segs := []segment{seg(" ", s.Muted)}
	if w := m.refsWidth(); w > 0 {
		segs = append(segs, seg(fit("BRANCH / TAG", w, s.Ellipsis), s.Muted))
	}
	// A history of one or two lanes has no room for the word: the drawing
	// says what the column is.
	graph := "GRAPH"
	if m.graphWidth() < ansi.StringWidth(graph) {
		graph = ""
	}
	segs = append(segs, seg(fit(graph, m.graphWidth(), s.Ellipsis)+" ", s.Muted))
	var right []segment
	if m.width >= graphAuthorEnough {
		right = []segment{
			seg(" "+fitRight("AUTHOR", graphAuthorWidth, s.Ellipsis), s.Muted),
			seg("  "+fitRight("WHEN", graphAgeWidth, s.Ellipsis)+" ", s.Muted),
		}
	}
	rest := max(m.width-segsWidth(segs)-segsWidth(right), 1)
	segs = append(segs, seg(fit("COMMIT MESSAGE", rest, s.Ellipsis), s.Muted))
	return s.line(m.width, &s.Bar, append(segs, right...)...)
}

// refsWidth is the column the branch and tag chips are drawn in, zero in a
// pane too narrow for one, where the chips are drawn before the subject
// instead.
func (m *GitGraphModel) refsWidth() int {
	if m.width < graphRefsEnough {
		return 0
	}
	return min(max(m.width/5, graphRefsMin), graphRefsMax)
}

// graphWidth is how many cells the lanes take, the drawing cut to the lanes
// the pane has room for.
func (m *GitGraphModel) graphWidth() int {
	width := vcs.CellCount(min(m.graph.Width, maxGraphLanes))
	if m.graph.Width > maxGraphLanes {
		width++
	}
	return width
}

// lanePalette is the color of a lane, one per line of descent and round
// again past the last, which the theme derives from its own palette.
func (m *GitGraphModel) lanePalette() []lipgloss.Style { return m.opts.Styles.Lanes }

// asciiCell is the glyph the ascii icon set draws a cell of the graph with:
// a commit, a merge, a line, and a junction for every corner and crossing.
func asciiCell(r rune) rune {
	switch r {
	case '●':
		return '*'
	case '◆':
		return '#'
	case '│':
		return '|'
	case '─':
		return '-'
	case ' ':
		return ' '
	}
	return '+'
}

// cells draws one line of the lanes, each cell in the color of the lane it
// belongs to and the whole cut to the width of the column.
func (m *GitGraphModel) cells(runes []rune) []segment {
	s := m.opts.Styles
	palette := m.lanePalette()
	cut := min(vcs.CellCount(min(m.graph.Width, maxGraphLanes)), len(runes))
	segs := make([]segment, 0, cut+1)
	ascii := s.Theme.Icons.Name == "ascii"
	for i, r := range runes[:cut] {
		if ascii {
			r = asciiCell(r)
		}
		segs = append(segs, seg(string(r), palette[vcs.CellLane(i)%len(palette)]))
	}
	if cut < len(runes) {
		segs = append(segs, seg(s.Ellipsis, s.Muted))
	}
	return segs
}

// gap draws the lines that move between lanes above a commit, under an empty
// chip column so the lanes stay in their own column.
func (m *GitGraphModel) gap(row vcs.GraphRow) string {
	s := m.opts.Styles
	segs := []segment{seg(" ", s.Text)}
	if w := m.refsWidth(); w > 0 {
		segs = append(segs, seg(strings.Repeat(" ", w), s.Text))
	}
	segs = append(segs, m.cells(vcs.LinkCells(m.graph.Width, row.Link))...)
	return s.line(m.width, nil, segs...)
}

// row draws one commit: the refs pointing at it, its lanes, its object name,
// its subject, and who made it when.
func (m *GitGraphModel) row(row vcs.GraphRow, index int) string {
	s := m.opts.Styles
	c := row.Commit
	var bg *lipgloss.Style
	cursor := " "
	if index == m.cursor {
		bg = &s.Selected
		if cursor = "▌"; s.Theme.Icons.Name == "ascii" {
			cursor = ">"
		}
		if !m.focused {
			bg, cursor = &s.Bar, " "
		}
	}
	segs := []segment{seg(cursor, s.Accent)}
	refs := m.refsWidth()
	if refs > 0 {
		segs = append(segs, m.chips(c, refs, row.Lane)...)
	}
	segs = append(segs, m.cells(vcs.NodeCells(m.graph.Width, row))...)
	segs = append(segs, seg(" ", s.Text))
	if m.width >= graphOIDEnough {
		segs = append(segs, seg(fit(c.Short(), graphOIDWidth, s.Ellipsis)+" ", s.Muted))
	}
	var right []segment
	if m.width >= graphAuthorEnough {
		right = append(right, seg(" "+fitRight(sanitize.Line(c.Author), graphAuthorWidth, s.Ellipsis), s.Muted))
	}
	right = append(right, m.when(index))
	var labels []segment
	if refs == 0 {
		labels = m.labels(c)
	}
	rest := max(m.width-segsWidth(segs)-segsWidth(right)-segsWidth(labels), 1)
	subject := s.Text
	if m.search.value != "" && m.hit(c) {
		subject = s.Warning
	}
	segs = append(segs, labels...)
	segs = append(segs, seg(fit(sanitize.Line(c.Subject), rest, s.Ellipsis), subject))
	return s.line(m.width, bg, append(segs, right...)...)
}

// when is the age of a commit, drawn as a chip on the last commit of a day so
// the history reads in groups rather than as one run of numbers.
func (m *GitGraphModel) when(index int) segment {
	s := m.opts.Styles
	age := " " + fitRight(m.age(m.opts.Commits[index]), graphAgeWidth, s.Ellipsis) + " "
	if m.dayEnds(index) {
		return seg(age, s.Bar.Bold(true))
	}
	return seg(age, s.Muted)
}

// dayEnds reports the oldest commit of a day: the one the commit under it was
// made on another day than.
func (m *GitGraphModel) dayEnds(index int) bool {
	c := m.opts.Commits[index]
	if c.Committed.IsZero() || index+1 >= len(m.opts.Commits) {
		return false
	}
	next := m.opts.Commits[index+1]
	if next.Committed.IsZero() {
		return false
	}
	return c.Committed.YearDay() != next.Committed.YearDay() || c.Committed.Year() != next.Committed.Year()
}

// chips are the refs of a commit drawn in the column of their own: the branch
// the working tree is on first, then the other branches, the tags and the
// branches of the remotes. A branch takes the color of the lane it sits in,
// so the chip and the line of the history are read as one.
func (m *GitGraphModel) chips(c vcs.Commit, width, lane int) []segment {
	s := m.opts.Styles
	ic := s.Theme.Icons
	ascii := ic.Name == "ascii"
	check, branch, tag := "✓", ic.Branch, "#"
	if ascii {
		check, branch = "*", "@"
	}
	palette := m.lanePalette()
	laneStyle := palette[max(lane, 0)%len(palette)]
	var segs []segment
	room := width
	var left int
	for _, ref := range order(c.Refs) {
		mark, style := branch+" ", laneStyle
		switch {
		case ref.Kind == vcs.RefBranch && ref.Head:
			mark, style = check+" ", laneStyle.Bold(true)
		case ref.Kind == vcs.RefTag:
			mark, style = tag, s.Warning
		case ref.Kind == vcs.RefRemote, ref.Kind == vcs.RefOther:
			style = s.Muted
		}
		chip := mark + sanitize.Line(ref.Name)
		// A chip is never cut to nothing: what does not fit is counted
		// instead, so the column says how many refs the commit carries.
		w := ansi.StringWidth(chip) + 1
		if w > room || room < graphRefsMin/2 {
			left++
			continue
		}
		room -= w
		segs = append(segs, seg(chip+" ", style))
	}
	if left > 0 && room >= 3 {
		segs = append(segs, seg("+"+strconv.Itoa(left)+" ", s.Muted))
		room -= 3
	}
	if room > 0 {
		segs = append(segs, seg(strings.Repeat(" ", room), s.Text))
	}
	return segs
}

// order puts the refs of a commit in the order the chips are drawn in.
func order(refs []vcs.Ref) []vcs.Ref {
	rank := func(r vcs.Ref) int {
		switch {
		case r.Kind == vcs.RefBranch && r.Head:
			return 0
		case r.Kind == vcs.RefBranch:
			return 1
		case r.Kind == vcs.RefTag:
			return 2
		default:
			return 3
		}
	}
	out := slices.Clone(refs)
	slices.SortStableFunc(out, func(a, b vcs.Ref) int { return rank(a) - rank(b) })
	return out
}

// labels are the refs drawn before a subject in a pane too narrow for a
// column of their own, each in the brackets of its kind so a frame says what
// a ref is without its colors.
func (m *GitGraphModel) labels(c vcs.Commit) []segment {
	s := m.opts.Styles
	var segs []segment
	for _, ref := range order(c.Refs) {
		var left, right string
		style := s.Accent2
		switch ref.Kind {
		case vcs.RefBranch:
			left, right = "[", "]"
			if ref.Head {
				style = s.Title
			}
		case vcs.RefRemote:
			left, right, style = "{", "}", s.Muted
		case vcs.RefTag:
			left, right, style = "<", ">", s.Warning
		case vcs.RefOther:
			left, right, style = "(", ")", s.Muted
		}
		name := truncate(sanitize.Line(ref.Name), max(m.width/4, 8), s.Ellipsis)
		segs = append(segs, seg(left+name+right+" ", style))
	}
	// The labels never take more than half the line: a commit carrying ten
	// refs still shows what it says.
	half := max(m.width/2, 8)
	for segsWidth(segs) > half && len(segs) > 0 {
		segs = segs[:len(segs)-1]
		if len(segs) > 0 {
			segs[len(segs)-1] = seg(strings.TrimSuffix(segs[len(segs)-1].text, " ")+s.Ellipsis+" ", segs[len(segs)-1].style)
		}
	}
	return segs
}

func (m *GitGraphModel) age(c vcs.Commit) string {
	if c.Committed.IsZero() {
		return ""
	}
	return formatAge(m.now().Sub(c.Committed))
}

func (m *GitGraphModel) footer() string {
	s := m.opts.Styles
	if m.seeking || m.search.value != "" {
		cursor := ""
		if m.seeking {
			cursor = "_"
		}
		hits := ""
		if m.search.value != "" && m.match(0, 1) < 0 {
			hits = " no hit"
		}
		return s.line(m.width, nil,
			seg(" /", s.Key),
			seg(truncate(m.search.value, max(m.width-3, 1), s.Ellipsis), s.Text),
			seg(cursor, s.Accent),
			seg(hits, s.Danger))
	}
	keys := m.helpKeys()
	help := s.helpLine(max(m.width/2, 12), keys...)
	if _, ok := m.Selected(); !ok || ansi.StringWidth(help) >= m.width {
		return help
	}
	c, _ := m.Selected()
	left := []segment{seg(" "+c.Short(), s.Muted)}
	if n := len(c.Parents); n > 1 {
		left = append(left, seg("  "+strconv.Itoa(n)+" parents", s.Muted))
	}
	return s.line(m.width-ansi.StringWidth(help), nil, left...) + help
}

func (m *GitGraphModel) helpKeys() []key.Binding {
	var keys []key.Binding
	if len(m.opts.Commits) > 1 {
		keys = append(keys, m.nav.Down)
	}
	if _, ok := m.Selected(); ok {
		keys = append(keys, m.openK)
	}
	keys = append(keys, m.searchK)
	if m.search.value != "" {
		keys = append(keys, m.nextK)
	}
	if m.opts.Popup {
		keys = append(keys, m.quitK)
	}
	return keys
}
