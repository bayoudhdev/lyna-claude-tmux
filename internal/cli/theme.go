package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/statusline"
)

// themeSwatch names the palette colors a swatch shows, in order: the accents
// and agent states that set a theme apart, then its text colors.
var themeSwatch = []func(theme.Palette) theme.Color{
	func(p theme.Palette) theme.Color { return p.Accent },
	func(p theme.Palette) theme.Color { return p.Accent2 },
	func(p theme.Palette) theme.Color { return p.Busy },
	func(p theme.Palette) theme.Color { return p.Waiting },
	func(p theme.Palette) theme.Color { return p.Text },
	func(p theme.Palette) theme.Color { return p.Muted },
}

// themeSlots names every palette color, in the order the plain preview
// spells them.
var themeSlots = []struct {
	name string
	pick func(theme.Palette) theme.Color
}{
	{"bg", func(p theme.Palette) theme.Color { return p.Bg }},
	{"surface", func(p theme.Palette) theme.Color { return p.Surface }},
	{"overlay", func(p theme.Palette) theme.Color { return p.Overlay }},
	{"border", func(p theme.Palette) theme.Color { return p.Border }},
	{"muted", func(p theme.Palette) theme.Color { return p.Muted }},
	{"text", func(p theme.Palette) theme.Color { return p.Text }},
	{"accent", func(p theme.Palette) theme.Color { return p.Accent }},
	{"accent2", func(p theme.Palette) theme.Color { return p.Accent2 }},
	{"busy", func(p theme.Palette) theme.Color { return p.Busy }},
	{"waiting", func(p theme.Palette) theme.Color { return p.Waiting }},
	{"idle", func(p theme.Palette) theme.Color { return p.Idle }},
	{"danger", func(p theme.Palette) theme.Color { return p.Danger }},
	{"success", func(p theme.Palette) theme.Color { return p.Success }},
	{"warning", func(p theme.Palette) theme.Color { return p.Warning }},
}

func themeCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme [name]",
		Short: "List the color themes or switch the workspace theme",
		Long: "Without a name, list the themes with a color swatch each and mark the configured one.\n" +
			"With a name, set ui.theme in the configuration file (its comments and layout are kept)\n" +
			"and restyle the running workspaces at once. A missing file is created from the template.\n" +
			"See a theme before switching with: lyna-tmux theme preview [name]",
		Example: "  lyna-tmux theme\n" +
			"  lyna-tmux theme preview nord\n" +
			"  lyna-tmux theme light\n" +
			"  lyna-tmux theme claude",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: themeNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return themeList(cmd, d, h)
			}
			return themeSet(cmd, h, args[0])
		},
	}
	cmd.AddCommand(themePreviewCommand(d), themeClaudeCommand(d))
	return cmd
}

