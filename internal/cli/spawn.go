package cli

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

func spawnCommand(d Deps) *cobra.Command {
	var req app.SpawnRequest
	cmd := &cobra.Command{
		Use:   "spawn",
		Short: "Start an agent in this workspace, or ask its lead to start one",
		Long: "Ask for an agent: which definition it runs, on which model and effort, in the project\n" +
			"directory or in a git worktree of its own, and what it is being asked to do. The agent\n" +
			"definitions offered are the ones the project carries in .claude/agents and your own.\n\n" +
			"Two targets. Asking the lead types the request into the agent already running here, which\n" +
			"keeps it in the conversation you are having; the exact text is shown before it is sent.\n" +
			"Starting one of our own opens a window of the workspace with a conversation of its own.\n" +
			"Outside a workspace pane, name the workspace with --session.",
		Example: "  lmux spawn\n" +
			"  lmux spawn --session api",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("spawn asks what to start and needs a terminal; lyna-tmux opens it in a popup")
			}
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			view, err := s.OpenSpawn(ctx, h, req)
			if err != nil {
				return wsTargetErr(err, "--session <name>")
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			ui := rootUI{host: h, config: s.Config, term: term}
			return d.spawnForm(cmd, ui, view, tui.NewSpawn(view.Options))
		},
	}
	cmd.Flags().StringVarP(&req.Target.Session, "session", "s", "", "workspace to start the agent in (default: the current one)")
	_ = cmd.RegisterFlagCompletionFunc("session", wsSessionChoices(d))
	return cmd
}

// spawnModel is the form as the command drives it.
type spawnModel interface {
	tea.Model
	Result() (tui.SpawnResult, bool)
}

// spawnForm asks what to start, starts it and says what happened. A form that
// ended without a request starts nothing: the workspace is left as it was.
func (d Deps) spawnForm(cmd *cobra.Command, ui rootUI, view app.SpawnView, m spawnModel) error {
	if err := rootProgram(cmd, ui, m); err != nil {
		return err
	}
	res, done := m.Result()
	if !done || !res.Sent {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Nothing was started")
		return err
	}
	out, err := view.Start(cmd.Context(), res)
	if err != nil {
		return err
	}
	return spawnReport(cmd, res, out)
}

// spawnReport says what was started, which the run that is not in a popup
// reads after the form has closed.
func spawnReport(cmd *cobra.Command, res tui.SpawnResult, out app.SpawnOutcome) error {
	if out.Lead != "" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Asked the agent of workspace %s:\n%s\n", out.Session, res.Message)
		return err
	}
	wsWarn(cmd, out.Window.Warnings)
	if out.Window.Existing {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Window %s of workspace %s is already open\n", res.Request.Name, out.Session)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Agent %s is running in window %s of workspace %s\n",
		res.Request.AgentName(), res.Request.Name, out.Session)
	return err
}
