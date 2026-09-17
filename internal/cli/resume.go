package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
)

func resumeCommand(d Deps) *cobra.Command {
	var target app.WindowTarget
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Pick a past Claude conversation and continue it in the workspace",
		Long: "Open a window in the current workspace running Claude's conversation picker with the\n" +
			"workspace's per-launch settings. A picker window already open is selected instead of a\n" +
			"second one. Outside a workspace pane, name the workspace with --session.",
		Example: "  lmux resume\n  lmux resume --session api",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			res, err := s.OpenResume(ctx, h, target)
			if err != nil {
				return wsTargetErr(err, "--session <name>")
			}
			wsWarn(cmd, res.Warnings)
			if res.Existing {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Conversation picker is already open in workspace %s\n", res.Session)
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Conversation picker is open in workspace %s\n", res.Session)
			return err
		},
	}
	cmd.Flags().StringVarP(&target.Session, "session", "s", "", "workspace to open the window in (default: the current one)")
	_ = cmd.RegisterFlagCompletionFunc("session", wsSessionChoices(d))
	return cmd
}
