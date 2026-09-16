package tui

import (
	"errors"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
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
}

type (
	changesUpdateMsg struct{ update watch.Update }
	changesClosedMsg struct{}
)

// ChangesModel is the live changes view of a working tree.
type ChangesModel struct {
	opts   ChangesOptions
	nav    navKeys
	quitK  key.Binding
	width  int
	height int
	have   bool
	closed bool
	update watch.Update
	offset int
}

// NewChanges builds the changes view.
func NewChanges(opts ChangesOptions) *ChangesModel {
	w, h := sizeOr(opts.Width, opts.Height)
	return &ChangesModel{
		opts:   opts,
		nav:    newNavKeys(),
		quitK:  key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		width:  w,
		height: h,
	}
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
		m.clampOffset()
	case changesUpdateMsg:
		m.update, m.have = msg.update, true
		m.clampOffset()
		return m, m.wait()
	case changesClosedMsg:
		m.closed = true
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || (m.opts.Popup && key.Matches(msg, m.quitK)) {
			return m, tea.Quit
		}
		if d, ok := m.nav.delta(msg, len(m.update.Changes.Files), m.bodyHeight()); ok {
			m.offset += d
			m.clampOffset()
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.offset -= 3
		case tea.MouseWheelDown:
			m.offset += 3
		}
		m.clampOffset()
	}
	return m, nil
}

func (m *ChangesModel) bodyHeight() int { return max(m.height-2, 1) }

func (m *ChangesModel) clampOffset() {
	n := len(m.update.Changes.Files)
	m.offset = min(max(m.offset, 0), max(n-m.bodyHeight(), 0))
}

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
	end := min(m.offset+h, len(files))
	lines := make([]string, 0, h)
	for _, f := range files[m.offset:end] {
		lines = append(lines, m.fileRow(f))
	}
	return lines
}

func (m *ChangesModel) fileRow(f watch.File) string {
	s := m.opts.Styles
	const countsWidth = 12
	codes := []segment{seg(" ", s.Text)}
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
	return s.line(m.width, nil, append(codes, counts...)...)
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
	if m.have {
		segs = append(segs, seg("updated "+m.update.At.Format("15:04:05"), s.Muted))
		if n := m.update.Changes.Staged(); n > 0 && m.update.Err == nil {
			segs = append(segs, seg("  "+strconv.Itoa(n)+" staged", s.Success))
		}
	}
	if m.closed {
		segs = append(segs, seg("  watcher stopped", s.Warning))
	}
	if !m.opts.Popup {
		return s.line(m.width, nil, segs...)
	}
	help := s.helpLine(max(m.width/2, 12), m.nav.Down, m.quitK)
	hw := ansi.StringWidth(help)
	if hw >= m.width {
		return help
	}
	return s.line(m.width-hw, nil, segs...) + help
}
