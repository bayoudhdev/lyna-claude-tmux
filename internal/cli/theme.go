package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

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

func themeCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme [name]",
		Short: "List the color themes or switch the workspace theme",
		Long: "Without a name, list the themes with a color swatch each and mark the configured one.\n" +
			"With a name, set ui.theme in the configuration file (its comments and layout are kept)\n" +
			"and restyle the running workspaces at once. A missing file is created from the template.",
		Example: "  lyna-tmux theme\n" +
			"  lyna-tmux theme light\n" +
			"  lyna-tmux theme claude",
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			var names []string
			if len(args) == 0 {
				for _, name := range app.ThemeNames() {
					if strings.HasPrefix(name, toComplete) {
						names = append(names, name)
					}
				}
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
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
	cmd.AddCommand(themeClaudeCommand(d))
	return cmd
}

func themeList(cmd *cobra.Command, d Deps, h app.Host) error {
	list, err := app.ThemeList(h)
	if err != nil {
		return err
	}
	color := h.Getenv("NO_COLOR") == "" && d.Terminal != nil && d.Terminal().Interactive
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
	b.WriteString("Switch with: lyna-tmux theme <name>\n")
	b.WriteString("Give Claude Code matching themes with: lyna-tmux theme claude\n")
	_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
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
