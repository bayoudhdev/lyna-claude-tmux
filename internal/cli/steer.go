package cli

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// steerCommands returns the commands that steer an agent of the workspace: message and stop.
func steerCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{messageCommand(d), stopCommand(d)}
}

func messageCommand(d Deps) *cobra.Command {
	var req app.SteerRequest
	cmd := &cobra.Command{
		Use:   "message",
		Short: "Type a message into an agent of this workspace",
		Long: "Type one line into the pane of an agent of the workspace, its lead or one of its\n" +
			"teammates, and submit it, as if you had typed it there yourself. The exact text is shown\n" +
			"before it is sent. A pane that runs no agent is refused: a shell would run the line as a\n" +
			"command. An agent waiting on you is left alone, since the Enter that submits the message\n" +
			"would answer its question. Name the agent by its pane id (the rail's m key does).\n" +
			"Outside a workspace pane, name the workspace with --session.",
		Example: "  lmux message --to %3\n" +
			"  lmux message --session api --to %3",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("message asks what to send and needs a terminal; lyna-tmux opens it in a popup")
			}
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			view, err := s.OpenMessage(ctx, h, req)
			if err != nil {
				return wsTargetErr(err, "--session <name>")
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			ui := rootUI{host: h, config: s.Config, term: term}
			return d.messageForm(cmd, ui, view, tui.NewMessage(view.Options))
		},
	}
	steerFlags(cmd, d, &req, "workspace of the agent (default: the current one)", "pane id of the agent to message, such as %3")
	return cmd
}

func stopCommand(d Deps) *cobra.Command {
	var req app.SteerRequest
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a teammate of this workspace",
		Long: "Stop a teammate, one of two ways. Asking the lead types a request to shut it down into\n" +
			"the lead's pane, which keeps the team in order; the exact text is shown before it is\n" +
			"sent. Stopping it now closes the teammate's pane, with whatever it was doing, once you\n" +
			"have typed its name. The lead is never stopped from here. Name the teammate by its pane\n" +
			"id (the rail's x key does). Outside a workspace pane, name the workspace with --session.",
		Example: "  lmux stop --to %3\n" +
			"  lmux stop --session api --to %3",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("stop asks how to stop the teammate and needs a terminal; lyna-tmux opens it in a popup")
			}
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			view, err := s.OpenStop(ctx, h, req)
			if err != nil {
				return wsTargetErr(err, "--session <name>")
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			ui := rootUI{host: h, config: s.Config, term: term}
			return d.stopForm(cmd, ui, view, tui.NewStop(view.Options))
		},
	}
	steerFlags(cmd, d, &req, "workspace of the teammate (default: the current one)", "pane id of the teammate to stop, such as %3")
	return cmd
}

// steerFlags are the flags message and stop share: the workspace, and the
// pane of the agent, which is required since nothing else identifies one.
func steerFlags(cmd *cobra.Command, d Deps, req *app.SteerRequest, session, to string) {
	cmd.Flags().StringVarP(&req.Target.Session, "session", "s", "", session)
	cmd.Flags().StringVar(&req.To, "to", "", to)
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.RegisterFlagCompletionFunc("session", wsSessionChoices(d))
}

// messageModel is the message form as the command drives it.
type messageModel interface {
	tea.Model
	Result() (tui.MessageResult, bool)
}

// messageForm asks what to send, sends it and says so. A form that ended
// without a message sends nothing.
func (d Deps) messageForm(cmd *cobra.Command, ui rootUI, view app.MessageView, m messageModel) error {
	if err := rootProgram(cmd, ui, m); err != nil {
		return err
	}
	res, done := m.Result()
	if !done || !res.Sent {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Nothing was sent")
		return err
	}
	out, err := view.Send(cmd.Context(), res)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Sent to %s in workspace %s\n", out.Agent, out.Session)
	return err
}

// stopModel is the stop form as the command drives it.
type stopModel interface {
	tea.Model
	Result() (tui.StopResult, bool)
}

// stopForm asks how to stop the teammate, stops it and says what happened. A
// form that ended without an answer stops nothing.
func (d Deps) stopForm(cmd *cobra.Command, ui rootUI, view app.StopView, m stopModel) error {
	if err := rootProgram(cmd, ui, m); err != nil {
		return err
	}
	res, done := m.Result()
	if !done || !res.Sent {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Nothing was stopped")
		return err
	}
	out, err := view.Stop(cmd.Context(), res)
	if err != nil {
		return err
	}
	if out.Stopped {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s in workspace %s: its pane is closed\n", out.Agent, out.Session)
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Asked the lead of workspace %s to shut down %s:\n%s\n", out.Session, out.Agent, res.Message)
	return err
}
