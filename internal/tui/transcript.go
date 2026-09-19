package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/transcript"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// TranscriptUpdate is one reading of a transcript: what it gained since the
// reading before.
type TranscriptUpdate struct {
	// Entries are the entries the transcript gained, in the order they were
	// written.
	Entries []transcript.Entry
	// Restart reports a transcript read from its start again, because the file
	// on screen was rewritten or replaced: what is shown belongs to the file
	// that is gone.
	Restart bool
	// Err is what went wrong reading it; the reader keeps what it has and
	// says so.
	Err error
	// At is when the reading was taken.
	At time.Time
}

// TranscriptOptions configure the reader.
type TranscriptOptions struct {
	Styles Styles
	// Updates delivers the readings; the reader waits on it for its whole
	// life. A closed channel keeps what is on screen.
	Updates <-chan TranscriptUpdate
	// Title names the agent whose transcript this is.
	Title string
	// Width and Height size the first frame.
	Width, Height int
	// Now is the clock the age of the last entry is measured with; time.Now
	// when nil.
	Now func() time.Time
}

// TranscriptKeep bounds the entries the reader holds. A session runs for
// hours and its transcript is read from its start, so the reader keeps the end
// of it, which is the part a reader follows.
const TranscriptKeep = 2000

type (
	transcriptUpdateMsg struct{ update TranscriptUpdate }
	transcriptClosedMsg struct{}
)

// TranscriptModel draws one agent's transcript while it is written.
type TranscriptModel struct {
	opts   TranscriptOptions
	nav    navKeys
	quitK  key.Binding
	width  int
	height int
	have   bool
	closed bool
	err    error
	// entries are what the transcript said, oldest first, and lines the same
	// wrapped to wrappedAt cells.
	entries []transcript.Entry
	lines   []transcriptLine
	// wrappedAt is the width the lines were built for, and drawn how many
	// entries they hold, so a reading that only appends only draws what it
	// added.
	wrappedAt, drawn int
	// offset is the first line drawn, and follow keeps it at the end while the
	// transcript grows.
	offset int
	follow bool
}

// transcriptLine is one drawn line: its segments and nothing else, so a redraw
// of the same width does not build them again.
type transcriptLine struct {
	segs []segment
}

// NewTranscript builds the reader.
func NewTranscript(opts TranscriptOptions) *TranscriptModel {
	w, h := sizeOr(opts.Width, opts.Height)
	return &TranscriptModel{
		opts:   opts,
		nav:    newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		quitK:  key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		width:  w,
		height: h,
		follow: true,
	}
}

func (m *TranscriptModel) now() time.Time { return nowOr(m.opts.Now)() }

// Init waits for the first reading.
func (m *TranscriptModel) Init() tea.Cmd { return m.wait() }

func (m *TranscriptModel) wait() tea.Cmd {
	ch := m.opts.Updates
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return transcriptClosedMsg{}
		}
		return transcriptUpdateMsg{update: u}
	}
}

// Update handles messages.
func (m *TranscriptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.rewrap()
	case transcriptUpdateMsg:
		m.add(msg.update)
		return m, m.wait()
	case transcriptClosedMsg:
		m.closed = true
	case tea.KeyPressMsg:
		return m, m.key(msg)
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

// add takes one reading: a transcript read from its start again replaces what
// is on screen, and the rest is appended.
func (m *TranscriptModel) add(u TranscriptUpdate) {
	m.have, m.err = true, u.Err
	if u.Restart {
		m.entries = nil
	}
	m.entries = append(m.entries, u.Entries...)
	if len(m.entries) > TranscriptKeep {
		m.entries = append([]transcript.Entry(nil), m.entries[len(m.entries)-TranscriptKeep:]...)
		u.Restart = true
	}
	if u.Restart {
		m.lines, m.wrappedAt, m.drawn = nil, 0, 0
	}
	m.rewrap()
}

// key handles one key press. The reader only reads, so every key moves through
// the transcript or leaves it.
func (m *TranscriptModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" || key.Matches(msg, m.quitK) {
		return tea.Quit
	}
	if d, ok := m.nav.delta(msg, len(m.lines), m.bodyHeight()); ok {
		m.scroll(d)
	}
	return nil
}

// scroll moves the window over the lines. Reaching the end follows the
// transcript again, and leaving it stops following, so a reader who scrolled
// up stays where they were reading while the agent writes.
func (m *TranscriptModel) scroll(delta int) {
	m.offset = min(max(m.offset+delta, 0), m.maxOffset())
	m.follow = m.offset == m.maxOffset()
}

func (m *TranscriptModel) maxOffset() int { return max(len(m.lines)-m.bodyHeight(), 0) }

// bodyHeight is the number of lines the transcript is drawn in: the header and
// the footer take one each.
func (m *TranscriptModel) bodyHeight() int { return max(m.height-2, 1) }

// rewrap builds the lines the entries are drawn as, and keeps the window on
// them: at the end while the reader follows, and where it was otherwise.
func (m *TranscriptModel) rewrap() {
	if m.wrappedAt != m.width {
		m.lines, m.wrappedAt, m.drawn = m.lines[:0], m.width, 0
	}
	for _, e := range m.entries[m.drawn:] {
		m.lines = append(m.lines, m.entryLines(e)...)
	}
	m.drawn = len(m.entries)
	if m.follow {
		m.offset = m.maxOffset()
		return
	}
	m.offset = min(m.offset, m.maxOffset())
}

