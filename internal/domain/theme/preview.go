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
// selected one below it. The rows draw what the tmux formats of the adapter
// draw (status.go), which are expressions only tmux can expand, so the
// labels are spelled here once more and a test holds them together.
func Preview(p Palette, icons Icons) []Line {
	return []Line{
		previewStatus(p, icons),
		previewBorder(p, icons, true),
		previewBorder(p, icons, false),
		previewMenu(p, icons, false),
		previewMenu(p, icons, true),
	}
}

// previewStatus is the status line: brand and session on the left, the
// window tabs after them, and the buttons, agents, shield, branch and clock
// on the right.
func previewStatus(p Palette, i Icons) Line {
	on := func(bg Color) func(text string, fg Color, bold bool) Span {
		return func(text string, fg Color, bold bool) Span {
			return Span{Text: text, Fg: fg, Bg: bg, Fill: true, Bold: bold}
		}
	}
	bar, raised := on(p.Bg), on(p.Surface)

	left := []Span{
		on(p.Accent)(" "+i.Brand+" ", p.Bg, true),
		raised(" api ", p.Text, false),
		bar(" ", p.Muted, false),
	}
	// Inactive tabs are muted with a colon in the border color; the current
	// tab is raised, its index in the accent and its name in the text color.
	left = append(left,
		bar(" ", p.Muted, false), bar(i.Idle+" ", p.Idle, false),
		bar("1", p.Muted, false), bar(":", p.Border, false), bar("main ", p.Muted, false),
		raised(" ", p.Accent, true), raised(i.Busy+" ", p.Busy, true),
		raised("2", p.Accent, true), raised(":", p.Muted, true), raised("auth", p.Text, true),
		raised(" [z]", p.Warning, true), raised(" ", p.Text, false),
		bar(" ", p.Muted, false), bar(i.Waiting+" ", p.Waiting, false),
		bar("3", p.Muted, false), bar(":", p.Border, false), bar("docs ", p.Muted, false),
	)

	right := []Span{
		bar(" "+i.Split+" split ", p.Muted, false),
		bar(" "+i.Review+" review ", p.Muted, false),
		bar(" "+i.Menu+" ", p.Muted, false),
		bar(" ", p.Muted, false), bar(i.Waiting+" 1 waiting", p.Waiting, true), bar(" ", p.Muted, false),
		bar(" "+i.Shield+" strict ", p.Success, false),
		bar(" "+i.Branch+" main ", p.Accent2, false),
	}
	clock := " "
	if i.Clock != "" {
		clock += i.Clock + " "
	}
	right = append(right, raised(clock+"12:00 ", p.Text, false))
	return Line{Kind: LineStatus, Spans: left, Right: right, Base: bar("", p.Text, false)}
}

// previewBorder is a pane border with its role label: the active Claude pane,
// working with subagents, in the accent; or an inactive shell pane in the
// border and muted colors.
func previewBorder(p Palette, i Icons, active bool) Line {
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
	return Line{Kind: LineBorder, Spans: []Span{
		{Text: edge, Fg: p.Accent},
		{Text: " " + i.Claude + " claude", Fg: p.Accent, Bold: true},
		{Text: " " + i.Busy + " working", Fg: p.Busy},
		{Text: " +2 subagents", Fg: p.Muted},
		{Text: " " + strings.Repeat(rule, 4), Fg: p.Accent},
	}}
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
