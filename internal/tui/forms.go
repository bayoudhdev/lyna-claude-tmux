package tui

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// huhTheme styles forms with the workspace palette, so the wizard and the
// create form look like the rest of the interface.
func (s Styles) huhTheme() huh.Theme {
	return huh.ThemeFunc(func(bool) *huh.Styles {
		p, c := s.Theme.Palette, s.Theme.Color
		t := huh.ThemeBase(p.Dark)
		ascii := s.Theme.Icons.Name == "ascii"

		f := &t.Focused
		f.Base = f.Base.BorderForeground(c(p.Accent))
		if ascii {
			f.Base = f.Base.BorderStyle(lipgloss.Border{Left: "|"})
		}
		f.Card = f.Base
		f.Title = lipgloss.NewStyle().Foreground(c(p.Accent)).Bold(true)
		f.NoteTitle = f.Title
		f.Description = lipgloss.NewStyle().Foreground(c(p.Muted))
		f.ErrorIndicator = lipgloss.NewStyle().Foreground(c(p.Danger)).SetString(" *")
		f.ErrorMessage = lipgloss.NewStyle().Foreground(c(p.Danger)).SetString(" *")
		f.SelectSelector = lipgloss.NewStyle().Foreground(c(p.Accent)).SetString("> ")
		next, prev := "→", "←"
		if ascii {
			next, prev = ">", "<"
		}
		f.NextIndicator = lipgloss.NewStyle().MarginLeft(1).Foreground(c(p.Accent)).SetString(next)
		f.PrevIndicator = lipgloss.NewStyle().MarginRight(1).Foreground(c(p.Accent)).SetString(prev)
		f.Option = lipgloss.NewStyle().Foreground(c(p.Text))
		f.SelectedOption = lipgloss.NewStyle().Foreground(c(p.Accent))
		f.UnselectedOption = f.Option
		button := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
		f.FocusedButton = button.Foreground(c(p.Bg)).Background(c(p.Accent)).Bold(true)
		f.BlurredButton = button.Foreground(c(p.Muted)).Background(c(p.Surface))
		f.TextInput.Cursor = lipgloss.NewStyle().Foreground(c(p.Accent))
		f.TextInput.Placeholder = lipgloss.NewStyle().Foreground(c(p.Muted))
		f.TextInput.Prompt = lipgloss.NewStyle().Foreground(c(p.Accent2))
		f.TextInput.Text = lipgloss.NewStyle().Foreground(c(p.Text))

		t.Blurred = t.Focused
		b := &t.Blurred
		b.Base = b.Base.BorderStyle(lipgloss.HiddenBorder())
		b.Card = b.Base
		b.Title = lipgloss.NewStyle().Foreground(c(p.Muted))
		b.SelectSelector = lipgloss.NewStyle().SetString("  ")
		b.NextIndicator = lipgloss.NewStyle()
		b.PrevIndicator = lipgloss.NewStyle()

		t.Group.Title = lipgloss.NewStyle().Foreground(c(p.Accent)).Bold(true)
		t.Group.Description = lipgloss.NewStyle().Foreground(c(p.Muted))
		t.Help.ShortKey = s.Key
		t.Help.ShortDesc = s.Muted
		t.Help.ShortSeparator = s.Muted
		return t
	})
}
