package cli

import (
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
)

// layoutCommand opens a laid out window. It prints nothing on success: the
// window menu runs it through run-shell, which shows any output over the pane.
func layoutCommand(d Deps) *cobra.Command {
	var req app.LayoutRequest
	cmd := &cobra.Command{
		Use:   "layout <name>",
		Short: "Open a new window laid out as a built-in or custom layout",
		Long: "Open a new window in the workspace of the pane, laid out as <name> with the same panes\n" +
			"create starts, and select it. A window of that layout already open is selected as it is,\n" +
			"and other open windows are left alone. Without --pane, the pane this runs in is used.",
		Example: "  lyna-tmux layout trio\n  lyna-tmux layout review --pane %3",
		Args:    cobra.ExactArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return layoutChoices(d), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Layout = args[0]
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			res, err := s.OpenLayout(ctx, h, req)
			if err != nil {
				return wsTargetErr(err, "--pane <pane_id>")
			}
			wsWarn(cmd, res.Warnings)
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Target.Pane, "pane", "", "id of a pane in the workspace (default: the pane this runs in)")
	return cmd
}
