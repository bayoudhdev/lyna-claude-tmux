package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
)

func taskCommand(d Deps) *cobra.Command {
	var req app.TaskRequest
	cmd := &cobra.Command{
		Use:   "task <name> [prompt]",
		Short: "Open a window running Claude in its own git worktree",
		Long: "Open a window named <name> in the current workspace running Claude in the git worktree\n" +
			"<name>, with the workspace's per-launch settings. An optional prompt starts the conversation.\n" +
			"A task window of that name already open is selected instead, so one worktree keeps one\n" +
			"conversation. Worktree names use letters, digits, '.', '_' and '-'. Outside a workspace pane,\n" +
			"name the workspace with --session.",
		Example: "  lyna-tmux task fix-login \"fix the login redirect loop\"\n" +
			"  lyna-tmux task spike --session api",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Name = args[0]
			if len(args) == 2 {
				req.Prompt = args[1]
			}
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			res, err := s.OpenTask(ctx, h, req)
			if err != nil {
				return wsTargetErr(err, "--session <name>")
			}
			wsWarn(cmd, res.Warnings)
			if res.Existing {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Task %s is already open in workspace %s\n", req.Name, res.Session)
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Task %s is running in workspace %s\n", req.Name, res.Session)
			return err
		},
	}
	cmd.Flags().StringVarP(&req.Target.Session, "session", "s", "", "workspace to open the window in (default: the current one)")
	_ = cmd.RegisterFlagCompletionFunc("session", wsSessionChoices(d))
	return cmd
}