// View draws the reader.
func (m *TranscriptModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *TranscriptModel) render() string {
	lines := []string{m.header()}
	lines = append(lines, m.body()...)
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return screen(append(lines[:max(m.height-1, 0)], m.footer()), m.width, m.height)
}

func (m *TranscriptModel) header() string {
	s := m.opts.Styles
	left := []segment{seg(" "+s.Theme.Icons.Claude+" transcript ", s.Title), seg(sanitize.Line(m.opts.Title), s.Text)}
	var right []segment
	if !m.follow && len(m.lines) > 0 {
		right = append(right, seg(strconv.Itoa(m.maxOffset()-m.offset)+" lines below ", s.Muted))
	}
	return s.bar(m.width, left, right)
}

func (m *TranscriptModel) body() []string {
	s := m.opts.Styles
	h := m.bodyHeight()
	switch {
	case !m.have:
		return []string{"", s.Muted.Render(center("reading the transcript", m.width, s.Ellipsis))}
	case len(m.lines) == 0 && m.err != nil:
		return []string{"", " " + s.Danger.Render(truncate(sanitize.Line(m.err.Error()), max(m.width-2, 0), s.Ellipsis))}
	case len(m.lines) == 0:
		return []string{"", s.Idle.Render(center("nothing written yet", m.width, s.Ellipsis))}
	}
	end := min(m.offset+h, len(m.lines))
	out := make([]string, 0, h)
	for _, l := range m.lines[m.offset:end] {
		out = append(out, s.line(m.width, nil, l.segs...))
	}
	return out
}

func (m *TranscriptModel) footer() string {
	s := m.opts.Styles
	var segs []segment
	switch {
	// A reading that failed is reported once: under the entries it could not
	// add to, or in place of them when there are none, never in both.
	case m.err != nil && len(m.lines) > 0:
		segs = append(segs, seg(" "+truncate(sanitize.Line(m.err.Error()), max(m.width-2, 0), s.Ellipsis), s.Danger))
	case len(m.entries) > 0:
		segs = append(segs, seg(" "+strconv.Itoa(len(m.entries))+" entries", s.Text))
		if at := m.entries[len(m.entries)-1].At; !at.IsZero() {
			segs = append(segs, seg("  "+formatAge(m.now().Sub(at))+" ago", s.Muted))
		}
	}
	if m.closed {
		segs = append(segs, seg("  reader stopped", s.Warning))
	}
	keys := []key.Binding{m.nav.Down}
	if !m.follow {
		keys = append(keys, m.nav.Bottom)
	}
	keys = append(keys, m.quitK)
	help := s.helpLine(max(m.width/2, 12), keys...)
	hw := ansi.StringWidth(help)
	if hw >= m.width {
		return help
	}
	return s.line(m.width-hw, &s.Bar, segs...) + help
}

// The markers an entry is drawn with: who wrote it, a tool it called, and what
// the call answered.
type transcriptMarks struct{ user, assistant, call, reply string }

func (m *TranscriptModel) marks() transcriptMarks {
	if m.opts.Styles.Theme.Icons.Name == "ascii" {
		return transcriptMarks{user: ">", assistant: "*", call: "+", reply: "-"}
	}
	return transcriptMarks{user: "›", assistant: "●", call: "▸", reply: "└"}
}

// toolIndent is the room a tool call and its result are drawn in, under the
// answer that called it.
const toolIndent = "  "

// entryLines draws one entry: a prompt and an answer wrapped over as many
// lines as they need, a tool call and its result on one line each, since what
// they are is the tool and the one thing it acted on.
func (m *TranscriptModel) entryLines(e transcript.Entry) []transcriptLine {
	s, mk := m.opts.Styles, m.marks()
	switch e.Who {
	case transcript.User:
		return m.textLines(mk.user, s.Accent2, s.Text, e.Text)
	case transcript.Assistant:
		return m.textLines(mk.assistant, s.Accent, s.Text, e.Text)
	}
	room := max(m.width-len(toolIndent)-2, 4)
	if e.Tool != "" {
		name := truncate(e.Tool, room, s.Ellipsis)
		return []transcriptLine{{segs: []segment{
			seg(toolIndent, s.Muted), seg(mk.call+" ", s.Border), seg(name, s.Key),
			seg(" "+truncate(e.Text, max(room-ansi.StringWidth(name)-1, 0), s.Ellipsis), s.Muted),
		}}}
	}
	style := s.Muted
	if e.Failed {
		style = s.Danger
	}
	return []transcriptLine{{segs: []segment{
		seg(toolIndent, s.Muted), seg(mk.reply+" ", s.Border), seg(truncate(e.Text, room, s.Ellipsis), style),
	}}}
}

// textLines wraps a prompt or an answer: the marker on the first line, the
// rest indented under it.
func (m *TranscriptModel) textLines(marker string, markStyle, textStyle lipgloss.Style, text string) []transcriptLine {
	room := max(m.width-2, 8)
	var out []transcriptLine
	for _, para := range strings.Split(text, "\n") {
		wrapped := ansi.Wrap(para, room, "")
		for _, line := range strings.Split(wrapped, "\n") {
			mark := "  "
			if len(out) == 0 {
				mark = marker + " "
			}
			out = append(out, transcriptLine{segs: []segment{seg(mark, markStyle), seg(line, textStyle)}})
		}
	}
	if len(out) == 0 {
		out = append(out, transcriptLine{segs: []segment{seg(marker+" ", markStyle)}})
	}
	return out
}
