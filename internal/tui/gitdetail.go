package tui

import (
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

// maxDetailBody is how much of a message the detail pane draws. A message
// longer than this is a file someone committed as one, and the pane says so
// rather than scrolling through it.
const maxDetailBody = 200

// GitFileChosenMsg is the file the user opened in the detail pane, with the
// commit it belongs to, empty for a file of the working tree.
type GitFileChosenMsg struct {
	Path string
	Rev  string
}

// GitDetailOptions configure the detail pane.
type GitDetailOptions struct {
	Styles Styles
	// Width and Height size the first frame.
	Width, Height int
	// Popup makes q and esc close the pane.
	Popup bool
	// Now is the clock ages and double clicks are measured with.
	Now func() time.Time
}

// gitDetailKind is what the pane is showing.
type gitDetailKind int

const (
	// detailNothing is a pane waiting for the first reading, detailReading
	// one whose reading is in flight, and detailFailed one whose reading
	// failed.
	detailNothing gitDetailKind = iota
	detailReading
	detailFailed
	detailCommit
	detailChanges
)

// detailLine is one drawn line and the file it stands for, -1 for a line that
// is not a file.
type detailLine struct {
	segs []segment
	file int
}

// GitDetailModel is the right of the workstation: the commit the history is
// on, or the working tree in front of you.
type GitDetailModel struct {
	opts    GitDetailOptions
	nav     navKeys
	quitK   key.Binding
	openK   key.Binding
	width   int
	height  int
	kind    gitDetailKind
	commit  vcs.CommitDetail
	changes vcs.Changes
	err     error
	// paths are the files of what is shown, in the order they are drawn, and
	// cursor the one the user is on.
	paths  []GitFileChosenMsg
	cursor int
	top    int
	clicks clicks
}

// NewGitDetail builds the detail pane.
func NewGitDetail(opts GitDetailOptions) *GitDetailModel {
	w, h := sizeOr(opts.Width, opts.Height)
	return &GitDetailModel{
		opts:   opts,
		nav:    newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		quitK:  key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		openK:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "diff")),
		width:  w,
		height: h,
	}
}

// ShowCommit draws one commit read in full.
func (m *GitDetailModel) ShowCommit(d vcs.CommitDetail) {
	m.kind, m.commit, m.err = detailCommit, d, nil
	m.reset()
}

// ShowChanges draws the working tree.
func (m *GitDetailModel) ShowChanges(c vcs.Changes) {
	m.kind, m.changes, m.err = detailChanges, c, nil
	m.reset()
}

// Reading says a reading is in flight, which the pane draws rather than
// leaving what it was showing under a cursor that no longer means anything.
func (m *GitDetailModel) Reading() {
	m.kind, m.err = detailReading, nil
	m.reset()
}

// ShowError draws what went wrong instead of a reading.
func (m *GitDetailModel) ShowError(err error) {
	m.kind, m.err = detailFailed, err
	m.reset()
}

// reset puts the cursor back at the top: what is drawn is another thing
// entirely, so the row the cursor was on means nothing here. It draws the
// lines once, which is what names the files, so a key pressed before the
// first frame lands on the file the frame will show. It does not bring the
// cursor into view: a commit opens on its message, and the files are scrolled
// to by whoever walks them.
func (m *GitDetailModel) reset() {
	m.cursor, m.top = 0, 0
	m.lines()
}

// Selected is the file the cursor is on, false when there is none.
func (m *GitDetailModel) Selected() (GitFileChosenMsg, bool) {
	if m.cursor < 0 || m.cursor >= len(m.paths) {
		return GitFileChosenMsg{}, false
	}
	return m.paths[m.cursor], true
}

func (m *GitDetailModel) now() time.Time { return nowOr(m.opts.Now)() }

// Init has nothing to wait for: the pane draws what it is given.
func (m *GitDetailModel) Init() tea.Cmd { return nil }

// Update handles messages.
func (m *GitDetailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.scroll(-1)
		case tea.MouseWheelDown:
			m.scroll(1)
		}
	}
	return m, nil
}

func (m *GitDetailModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	if d, ok := m.nav.delta(msg, len(m.paths), m.bodyHeight()); ok {
		m.move(d)
		return nil
	}
	switch {
	case key.Matches(msg, m.openK):
		return m.open()
	case m.opts.Popup && key.Matches(msg, m.quitK):
		return tea.Quit
	}
	return nil
}

