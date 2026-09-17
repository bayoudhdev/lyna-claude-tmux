package tui

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// ChangesOptions configure the changes view.
type ChangesOptions struct {
	Styles Styles
	// Updates delivers watcher results; the view waits on it for its whole
	// life. A closed channel keeps the last state on screen.
	Updates <-chan watch.Update
	// Popup makes q and esc close the view; in a pane they do nothing, so a
	// stray key never removes the changes pane from a layout.
	Popup bool
	// Dir names the watched directory in the header and errors.
	Dir  string
	Home string
	// Width and Height size the first frame.
	Width, Height int
	// Open opens the review of one file, OpenReview the review of the whole
	// working tree. They are nil where there is nothing to open into, outside
	// tmux and in the popup that a review would cover, and the keys then do
	// nothing rather than reporting a failure the user cannot act on.
	Open       func(path string) tea.Cmd
	OpenReview func() tea.Cmd
	// Now is the clock double clicks are measured with; time.Now when nil.
	Now func() time.Time
}

type (
	changesUpdateMsg struct{ update watch.Update }
	changesClosedMsg struct{}
)

// ChangesNoteMsg is one line for the footer of the changes view: what an
// action the view started has to say when it could not be carried out. The
// next key press clears it; nothing else does, so a line the user was not
// looking at is still there when they look.
type ChangesNoteMsg struct{ Text string }

// ChangesNote builds the note a command reports a failure with.
func ChangesNote(text string) tea.Msg { return ChangesNoteMsg{Text: sanitize.Line(text)} }

// ChangesModel is the live changes view of a working tree.
type ChangesModel struct {
	opts    ChangesOptions
	nav     navKeys
	quitK   key.Binding
	openK   key.Binding
	reviewK key.Binding
	width   int
	height  int
	have    bool
	closed  bool
	update  watch.Update
	list    listView
	clicks  clicks
	note    string
}

// NewChanges builds the changes view.
func NewChanges(opts ChangesOptions) *ChangesModel {
	w, h := sizeOr(opts.Width, opts.Height)
	return &ChangesModel{
		opts:    opts,
		nav:     newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		quitK:   key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		openK:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "diff")),
		reviewK: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "review")),
		width:   w,
		height:  h,
	}
}

// now is the clock double clicks are measured with.
func (m *ChangesModel) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

// files are the rows of the list, empty until the first reading and whenever
// the working tree is clean or unreadable.
func (m *ChangesModel) files() []watch.File {
	if !m.have || m.update.Err != nil {
		return nil
	}
	return m.update.Changes.Files
}

// selected is the file the cursor is on.
func (m *ChangesModel) selected() (watch.File, bool) {
	files := m.files()
	if m.list.cursor < 0 || m.list.cursor >= len(files) {
		return watch.File{}, false
	}
	return files[m.list.cursor], true
}

// open runs the review of the file under the cursor, or of the whole working
// tree when all is set.
func (m *ChangesModel) open(all bool) tea.Cmd {
	if all {
		if m.opts.OpenReview == nil {
			return nil
		}
		return m.opts.OpenReview()
	}
	f, ok := m.selected()
	if !ok || m.opts.Open == nil {
		return nil
	}
	// A rename is reviewed under the path the file has now.
	return m.opts.Open(f.Path)
}

// Init waits for the first update.
func (m *ChangesModel) Init() tea.Cmd { return m.wait() }

func (m *ChangesModel) wait() tea.Cmd {
	ch := m.opts.Updates
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return changesClosedMsg{}
		}
		return changesUpdateMsg{update: u}
	}
}

// Update handles messages.
func (m *ChangesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.clampList()
	case changesUpdateMsg:
		m.update, m.have = msg.update, true
		m.clampList()
		return m, m.wait()
	case changesClosedMsg:
		m.closed = true
	case ChangesNoteMsg:
		m.note = msg.Text
	case tea.KeyPressMsg:
		m.note = ""
		return m, m.key(msg)
	case tea.MouseClickMsg:
		return m, m.click(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.move(-1, len(m.files()), m.bodyHeight())
		case tea.MouseWheelDown:
			m.list.move(1, len(m.files()), m.bodyHeight())
		}
	}
	return m, nil
}

