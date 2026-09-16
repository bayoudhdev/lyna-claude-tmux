package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// errUninstallCanceled reports a confirmation the user declined.
var errUninstallCanceled = errors.New("canceled: nothing was removed")

func uninstallCommand(d Deps) *cobra.Command {
	var yes, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the lyna-tmux server and remove everything lyna-tmux wrote",
		Long: "Stop the lyna-tmux tmux server and remove its state, cache and data directories: the\n" +
			"generated tmux configuration, the per-launch Claude settings, the agent cache and the\n" +
			"review editor installation. With --purge the configuration directory goes too. Claude\n" +
			"Code theme files are removed only when they are still exactly the ones lyna-tmux wrote.\n" +
			"Your own tmux, Claude Code and Neovim settings, and every other tmux server, are left\n" +
			"alone. What is left to do by hand is printed at the end.",
		Example: "  lyna-tmux uninstall\n" +
			"  lyna-tmux uninstall --purge --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			p, err := app.UninstallPlanFor(h, purge)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "This stops the lyna-tmux server on socket %s and removes:\n", sanitize.Line(p.SocketName))
			if len(p.Dirs) == 0 && len(p.Themes) == 0 {
				fmt.Fprintln(out, "  nothing: lyna-tmux has written no files here")
			}
			for _, path := range p.Dirs {
				fmt.Fprintf(out, "  %s\n", sanitize.Line(path))
			}
			for _, path := range p.Themes {
				fmt.Fprintf(out, "  %s (a Claude Code theme lyna-tmux generated)\n", sanitize.Line(path))
			}
			if p.KeptConfig != "" {
				fmt.Fprintf(out, "Kept: %s (pass --purge to remove it too)\n", sanitize.Line(p.KeptConfig))
			}
			if !yes {
				ok, err := d.infraConfirm(cmd, "Remove them?", "uninstall removes files; pass --yes to confirm without a terminal")
				if err != nil {
					return err
				}
				if !ok {
					return errUninstallCanceled
				}
			}
			res, err := p.Apply(cmd.Context(), h)
			for _, path := range res.Removed {
				fmt.Fprintf(out, "Removed %s\n", sanitize.Line(path))
			}
			if err != nil {
				return err
			}
			if res.ServerStopped {
				fmt.Fprintln(out, "Stopped the lyna-tmux server")
			}
			return uninstallWriteManual(cmd, p)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&yes, "yes", false, "remove without asking")
	f.BoolVar(&purge, "purge", false, "remove the configuration directory too")
	return cmd
}

// uninstallWriteManual prints what lyna-tmux will not do for the user: remove
// its own binary, the shell completions they installed, the Claude Code
// plugin and the tmux plugin manager entry.
func uninstallWriteManual(cmd *cobra.Command, p app.UninstallPlan) error {
	binary := p.Binary
	if binary == "" {
		binary = "the lyna-tmux binary"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "\nLeft to do by hand:\n"+
		"  remove the binary: %s\n"+
		"  remove the shell completions you installed from `lyna-tmux completion`\n"+
		"  in Claude Code, run: %s\n"+
		"  remove this line from your tmux configuration if you added it: %s\n",
		sanitize.Line(binary), app.UninstallClaudePlugin, app.UninstallTPMLine)
	return err
}
