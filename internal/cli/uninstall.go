package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

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
			"Code theme files and shell completion scripts are removed only when they are still\n" +
			"exactly the ones lyna-tmux wrote, and the binary only when it is yours to remove: not\n" +
			"a package manager's, not in a directory you cannot write. Your own tmux, Claude Code\n" +
			"and Neovim settings, and every other tmux server, are left alone. What is left to do\n" +
			"by hand is printed at the end.",
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
			if p.Empty() {
				// No question: an answer that changes nothing is not worth
				// asking for, and a script running this twice must not stop
				// on the second run.
				fmt.Fprintln(out, "Nothing to remove: lyna-tmux has written no files here and its server is not running.")
				uninstallWriteKept(out, p)
				return uninstallWriteManual(cmd, p)
			}
			if p.ServerSocket != "" {
				fmt.Fprintf(out, "This stops the lyna-tmux server on socket %s and removes:\n", sanitize.Line(p.SocketName))
			} else {
				fmt.Fprintln(out, "This removes:")
			}
			for _, path := range p.Dirs {
				fmt.Fprintf(out, "  %s\n", sanitize.Line(path))
			}
			for _, path := range p.Themes {
				fmt.Fprintf(out, "  %s (a Claude Code theme lyna-tmux generated)\n", sanitize.Line(path))
			}
			for _, path := range p.Completions {
				fmt.Fprintf(out, "  %s (a shell completion lyna-tmux generated)\n", sanitize.Line(path))
			}
			if p.Binary != "" {
				fmt.Fprintf(out, "  %s (the lyna-tmux binary)\n", sanitize.Line(p.Binary))
			}
			uninstallWriteKept(out, p)
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

// uninstallWriteKept names the configuration directory a run without --purge
// leaves in place.
func uninstallWriteKept(out io.Writer, p app.UninstallPlan) {
	if p.KeptConfig != "" {
		fmt.Fprintf(out, "Kept: %s (pass --purge to remove it too)\n", sanitize.Line(p.KeptConfig))
	}
}

// uninstallWriteManual prints what uninstall leaves to the user, each step
// with the way to do it, or that nothing is left.
func uninstallWriteManual(cmd *cobra.Command, p app.UninstallPlan) error {
	out := cmd.OutOrStdout()
	if len(p.Manual) == 0 {
		_, err := fmt.Fprintln(out, "\nNothing is left to do by hand.")
		return err
	}
	var b strings.Builder
	b.WriteString("\nLeft to do by hand:\n")
	for _, m := range p.Manual {
		fmt.Fprintf(&b, "  %s\n    %s\n", sanitize.Line(m.Step), sanitize.Line(m.How))
	}
	_, err := io.WriteString(out, b.String())
	return err
}