// themeNameCompletion completes the first argument with the theme names.
func themeNameCompletion(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var names []string
	if len(args) == 0 {
		for _, name := range app.ThemeNames() {
			if strings.HasPrefix(name, toComplete) {
				names = append(names, name)
			}
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func themeList(cmd *cobra.Command, d Deps, h app.Host) error {
	list, err := app.ThemeList(h)
	if err != nil {
		return err
	}
	color := themeColor(d, h)
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  THEME\tSTYLE\tCOLORS (%s)\n", list.Depth)
	for _, p := range list.Palettes {
		mark := " "
		if p.Name == list.Current {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\n", mark, p.Name, themeStyle(p), themeSwatchLine(p, list.Depth, color))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(&b, "\n* is the configured theme (ui.theme in %s).\n", sanitize.Line(list.ConfigPath))
	b.WriteString("Preview one with: lyna-tmux theme preview <name>\n")
	b.WriteString("Switch with: lyna-tmux theme <name>\n")
	b.WriteString("Give Claude Code matching themes with: lyna-tmux theme claude\n")
	_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

// themeColor reports whether output may carry colors: only on a terminal,
// and never when NO_COLOR asks for none.
func themeColor(d Deps, h app.Host) bool {
	return h.Getenv("NO_COLOR") == "" && d.Terminal != nil && d.Terminal().Interactive
}

// themeStyle describes the background a palette is designed for.
func themeStyle(p theme.Palette) string {
	switch {
	case p.Bg.Indexed:
		return "terminal colors"
	case p.Dark:
		return "dark"
	}
	return "light"
}

// themeSwatchLine draws the swatch colors as blocks at depth, or spells them
// the way tmux receives them when color output is off.
func themeSwatchLine(p theme.Palette, depth theme.Depth, color bool) string {
	parts := make([]string, len(themeSwatch))
	for i, pick := range themeSwatch {
		c := pick(p)
		if color {
			parts[i] = "\x1b[" + statusline.Foreground(c, depth) + "m██\x1b[0m"
		} else {
			parts[i] = c.Tmux(depth)
		}
	}
	return strings.Join(parts, " ")
}

func themePreviewCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "preview [name]",
		Short: "Show what the workspace looks like in a theme",
		Long: "Draw the status line, pane borders and a menu in the named theme, or in every theme\n" +
			"without a name, at the color depth and with the icons the workspace would use. On a\n" +
			"terminal the preview is drawn in color; elsewhere, or with NO_COLOR set, it names the\n" +
			"colors instead. The configuration is not changed.",
		Example: "  lyna-tmux theme preview\n" +
			"  lyna-tmux theme preview nord",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: themeNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return themePreview(cmd, d, h, name)
		},
	}
}

// previewIndent and previewLabel lay out a preview row: two spaces, the row
// kind padded to one column, the row.
const (
	previewIndent = "  "
	previewLabel  = 8
)

func themePreview(cmd *cobra.Command, d Deps, h app.Host, name string) error {
	mockup, err := app.ThemePreview(h, name)
	if err != nil {
		return err
	}
	color := themeColor(d, h)
	// The status line spans the terminal, its right part against the right
	// edge; without a terminal there is no edge to reach.
	width := 0
	if color {
		width = d.Terminal().Width - len(previewIndent) - previewLabel
	}
	var b strings.Builder
	for i, m := range mockup.Themes {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s: %s", m.Palette.Name, themeStyle(m.Palette))
		if m.Palette.Name == mockup.Current {
			b.WriteString(" (configured)")
		}
		b.WriteByte('\n')
		for _, l := range m.Lines {
			row := l.Text()
			if color {
				row = themePaintLine(l, mockup.Depth, width)
			}
			fmt.Fprintf(&b, "%s%-*s%s\n", previewIndent, previewLabel, l.Kind, row)
		}
		fmt.Fprintf(&b, "%s%-*s%s\n", previewIndent, previewLabel, "swatch", themeSwatchLine(m.Palette, mockup.Depth, color))
		if !color {
			fmt.Fprintf(&b, "%s%-*s%s\n", previewIndent, previewLabel, "colors", themeSlotsLine(m.Palette, mockup.Depth))
		}
	}
	if name == "" {
		b.WriteString("\nSwitch with: lyna-tmux theme <name>\n")
	} else {
		fmt.Fprintf(&b, "\nSwitch with: lyna-tmux theme %s\n", name)
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

// themeSlotsLine spells every palette color by name, the way tmux receives
// it at depth, for output that cannot show the colors themselves.
func themeSlotsLine(p theme.Palette, depth theme.Depth) string {
	parts := make([]string, len(themeSlots))
	for i, slot := range themeSlots {
		parts[i] = slot.name + "=" + slot.pick(p).Tmux(depth)
	}
	return strings.Join(parts, " ")
}

// themePaintLine draws a preview row with SGR colors at depth. When the row
// has a right part and the width is known, the gap between the two parts is
// filled in the row's base style so the bar reaches the edge as a status
// line does; a row wider than the terminal keeps a two-space gap.
func themePaintLine(l theme.Line, depth theme.Depth, width int) string {
	var b strings.Builder
	cells := 0
	for _, s := range l.Spans {
		b.WriteString(themePaintSpan(s, depth))
		cells += ansi.StringWidth(s.Text)
	}
	if len(l.Right) == 0 {
		return b.String()
	}
	for _, s := range l.Right {
		cells += ansi.StringWidth(s.Text)
	}
	gap := l.Base
	gap.Text = "  "
	if width-cells > 2 {
		gap.Text = strings.Repeat(" ", width-cells)
	}
	b.WriteString(themePaintSpan(gap, depth))
	for _, s := range l.Right {
		b.WriteString(themePaintSpan(s, depth))
	}
	return b.String()
}

// themePaintSpan wraps one span in its SGR attributes and a reset.
func themePaintSpan(s theme.Span, depth theme.Depth) string {
	params := make([]string, 0, 3)
	if s.Bold {
		params = append(params, "1")
	}
	params = append(params, statusline.Foreground(s.Fg, depth))
	if s.Fill {
		params = append(params, themeBackground(s.Bg, depth))
	}
	return "\x1b[" + strings.Join(params, ";") + "m" + s.Text + "\x1b[0m"
}

// themeBackground returns the SGR parameters selecting c as the background
// at depth: the foreground selection with its leading 38 (extended color)
// turned into 48, or a basic code moved from the 30s and 90s into the 40s
// and 100s.
func themeBackground(c theme.Color, depth theme.Depth) string {
	fg := statusline.Foreground(c, depth)
	if rest, ok := strings.CutPrefix(fg, "38;"); ok {
		return "48;" + rest
	}
	n, err := strconv.Atoi(fg)
	if err != nil {
		// Foreground only ever returns an extended selection or a basic
		// code; anything else would be a change to it this must follow.
		panic("theme: unexpected foreground selection " + fg)
	}
	return strconv.Itoa(n + 10)
}

func themeSet(cmd *cobra.Command, h app.Host, name string) error {
	change, err := app.ThemeSet(h, name)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	path := sanitize.Line(change.Path)
	if change.Created {
		fmt.Fprintf(out, "Created %s from the template\n", path)
	}
	if change.Changed || change.Created {
		fmt.Fprintf(out, "Theme set to %s in %s\n", name, path)
	} else {
		fmt.Fprintf(out, "Theme is already %s in %s\n", name, path)
	}
	running, err := app.ThemeApply(cmd.Context(), h)
	if err != nil {
		return fmt.Errorf("the theme is saved, but restyling the running workspaces failed: %w", err)
	}
	if running {
		_, err = fmt.Fprintln(out, "Restyled the running workspaces. Claude status lines already open change when Claude restarts.")
	}
	return err
}

func themeClaudeCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "claude",
		Short: "Write Claude Code color themes that match the workspace themes",
		Long: "Write one Claude Code theme per workspace theme into the themes directory of your\n" +
			"Claude Code configuration ($CLAUDE_CONFIG_DIR or ~/.claude). Only lyna-*.json files\n" +
			"there are written, and a theme file you created or edited is never replaced.",
		Example: "  lyna-tmux theme claude",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			rep, err := app.ThemeClaude(h)
			if rep.Dir == "" {
				return themeClaudeNothing(err)
			}
			return themeClaudeReport(cmd, rep, err)
		},
	}
}

// themeClaudeNothing reports a run that produced no themes directory, and so
// has nothing to print. Every such run carries the reason it stopped; without
// one the command would claim success in silence, so it says what happened.
func themeClaudeNothing(err error) error {
	if err == nil {
		return errors.New("no Claude Code themes were written")
	}
	return err
}

func themeClaudeReport(cmd *cobra.Command, rep claudetheme.Report, writeErr error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Claude Code themes in %s\n", sanitize.Line(rep.Dir))
	var names []string
	skipped := 0
	for _, f := range rep.Files {
		path := f.Path
		if path == "" {
			path = f.Palette
		}
		fmt.Fprintf(&b, "  %-9s  %s", f.Outcome, sanitize.Line(path))
		switch f.Outcome {
		case claudetheme.Refused:
			skipped++
			b.WriteString(": not written by lyna-tmux or edited since, so it is kept (delete it and run this again to regenerate it)")
		case claudetheme.Failed:
			skipped++
			b.WriteString(": " + sanitize.Line(f.Err.Error()))
		default:
			names = append(names, claudetheme.DisplayName(f.Palette))
		}
		b.WriteByte('\n')
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "Pick one in Claude Code with /theme: %s.\n", strings.Join(names, ", "))
	}
	if rep.CreatedDir {
		b.WriteString("Restart Claude Code sessions that are already running to see the new themes.\n")
	}
	if _, err := fmt.Fprint(cmd.OutOrStdout(), b.String()); err != nil {
		return err
	}
	if skipped > 0 {
		return fmt.Errorf("%d of %d theme files were not written", skipped, len(rep.Files))
	}
	return writeErr
}
