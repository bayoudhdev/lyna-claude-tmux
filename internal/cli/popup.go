package cli

import (
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
)

// popupCommand groups the popups plugin mode key bindings open in a tmux
// server the user runs. Both print nothing on success: run-shell shows any
// output over the pane.
func popupCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "popup",
		Short:  "Open plugin mode popups in your own tmux",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		popupSubcommand(d, "launch", "Toggle the Claude popup of the pane's directory",
			"Show the Claude session of the pane's directory in a popup on the client, starting it with\n"+
				"per-launch settings when it is not running. Run from inside that popup, it closes the popup\n"+
				"and Claude keeps running. The tmux server comes from $TMUX.",
			func(cmd *cobra.Command, s *app.Server, h app.Host, req app.PopupRequest) error {
				_, err := s.PopupLaunch(cmd.Context(), h, req)
				return err
			}),
		popupSubcommand(d, "agents", "Open the agents picker in a popup",
			"Open the agents picker in a popup on the client, in the pane's directory. The tmux server\n"+
				"comes from $TMUX.",
			func(cmd *cobra.Command, s *app.Server, h app.Host, req app.PopupRequest) error {
				return s.PopupAgents(cmd.Context(), h, req)
			}),
	)
	return cmd
}

func popupSubcommand(d Deps, name, short, long string, run func(*cobra.Command, *app.Server, app.Host, app.PopupRequest) error) *cobra.Command {
	var req app.PopupRequest
	cmd := &cobra.Command{
		Use:     name + " --pane <pane_id> --client <client_name>",
		Short:   short,
		Long:    long,
		Example: "  bind-key -T prefix y run-shell -b 'lmux popup " + name + " --pane #{pane_id} --client #{q:client_name}'",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			s, err := app.OpenPluginServer(cmd.Context(), h)
			if err != nil {
				return err
			}
			return run(cmd, s, h, req)
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Pane, "pane", "", "id of the pane the key was pressed in (#{pane_id})")
	f.StringVar(&req.Client, "client", "", "name of the client to show the popup on (#{client_name})")
	_ = cmd.MarkFlagRequired("pane")
	_ = cmd.MarkFlagRequired("client")
	return cmd
}
