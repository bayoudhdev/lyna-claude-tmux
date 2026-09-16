package cli

import "github.com/spf13/cobra"

// newPluginCmd groups the integrations with a tmux server or a Claude Code
// setup the user runs themselves.
func newPluginCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Use lyna-tmux from your own tmux or Claude Code setup",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(pluginClaudeCommands(d)...)
	cmd.AddCommand(pluginTmuxCommands(d)...)
	return cmd
}
