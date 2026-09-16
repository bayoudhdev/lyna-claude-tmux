package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// workspaceCommands control panes, windows and popups of a running
// workspace: popup, task, team, resume, layout and split.
func workspaceCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{
		popupCommand(d),
		taskCommand(d),
		teamCommand(d),
		resumeCommand(d),
		layoutCommand(d),
		splitCommand(d),
	}
}

// pluginTmuxCommands are the subcommands of `plugin` for a tmux server the
// user runs.
func pluginTmuxCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{pluginTmuxCommand(d)}
}

// wsTargetErr names the flag that selects a target when a window command ran
// outside a workspace pane.
func wsTargetErr(err error, flag string) error {
	if errors.Is(err, app.ErrNoTarget) {
		return fmt.Errorf("%w: pass %s (lyna-tmux ls lists workspaces)", err, flag)
	}
	return err
}

// wsWarn prints housekeeping warnings on stderr.
func wsWarn(cmd *cobra.Command, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", sanitize.Line(w))
	}
}

// wsSessionChoices lists the running workspaces, for completion.
func wsSessionChoices(d Deps) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		client, err := d.wsCompleteClient()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		sessions, err := client.ListSessions(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out := make([]string, len(sessions))
		for i, x := range sessions {
			out[i] = x.Name
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// wsCompleteClient addresses the lyna-tmux server without opening it:
// pressing TAB must write nothing, and opening the server rewrites the
// generated tmux configuration whenever its content changed. Listing never
// starts a server, so that file is not needed here.
func (d Deps) wsCompleteClient() (*tmux.Client, error) {
	h, err := d.Host()
	if err != nil {
		return nil, err
	}
	name := h.Getenv(session.EnvSocketName)
	if name == "" {
		name = tmux.DefaultSocketName
	} else if err := session.Validate(name); err != nil {
		return nil, err
	}
	return tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: tmux.Socket{Name: name}, Env: app.ServerEnviron(h.Environ)}), nil
}
