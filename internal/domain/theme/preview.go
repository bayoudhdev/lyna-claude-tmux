package theme

import "strings"

// Span is a run of preview text drawn in one style.
type Span struct {
	Text string
	Fg   Color
	Bg   Color
	// Fill paints Bg behind the text, the way the status line and menus are
	// drawn; a border rule draws over the pane's own background and leaves
	// it unset.
	Fill bool
	Bold bool
}

// Line is one row of the preview.
type Line struct {
	// Kind says what the row mocks: LineStatus, LineBorder or LineMenu.
	Kind string
	// Spans is the row from its left edge.
	Spans []Span
	// Right is the part of the row set against the right edge, as tmux
	// places status-right; empty for rows without one.
	Right []Span
	// Base is the style of the row's empty cells between Spans and Right
	// (tmux status-style); its Text is empty. Only rows with a Right part
	// set it.
	Base Span
}

// Row kinds of a preview.
const (
	LineStatus = "status"
	LineBorder = "border"
	LineMenu   = "menu"
)

// Text is the row without its styling, for output that cannot show colors.
// A right part follows the left one after two spaces.
func (l Line) Text() string {
	var b strings.Builder
	for _, s := range l.Spans {
		b.WriteString(s.Text)
	}
	if len(l.Right) > 0 {
		b.WriteString("  ")
		for _, s := range l.Right {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// Preview mocks the workspace in a palette so a theme can be looked at before
// it is chosen: the status line with a session, three window tabs (one per
// agent state, the busy one current and zoomed), the buttons, a waiting
// agent, the sandbox shield, the branch and the clock; the border of an
// active Claude pane and of an inactive shell pane; a menu row and the
// selected one below it. The separators are the ones the bar would join its
// segments with. The rows draw what the tmux formats of the adapter draw
// (status.go), which are expressions only tmux can expand, so the labels are
// spelled here once more and a test holds them together.
func Preview(p Palette, icons Icons, seps Seps) []Line {
	return []Line{
		previewStatus(p, icons, seps),
		previewBorder(p, icons, seps, true),
		previewBorder(p, icons, seps, false),
		previewMenu(p, icons, false),
		previewMenu(p, icons, true),
	}
}

// previewStatus is the status line: brand and session on the left, the
// window tabs after them, and the buttons, agents, shield, branch and clock
// on the right.
func previewStatus(p Palette, i Icons, seps Seps) Line {
	on := func(bg Color) func(text string, fg Color, bold bool) Span {
		return func(text string, fg Color, bold bool) Span {
			return Span{Text: text, Fg: fg, Bg: bg, Fill: true, Bold: bold}
		}
	}
	bar, raised := on(p.Bg), on(p.Surface)
	// A separator carries the color of the segment it ends over the
	// background of the one it points into; the plain style has none.
	sep := func(glyph string, from, to Color) []Span {
		if glyph == "" {
			return nil
		}
		return []Span{{Text: glyph, Fg: from, Bg: to, Fill: true}}
	}
	right := func(from, to Color) []Span { return sep(seps.Right, from, to) }
	left := func(from, to Color) []Span { return sep(seps.Left, to, from) }
	thin := func(bg Color) []Span { return sep(seps.Thin, p.Border, bg) }
	// The right half of the bar sits on a segment of its own where there are
	// separators to open it with, and on the bar itself where there are not.
	segBg := p.Bg
	if seps.Powerline() {
		segBg = p.Surface
	}
	seg := on(segBg)

	head := []Span{on(p.Accent)(" "+i.Brand+" ", p.Bg, true)}
	head = append(head, right(p.Accent, p.Surface)...)
	head = append(head, raised(" api ", p.Text, false))
	head = append(head, right(p.Surface, p.Bg)...)
	if !seps.Powerline() {
		head = append(head, bar(" ", p.Muted, false))
	}
	// Inactive tabs are muted with a colon in the border color; the current
	// tab is raised, its index in the accent and its name in the text color.
	head = append(head,
		bar(" ", p.Muted, false), bar(i.Idle+" ", p.Idle, false),
		bar("1", p.Muted, false), bar(":", p.Border, false), bar("main ", p.Muted, false))
	head = append(head, right(p.Bg, p.Surface)...)
	head = append(head,
		raised(" ", p.Accent, true), raised(i.Busy+" ", p.Busy, true),
		raised("2", p.Accent, true), raised(":", p.Muted, true), raised("auth", p.Text, true),
		raised(" [z]", p.Warning, true), raised(" ", p.Text, false))
	head = append(head, right(p.Surface, p.Bg)...)
	head = append(head,
		bar(" ", p.Muted, false), bar(i.Waiting+" ", p.Waiting, false),
		bar("3", p.Muted, false), bar(":", p.Border, false), bar("docs ", p.Muted, false))

	tail := []Span{
		bar(" "+i.Split+" split ", p.Muted, false),
		bar(" "+i.Review+" review ", p.Muted, false),
		bar(" "+i.Menu+" ", p.Muted, false),
	}
	tail = append(tail, left(p.Bg, segBg)...)
	tail = append(tail,
		seg(" ", p.Muted, false), seg(i.Waiting+" 1 waiting", p.Waiting, true), seg(" ", p.Muted, false))
	tail = append(tail, thin(segBg)...)
	tail = append(tail, seg(" "+i.Shield+" strict ", p.Success, false))
	tail = append(tail, thin(segBg)...)
	tail = append(tail, seg(" "+i.Branch+" main ", p.Accent2, false))

	clock := " "
	if i.Clock != "" {
		clock += i.Clock + " "
	}
	clockBg, clockFg := p.Surface, p.Text
	if seps.Powerline() {
		clockBg, clockFg = p.Accent, p.Bg
	}
	tail = append(tail, left(segBg, clockBg)...)
	tail = append(tail, on(clockBg)(clock+"12:00 ", clockFg, false))
	return Line{Kind: LineStatus, Spans: head, Right: tail, Base: bar("", p.Text, false)}
}

// previewBorder is a pane border with its role label: the active Claude pane,
// working with subagents, in the accent, wearing its label as a filled block
// where the bar has separators; or an inactive shell pane in the border and
// muted colors.
func previewBorder(p Palette, i Icons, seps Seps, active bool) Line {
	rule := "━"
	if i.Name == "ascii" {
		rule = "-"
	}
	edge := strings.Repeat(rule, 2)
	if !active {
		return Line{Kind: LineBorder, Spans: []Span{
			{Text: edge, Fg: p.Border},
			{Text: " " + i.Shell + " shell ", Fg: p.Muted},
			{Text: strings.Repeat(rule, 20), Fg: p.Border},
		}}
	}
	label := []Span{{Text: " " + i.Claude + " claude", Fg: p.Accent, Bold: true}}
	if seps.Powerline() {
		label = []Span{
			{Text: " " + i.Claude + " claude ", Fg: p.Bg, Bg: p.Accent, Fill: true, Bold: true},
			{Text: seps.Right, Fg: p.Accent},
		}
	}
	spans := append([]Span{{Text: edge, Fg: p.Accent}}, label...)
	return Line{Kind: LineBorder, Spans: append(spans,
		Span{Text: " " + i.Busy + " working", Fg: p.Busy},
		Span{Text: " +2 subagents", Fg: p.Muted},
		Span{Text: " " + strings.Repeat(rule, 4), Fg: p.Accent},
	)}
}

// previewMenu is one entry of the session menu between its borders, either
// at rest on the surface or selected in the accent.
func previewMenu(p Palette, i Icons, selected bool) Line {
	edge := "│"
	if i.Name == "ascii" {
		edge = "|"
	}
	entry := Span{Text: " Agents            M-a ", Fg: p.Text, Bg: p.Surface, Fill: true}
	if selected {
		entry = Span{Text: " Review changes    M-g ", Fg: p.Bg, Bg: p.Accent, Fill: true}
	}
	return Line{Kind: LineMenu, Spans: []Span{
		{Text: edge, Fg: p.Accent}, entry, {Text: edge, Fg: p.Accent},
	}}
}