// key handles one key press. The movement and opening keys work in a pane as
// well as in a popup: a changes pane the user cannot move through is a list
// they can only read the top of. Only closing is held back to the popup, so a
// stray q never takes the pane out of a layout.
func (m *ChangesModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" || (m.opts.Popup && key.Matches(msg, m.quitK)) {
		return tea.Quit
	}
	if d, ok := m.nav.delta(msg, len(m.files()), m.bodyHeight()); ok {
		m.list.move(d, len(m.files()), m.bodyHeight())
		return nil
	}
	switch {
	case key.Matches(msg, m.openK):
		return m.open(false)
	case key.Matches(msg, m.reviewK):
		return m.open(true)
	}
	return nil
}

// click moves the cursor to the row under the pointer; a double click on the
// same row opens its diff.
func (m *ChangesModel) click(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	// The first body row is drawn under the header.
	row := msg.Y - 1
	files := m.files()
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	idx := m.list.offset + row
	if idx >= len(files) {
		return nil
	}
	double := m.clicks.click(m.now(), idx)
	m.list.move(idx-m.list.cursor, len(files), m.bodyHeight())
	if double {
		return m.open(false)
	}
	return nil
}

func (m *ChangesModel) bodyHeight() int { return max(m.height-2, 1) }

func (m *ChangesModel) clampList() { m.list.clamp(len(m.files()), m.bodyHeight()) }

// View draws the changes.
func (m *ChangesModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *ChangesModel) render() string {
	lines := []string{m.header()}
	lines = append(lines, m.body()...)
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], m.footer())
	return screen(lines, m.width, m.height)
}

func (m *ChangesModel) header() string {
	s := m.opts.Styles
	ic := s.Theme.Icons
	left := []segment{seg(" "+ic.Changes+" changes ", s.Title)}
	if m.have && m.update.Err == nil {
		left = append(left, m.branch()...)
	} else if m.opts.Dir != "" {
		left = append(left, seg(" "+shortPath(sanitize.Line(m.opts.Dir), m.opts.Home)+" ", s.Muted))
	}
	var right []segment
	if m.have && m.update.Err == nil && !m.update.Changes.Clean() {
		ch := m.update.Changes
		right = append(right,
			seg(plural(len(ch.Files), "file", "files")+"  ", s.Text),
			seg("+"+strconv.Itoa(ch.Added), s.Success),
			seg(" -"+strconv.Itoa(ch.Deleted)+" ", s.Danger))
	}
	return s.bar(m.width, left, right)
}

func (m *ChangesModel) branch() []segment {
	s := m.opts.Styles
	ic := s.Theme.Icons
	b := m.update.Changes.Branch
	var name string
	switch {
	case b.Detached && len(b.OID) >= 7:
		name = "detached " + b.OID[:7]
	case b.Detached:
		name = "detached"
	default:
		name = sanitize.Line(b.Head)
	}
	segs := []segment{seg(" "+ic.Branch+" "+name, s.Accent2)}
	if b.Initial {
		segs = append(segs, seg(" no commits yet", s.Muted))
	}
	if b.AheadBehind && (b.Ahead > 0 || b.Behind > 0) {
		up, down := "↑", "↓"
		if ic.Name == "ascii" {
			up, down = "^", "v"
		}
		segs = append(segs, seg(" "+up+strconv.Itoa(b.Ahead)+" "+down+strconv.Itoa(b.Behind), s.Warning))
	}
	return append(segs, seg(" ", s.Muted))
}

func (m *ChangesModel) body() []string {
	s := m.opts.Styles
	h := m.bodyHeight()
	switch {
	case !m.have:
		return []string{"", s.Muted.Render(center("reading git status", m.width, s.Ellipsis))}
	case m.update.Err != nil:
		msg := sanitize.Line(m.update.Err.Error())
		if errors.Is(m.update.Err, watch.ErrNotRepository) {
			msg = "not a git repository: " + shortPath(sanitize.Line(m.opts.Dir), m.opts.Home)
		}
		return []string{"", " " + s.Danger.Render(truncate(msg, m.width-2, s.Ellipsis))}
	case m.update.Changes.Clean():
		return []string{"", s.Idle.Render(center("working tree clean", m.width, s.Ellipsis))}
	}
	files := m.update.Changes.Files
	end := min(m.list.offset+h, len(files))
	lines := make([]string, 0, h)
	for i, f := range files[m.list.offset:end] {
		lines = append(lines, m.fileRow(f, m.list.offset+i == m.list.cursor))
	}
	return lines
}