// move walks the cursor over the files and brings the line it is on into
// view.
func (m *GitDetailModel) move(delta int) {
	m.cursor += delta
	m.clamp()
}

// scroll moves the view without moving the cursor, which is how a long
// message is read.
func (m *GitDetailModel) scroll(delta int) {
	lines := m.lines()
	m.top = min(max(m.top+delta, 0), max(len(lines)-m.bodyHeight(), 0))
}

func (m *GitDetailModel) clamp() {
	lines := m.lines()
	h := m.bodyHeight()
	if len(m.paths) == 0 {
		m.cursor = 0
		m.top = min(max(m.top, 0), max(len(lines)-h, 0))
		return
	}
	m.cursor = min(max(m.cursor, 0), len(m.paths)-1)
	at := -1
	for i, l := range lines {
		if l.file == m.cursor {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	if at < m.top {
		m.top = at
	}
	if at >= m.top+h {
		m.top = at - h + 1
	}
	m.top = min(max(m.top, 0), max(len(lines)-h, 0))
}

func (m *GitDetailModel) open() tea.Cmd {
	file, ok := m.Selected()
	if !ok {
		return nil
	}
	return func() tea.Msg { return file }
}

// click puts the cursor on the file under the pointer; a double click opens
// its diff.
func (m *GitDetailModel) click(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	row := msg.Y - 1
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	lines := m.lines()
	at := m.top + row
	if at >= len(lines) || lines[at].file < 0 {
		return nil
	}
	idx := lines[at].file
	double := m.clicks.click(m.now(), idx)
	m.move(idx - m.cursor)
	if double {
		return m.open()
	}
	return nil
}

func (m *GitDetailModel) bodyHeight() int { return max(m.height-2, 1) }

// View draws the pane.
func (m *GitDetailModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *GitDetailModel) render() string {
	s := m.opts.Styles
	lines := m.lines()
	out := []string{m.header()}
	end := min(m.top+m.bodyHeight(), len(lines))
	for i := m.top; i < end; i++ {
		var bg *lipgloss.Style
		if lines[i].file >= 0 && lines[i].file == m.cursor {
			bg = &s.Selected
		}
		out = append(out, s.line(m.width, bg, lines[i].segs...))
	}
	for len(out) < m.height-1 {
		out = append(out, "")
	}
	out = append(out[:min(len(out), max(m.height-1, 0))], m.footer())
	return screen(out, m.width, m.height)
}

func (m *GitDetailModel) header() string {
	s := m.opts.Styles
	ic := s.Theme.Icons
	var left, right []segment
	switch m.kind {
	case detailChanges:
		left = []segment{seg(" "+ic.Changes+" working tree ", s.Title)}
		if n := m.changes.Staged(); n > 0 {
			right = append(right, seg(strconv.Itoa(n)+" staged ", s.Success))
		}
	case detailCommit:
		left = []segment{seg(" "+ic.Branch+" commit ", s.Title)}
		right = append(right, seg(m.commit.Commit.Short()+" ", s.Muted))
	case detailNothing, detailReading, detailFailed:
		left = []segment{seg(" "+ic.Branch+" detail ", s.Title)}
	}
	return s.bar(m.width, left, right)
}

// lines are everything the pane draws under its header, with the file each
// one stands for. It builds the list of files as it goes, so what the cursor
// walks is exactly what is on screen.
func (m *GitDetailModel) lines() []detailLine {
	s := m.opts.Styles
	m.paths = nil
	switch m.kind {
	case detailNothing:
		return []detailLine{text("", s.Muted, -1), text(center("no commit chosen", m.width, s.Ellipsis), s.Muted, -1)}
	case detailReading:
		return []detailLine{text("", s.Muted, -1), text(center("reading the commit", m.width, s.Ellipsis), s.Muted, -1)}
	case detailFailed:
		return m.wrapped(sanitize.Line(m.err.Error()), s.Danger)
	case detailChanges:
		return m.changesLines()
	}
	return m.commitLines()
}

// text is one line of a single style.
func text(content string, style lipgloss.Style, file int) detailLine {
	return detailLine{segs: []segment{seg(content, style)}, file: file}
}

// wrapped breaks text into the lines of the pane, so a message is read
// rather than cut. Every line it makes carries the gutter the pane draws its
// other lines with, so a message that wraps stays a block.
func (m *GitDetailModel) wrapped(content string, style lipgloss.Style) []detailLine {
	var out []detailLine
	for _, para := range strings.Split(content, "\n") {
		wrapped := ansi.Wrap(strings.TrimRight(para, " "), max(m.width-2, 1), "")
		for _, l := range strings.Split(wrapped, "\n") {
			out = append(out, text(" "+l, style, -1))
		}
	}
	return out
}

// commitLines draw one commit: its message, who made it and who landed it,
// what it points at, and the files it touched.
func (m *GitDetailModel) commitLines() []detailLine {
	s := m.opts.Styles
	d := m.commit
	c := d.Commit
	out := m.wrapped(sanitize.Line(c.Subject), s.Text)
	if d.Body != "" {
		body := sanitize.Line(d.Body)
		if len(body) > maxDetailBody {
			body = truncate(body, maxDetailBody, s.Ellipsis)
		}
		out = append(out, text("", s.Muted, -1))
		out = append(out, m.wrapped(body, s.Muted)...)
	}
	out = append(out, text("", s.Muted, -1))
	out = append(out, m.who(" authored by ", c.Author, d.AuthorEmail, c.Authored))
	// A rebase, a patch or an amend makes the committer someone else, or the
	// same person at another time; it is only worth a line when it differs.
	if d.Committer != c.Author || !c.Committed.Equal(c.Authored) {
		out = append(out, m.who(" landed by   ", d.Committer, d.CommitterEmail, c.Committed))
	}
	if len(c.Parents) > 0 {
		short := make([]string, 0, len(c.Parents))
		for _, p := range c.Parents {
			short = append(short, vcs.Commit{OID: p}.Short())
		}
		label := " parent      "
		if len(short) > 1 {
			label = " parents     "
		}
		out = append(out, detailLine{file: -1, segs: []segment{
			seg(label, s.Muted), seg(strings.Join(short, " "), s.Accent2),
		}})
	}
	if len(c.Refs) > 0 {
		out = append(out, detailLine{file: -1, segs: append([]segment{seg(" refs        ", s.Muted)}, m.refs(c.Refs)...)})
	}
	out = append(out, text("", s.Muted, -1))
	out = append(out, m.fileHeader(len(d.Files), d.Added(), d.Deleted()))
	for _, f := range d.Files {
		out = append(out, m.fileRow(f, c.OID, "", len(m.paths)))
	}
	return out
}

// who is one of the two lines naming a person and when they acted. The age is
// held against the right edge, so a narrow pane cuts the address rather than
// the one thing that says how old the commit is.
func (m *GitDetailModel) who(label, name, email string, at time.Time) detailLine {
	s := m.opts.Styles
	var age string
	if !at.IsZero() {
		age = formatAge(m.now().Sub(at)) + " ago "
	}
	who := sanitize.Line(name)
	if email != "" {
		who += " <" + sanitize.Line(email) + ">"
	}
	width := max(m.width-ansi.StringWidth(label)-ansi.StringWidth(age), 4)
	return detailLine{file: -1, segs: []segment{
		seg(label, s.Muted), seg(fit(who, width, s.Ellipsis), s.Text), seg(age, s.Muted),
	}}
}

// refs are the branches, tags and remote branches of a commit, drawn as the
// history draws them.
func (m *GitDetailModel) refs(refs []vcs.Ref) []segment {
	s := m.opts.Styles
	var segs []segment
	for _, ref := range order(refs) {
		style := s.Accent2
		switch ref.Kind {
		case vcs.RefTag:
			style = s.Warning
		case vcs.RefRemote, vcs.RefOther:
			style = s.Muted
		case vcs.RefBranch:
			if ref.Head {
				style = s.Accent.Bold(true)
			}
		}
		segs = append(segs, seg(truncate(sanitize.Line(ref.Name), max(m.width/3, 8), s.Ellipsis)+" ", style))
	}
	return segs
}

// fileHeader is the line the files are counted on.
func (m *GitDetailModel) fileHeader(n, added, deleted int) detailLine {
	s := m.opts.Styles
	segs := []segment{seg(" "+plural(n, "file", "files"), s.Title)}
	if added > 0 || deleted > 0 {
		segs = append(segs,
			seg("  +"+strconv.Itoa(added), s.Success),
			seg(" -"+strconv.Itoa(deleted), s.Danger))
	}
	return detailLine{segs: segs, file: -1}
}

// fileRow draws one file with its counts, and records what opening it means.
func (m *GitDetailModel) fileRow(f vcs.NumStat, rev, mark string, index int) detailLine {
	s := m.opts.Styles
	const counts = 11
	m.paths = append(m.paths, GitFileChosenMsg{Path: f.Path, Rev: rev})
	arrow := " → "
	if s.Theme.Icons.Name == "ascii" {
		arrow = " -> "
	}
	path := sanitize.Line(f.Path)
	if f.OrigPath != "" {
		path = sanitize.Line(f.OrigPath) + arrow + path
	}
	var tail []segment
	switch {
	case f.Binary:
		tail = []segment{seg(fitRight("bin", counts, s.Ellipsis), s.Muted)}
	default:
		tail = []segment{
			seg(fitRight("+"+strconv.Itoa(f.Added), counts/2, s.Ellipsis), s.Success),
			seg(fitRight(" -"+strconv.Itoa(f.Deleted), counts-counts/2, s.Ellipsis), s.Danger),
		}
	}
	head := " " + mark
	width := max(m.width-ansi.StringWidth(head)-counts-1, 4)
	return detailLine{
		file: index,
		segs: append([]segment{seg(head, s.Accent), seg(fit(path, width, s.Ellipsis), s.Text)}, tail...),
	}
}

// changesLines draw the working tree: what is staged, what is not, and what
// git does not follow yet.
func (m *GitDetailModel) changesLines() []detailLine {
	s := m.opts.Styles
	c := m.changes
	if c.Clean() {
		return []detailLine{text("", s.Idle, -1), text(center("working tree clean", m.width, s.Ellipsis), s.Idle, -1)}
	}
	var staged, unstaged []vcs.File
	for _, f := range c.Files {
		if f.Staged() {
			staged = append(staged, f)
		}
		if f.Unstaged() {
			unstaged = append(unstaged, f)
		}
	}
	var out []detailLine
	out = append(out, m.section("STAGED", staged)...)
	if len(staged) > 0 && len(unstaged) > 0 {
		out = append(out, text("", s.Muted, -1))
	}
	out = append(out, m.section("UNSTAGED", unstaged)...)
	return out
}

// section is one list of the working tree with its heading.
func (m *GitDetailModel) section(title string, files []vcs.File) []detailLine {
	s := m.opts.Styles
	if len(files) == 0 {
		return nil
	}
	var added, deleted int
	for _, f := range files {
		added, deleted = added+f.Added, deleted+f.Deleted
	}
	out := []detailLine{{file: -1, segs: []segment{
		seg(" "+title, s.Title), seg("  "+strconv.Itoa(len(files)), s.Muted),
		seg("  +"+strconv.Itoa(added), s.Success), seg(" -"+strconv.Itoa(deleted), s.Danger),
	}}}
	for _, f := range files {
		mark := string(f.Worktree)
		if title == "STAGED" {
			mark = string(f.Index)
		}
		if f.Kind == vcs.KindUntracked {
			mark = "?"
		}
		out = append(out, m.fileRow(vcs.NumStat{
			Path: f.Path, OrigPath: f.OrigPath, Added: f.Added, Deleted: f.Deleted, Binary: f.Binary,
		}, "", mark+" ", len(m.paths)))
	}
	return out
}

func (m *GitDetailModel) footer() string {
	s := m.opts.Styles
	keys := m.helpKeys()
	help := s.helpLine(max(m.width/2, 12), keys...)
	file, ok := m.Selected()
	if !ok || ansi.StringWidth(help) >= m.width {
		return help
	}
	left := []segment{seg(" "+strconv.Itoa(m.cursor+1)+"/"+strconv.Itoa(len(m.paths)), s.Muted)}
	if file.Rev != "" {
		left = append(left, seg("  "+vcs.Commit{OID: file.Rev}.Short(), s.Muted))
	}
	return s.line(m.width-ansi.StringWidth(help), nil, left...) + help
}

func (m *GitDetailModel) helpKeys() []key.Binding {
	var keys []key.Binding
	if len(m.paths) > 1 {
		keys = append(keys, m.nav.Down)
	}
	if _, ok := m.Selected(); ok {
		keys = append(keys, m.openK)
	}
	if m.opts.Popup {
		keys = append(keys, m.quitK)
	}
	return keys
}
