package tui

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Frame size used until the first WindowSizeMsg when the caller gives none.
const (
	defaultWidth  = 80
	defaultHeight = 24
	// doubleClick is the longest gap between two clicks on one row that
	// counts as a double click.
	doubleClick = 400 * time.Millisecond
)

// segment is styled text inside a line.
type segment struct {
	text  string
	style lipgloss.Style
}

func seg(text string, style lipgloss.Style) segment { return segment{text: text, style: style} }

// line renders segments into exactly width cells. Text past the width is
// truncated with the ellipsis; the rest is padded. A non-nil bg is combined
// with every segment so inner resets never break the row's background.
func (s Styles) line(width int, bg *lipgloss.Style, segs ...segment) string {
	var b strings.Builder
	used := 0
	for _, sg := range segs {
		if used >= width {
			break
		}
		text := sg.text
		if w := ansi.StringWidth(text); used+w > width {
			text = ansi.Truncate(text, width-used, s.Ellipsis)
		}
		style := sg.style
		if bg != nil {
			style = style.Background(bg.GetBackground())
		}
		b.WriteString(style.Render(text))
		used += ansi.StringWidth(text)
	}
	if used < width {
		pad := strings.Repeat(" ", width-used)
		if bg != nil {
			pad = bg.Render(pad)
		}
		b.WriteString(pad)
	}
	return b.String()
}

// bar renders a header line: left segments, right segments flush right when
// they fit, on the bar background.
func (s Styles) bar(width int, left, right []segment) string {
	lw, rw := segsWidth(left), segsWidth(right)
	segs := left
	if lw+rw+1 <= width && rw > 0 {
		segs = append(append(segs[:len(segs):len(segs)], seg(strings.Repeat(" ", width-lw-rw), s.Bar)), right...)
	}
	return s.line(width, &s.Bar, segs...)
}

// segsWidth is the width of segments in cells.
func segsWidth(segs []segment) int {
	w := 0
	for _, sg := range segs {
		w += ansi.StringWidth(sg.text)
	}
	return w
}

// rule renders a divider with an optional title.
func (s Styles) rule(width int, title string) string {
	h := "─"
	if s.Theme.Icons.Name == "ascii" {
		h = "-"
	}
	if title == "" {
		return s.Border.Render(strings.Repeat(h, max(width, 0)))
	}
	head := h + " "
	title = truncate(title, max(width-4, 0), s.Ellipsis)
	rest := max(width-ansi.StringWidth(head)-ansi.StringWidth(title)-1, 0)
	return s.Border.Render(head) + s.Muted.Render(title) + " " + s.Border.Render(strings.Repeat(h, rest))
}

// helpLine renders key help that fits in width.
func (s Styles) helpLine(width int, bindings ...key.Binding) string {
	h := help.New()
	h.ShortSeparator = "  "
	h.Ellipsis = s.Ellipsis
	h.Styles.ShortKey = s.Key
	h.Styles.ShortDesc = s.Muted
	h.Styles.ShortSeparator = s.Muted
	h.Styles.Ellipsis = s.Muted
	h.SetWidth(width)
	return " " + h.ShortHelpView(bindings)
}

// screen joins lines into exactly height rows, each clipped to width.
func screen(lines []string, width, height int) string {
	if height <= 0 {
		return ""
	}
	out := make([]string, height)
	for i := range height {
		if i < len(lines) {
			l := lines[i]
			if ansi.StringWidth(l) > width {
				l = ansi.Truncate(l, width, "")
			}
			out[i] = l
		}
	}
	return strings.Join(out, "\n")
}

// truncate shortens plain text to w cells.
func truncate(s string, w int, ellipsis string) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, ellipsis)
}