func (m *ChangesModel) fileRow(f watch.File, selected bool) string {
	s := m.opts.Styles
	const countsWidth = 12
	var bg *lipgloss.Style
	marker := " "
	if selected {
		marker = "▌"
		if s.Theme.Icons.Name == "ascii" {
			marker = ">"
		}
		bg = &s.Selected
	}
	codes := []segment{seg(marker, s.Accent)}
	switch f.Kind {
	case watch.KindUntracked:
		codes = append(codes, seg("??", s.Danger))
	case watch.KindUnmerged:
		codes = append(codes, seg(string([]byte{f.Index, f.Worktree}), s.Waiting))
	default:
		codes = append(codes, seg(codeChar(f.Index), s.Success), seg(codeChar(f.Worktree), s.Danger))
	}
	arrow := " → "
	if s.Theme.Icons.Name == "ascii" {
		arrow = " -> "
	}
	path := sanitize.Line(f.Path)
	if f.OrigPath != "" {
		path = sanitize.Line(f.OrigPath) + arrow + path
	}
	pathWidth := max(m.width-3-1-countsWidth-1, 4)
	codes = append(codes, seg(" "+fit(path, pathWidth, s.Ellipsis), s.Text))
	var counts []segment
	switch {
	case f.Binary:
		counts = []segment{seg("bin", s.Muted)}
	case f.Kind == watch.KindUntracked:
		counts = []segment{seg("new", s.Muted)}
	default:
		counts = []segment{seg("+"+strconv.Itoa(f.Added), s.Success), seg(" -"+strconv.Itoa(f.Deleted), s.Danger)}
	}
	codes = append(codes, seg(strings.Repeat(" ", max(countsWidth-segsWidth(counts), 0)+1), s.Text))
	return s.line(m.width, bg, append(codes, counts...)...)
}

func codeChar(c byte) string {
	if c == watch.Unchanged {
		return " "
	}
	return string(c)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func (m *ChangesModel) footer() string {
	s := m.opts.Styles
	segs := []segment{seg(" ", s.Muted)}
	// A note answers the key that was just pressed, and a narrow footer has
	// room for one thing: it takes the line the reading time is on, which the
	// next key press gives back.
	if m.note != "" {
		segs = append(segs, seg(m.note, s.Danger))
	} else {
		if m.have {
			segs = append(segs, seg("updated "+m.update.At.Format("15:04:05"), s.Muted))
			if n := m.update.Changes.Staged(); n > 0 && m.update.Err == nil {
				segs = append(segs, seg("  "+strconv.Itoa(n)+" staged", s.Success))
			}
		}
		if m.closed {
			segs = append(segs, seg("  watcher stopped", s.Warning))
		}
	}
	keys := m.helpKeys()
	if len(keys) == 0 {
		return s.line(m.width, nil, segs...)
	}
	help := s.helpLine(max(m.width/2, 12), keys...)
	hw := ansi.StringWidth(help)
	if hw >= m.width {
		return help
	}
	return s.line(m.width-hw, nil, segs...) + help
}

// helpKeys are the keys the footer offers: movement whenever there is a list
// to move through, opening whenever there is somewhere to open into, and
// closing only in a popup, which is the only place a key closes the view.
func (m *ChangesModel) helpKeys() []key.Binding {
	var keys []key.Binding
	if len(m.files()) > 1 {
		keys = append(keys, m.nav.Down)
	}
	if m.opts.Open != nil && len(m.files()) > 0 {
		keys = append(keys, m.openK)
	}
	if m.opts.OpenReview != nil {
		keys = append(keys, m.reviewK)
	}
	if m.opts.Popup {
		keys = append(keys, m.quitK)
	}
	return keys
}
