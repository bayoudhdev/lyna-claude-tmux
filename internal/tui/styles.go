// Package tui holds the full-screen terminal interfaces of lyna-tmux: the
// dashboard, the agents picker, the live changes view and the setup wizard.
//
// Models draw only; every read and every side effect goes through an interface
// the caller supplies, so this package never starts a process itself and each
// model is tested by calling Init, Update and View directly.
package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// Theme is the resolved look: a palette, the color depth it is reduced to and
// an icon set.
type Theme struct {
	Palette theme.Palette
	Depth   theme.Depth
	Icons   theme.Icons
}

// ThemeFromConfig resolves the ui settings against the terminal environment:
// "auto" color uses COLORTERM and TERM, "auto" icons use the locale.
func ThemeFromConfig(ui config.UI, getenv func(string) string) (Theme, error) {
	p, err := theme.Get(ui.Theme)
	if err != nil {
		return Theme{}, err
	}
	depth := theme.DetectDepth(getenv)
	if ui.Color != "" && ui.Color != "auto" {
		if depth, err = theme.ParseDepth(ui.Color); err != nil {
			return Theme{}, err
		}
	}
	icons, err := theme.GetIcons(theme.ResolveIcons(ui.Icons, getenv))
	if err != nil {
		return Theme{}, err
	}
	return Theme{Palette: p, Depth: depth, Icons: icons}, nil
}

// Color converts a palette color to a terminal color at the theme's depth.
// Colors are quantized here, not by the renderer, so a 256-color terminal gets
// the same nearest colors the tmux status line uses.
func (t Theme) Color(c theme.Color) color.Color {
	if c.Indexed {
		return ansi.BasicColor(c.ANSI)
	}
	switch t.Depth {
	case theme.DepthTrue:
		return lipgloss.RGBColor{R: c.R, G: c.G, B: c.B}
	case theme.Depth256:
		return ansi.IndexedColor(uint8(theme.To256(c))) //nolint:gosec // To256 returns 16-255
	}
	return ansi.BasicColor(uint8(theme.To16(c))) //nolint:gosec // To16 returns 0-15
}

// Styles are every style the views draw with, all derived from one Theme.
type Styles struct {
	Theme Theme
	// Ellipsis marks truncated text: one cell in every icon set.
	Ellipsis string

	Text    lipgloss.Style
	Muted   lipgloss.Style
	Accent  lipgloss.Style
	Accent2 lipgloss.Style
	Border  lipgloss.Style

	Busy    lipgloss.Style
	Waiting lipgloss.Style
	Idle    lipgloss.Style
	Unknown lipgloss.Style
	Danger  lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style

	// Bar is the header and footer background; Selected is the highlighted
	// row background. Row styles are combined with them per segment.
	Bar      lipgloss.Style
	Selected lipgloss.Style
	Title    lipgloss.Style
	Key      lipgloss.Style
}

// NewStyles derives the styles of a theme.
func NewStyles(t Theme) Styles {
	p := t.Palette
	fg := func(c theme.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Color(c)) }
	s := Styles{
		Theme:    t,
		Ellipsis: "…",
		Text:     fg(p.Text),
		Muted:    fg(p.Muted),
		Accent:   fg(p.Accent),
		Accent2:  fg(p.Accent2),
		Border:   fg(p.Border),
		Busy:     fg(p.Busy),
		Waiting:  fg(p.Waiting).Bold(true),
		Idle:     fg(p.Idle),
		Unknown:  fg(p.Muted),
		Danger:   fg(p.Danger),
		Success:  fg(p.Success),
		Warning:  fg(p.Warning),
		Bar:      lipgloss.NewStyle().Background(t.Color(p.Surface)).Foreground(t.Color(p.Text)),
		Selected: lipgloss.NewStyle().Background(t.Color(p.Overlay)).Foreground(t.Color(p.Text)),
		Title:    fg(p.Accent).Bold(true),
		Key:      fg(p.Accent2).Bold(true),
	}
	if t.Icons.Name == "ascii" {
		s.Ellipsis = "~"
	}
	return s
}

// Status returns the style and icon of an agent status.
func (s Styles) Status(st agent.Status) (lipgloss.Style, string) {
	ic := s.Theme.Icons
	switch st {
	case agent.StatusWaiting:
		return s.Waiting, ic.Waiting
	case agent.StatusIdle:
		return s.Idle, ic.Idle
	case agent.StatusBusy:
		return s.Busy, ic.Busy
	}
	return s.Unknown, ic.Unknown
}

// Swatch renders one block per semantic color of a palette at this theme's
// depth, for the setup wizard's theme preview. Blocks are background-colored
// spaces so they draw in any locale.
func (s Styles) Swatch(p theme.Palette) string {
	colors := []theme.Color{p.Bg, p.Surface, p.Overlay, p.Text, p.Muted, p.Accent, p.Accent2, p.Busy, p.Waiting, p.Idle}
	var b strings.Builder
	for _, c := range colors {
		b.WriteString(lipgloss.NewStyle().Background(s.Theme.Color(c)).Render("  "))
	}
	return b.String()
}