// fit truncates or pads plain text to exactly w cells.
func fit(s string, w int, ellipsis string) string {
	s = truncate(s, w, ellipsis)
	if n := ansi.StringWidth(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// fitRight is fit aligned to the right.
func fitRight(s string, w int, ellipsis string) string {
	s = truncate(s, w, ellipsis)
	if n := ansi.StringWidth(s); n < w {
		s = strings.Repeat(" ", w-n) + s
	}
	return s
}

// center pads plain text to be centered in w cells.
func center(s string, w int, ellipsis string) string {
	s = truncate(s, w, ellipsis)
	left := max((w-ansi.StringWidth(s))/2, 0)
	return strings.Repeat(" ", left) + s
}

// formatAge renders a duration compactly: 42s, 7m, 3h, 12d.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(max(d, 0)/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

// shortPath replaces the home directory prefix with "~".
func shortPath(path, home string) string {
	if home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, strings.TrimSuffix(home, "/")+"/"); ok {
		return "~/" + rest
	}
	return path
}

// listView tracks the cursor and scroll offset of a list of n rows drawn in
// height rows.
type listView struct {
	cursor, offset int
}

func (l *listView) clamp(n, height int) {
	if n <= 0 {
		l.cursor, l.offset = 0, 0
		return
	}
	l.cursor = min(max(l.cursor, 0), n-1)
	height = max(height, 1)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+height {
		l.offset = l.cursor - height + 1
	}
	l.offset = min(max(l.offset, 0), max(n-height, 0))
}

func (l *listView) move(delta, n, height int) {
	l.cursor += delta
	l.clamp(n, height)
}

// navKeys are the list movement keys shared by the list views.
type navKeys struct {
	Up, Down, PageUp, PageDown, Top, Bottom key.Binding
}

func newNavKeys() navKeys {
	return navKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k", "ctrl+p"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j", "ctrl+n"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		Top:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "top")),
		Bottom:   key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "bottom")),
	}
}

// delta returns the cursor movement for a navigation key, and false when the
// key is not a navigation key.
func (k navKeys) delta(msg tea.KeyPressMsg, n, page int) (int, bool) {
	page = max(page, 1)
	switch {
	case key.Matches(msg, k.Up):
		return -1, true
	case key.Matches(msg, k.Down):
		return 1, true
	case key.Matches(msg, k.PageUp):
		return -page, true
	case key.Matches(msg, k.PageDown):
		return page, true
	case key.Matches(msg, k.Top):
		return -n, true
	case key.Matches(msg, k.Bottom):
		return n, true
	}
	return 0, false
}

// clicks detects double clicks on the same row.
type clicks struct {
	at  time.Time
	row int
}

func (c *clicks) click(now time.Time, row int) bool {
	double := !c.at.IsZero() && c.row == row && now.Sub(c.at) <= doubleClick
	if double {
		c.at = time.Time{}
	} else {
		c.at, c.row = now, row
	}
	return double
}

// lineInput is a single-line text field for filters: printable input,
// backspace, ctrl+w (word) and ctrl+u (all). It never blinks, so it needs no
// timer commands.
type lineInput struct {
	value string
	limit int
}

func (in *lineInput) update(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "backspace", "ctrl+h":
		if in.value != "" {
			_, size := utf8.DecodeLastRuneInString(in.value)
			in.value = in.value[:len(in.value)-size]
		}
		return true
	case "ctrl+u":
		in.value = ""
		return true
	case "ctrl+w":
		trimmed := strings.TrimRight(in.value, " ")
		if i := strings.LastIndexByte(trimmed, ' '); i >= 0 {
			in.value = trimmed[:i+1]
		} else {
			in.value = ""
		}
		return true
	}
	if msg.Text == "" || msg.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
		return false
	}
	in.insert(msg.Text)
	return true
}

func (in *lineInput) insert(text string) {
	for _, r := range text {
		if r < 0x20 || r == 0x7f || (in.limit > 0 && utf8.RuneCountInString(in.value) >= in.limit) {
			continue
		}
		in.value += string(r)
	}
}

// ctxOr returns ctx, or a background context when nil.
func ctxOr(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// nowOr returns now, or time.Now when nil.
func nowOr(now func() time.Time) func() time.Time {
	if now == nil {
		return time.Now
	}
	return now
}

// sizeOr returns w and h, or the defaults when not positive.
func sizeOr(w, h int) (width, height int) {
	if w <= 0 {
		w = defaultWidth
	}
	if h <= 0 {
		h = defaultHeight
	}
	return w, h
}
