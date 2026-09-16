package cli

import (
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
)

// splitDirections are the accepted direction arguments, the default first.
var splitDirections = []string{"right", "down"}

// splitCommand splits a workspace pane. It prints nothing on success, so it
// can run from menus and key bindings.
func splitCommand(d Deps) *cobra.Command {
	var (
		req  app.SplitRequest
		role string
	)
	cmd := &cobra.Command{
		Use:   "split [right|down]",
		Short: "Split a workspace pane into a shell, Claude or live changes pane",
		Long: "Split the pane to the right (default) or down and start the role's process in the new pane:\n" +
			"a shell in the pane's directory, Claude with the workspace's per-launch settings, or the live\n" +
			"changes view of the project. Without --pane, the pane this runs in is split.",
		Example:   "  lyna-tmux split\n  lyna-tmux split down --role changes\n  lyna-tmux split right --role claude --pane %3",
		Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
		ValidArgs: splitDirections,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Down = len(args) == 1 && args[0] == "down"
			req.Role = layout.Role(role)
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			res, err := s.SplitPane(ctx, h, req)
			if err != nil {
				return wsTargetErr(err, "--pane <pane_id>")
			}
			wsWarn(cmd, res.Warnings)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Target.Pane, "pane", "", "id of the pane to split (default: the pane this runs in)")
	f.StringVar(&role, "role", string(layout.RoleShell), "what the new pane runs: shell, claude or changes")
	_ = cmd.RegisterFlagCompletionFunc("role", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return app.SplitRoles(), cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}
