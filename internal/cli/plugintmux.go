package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func pluginTmuxCommand(d Deps) *cobra.Command {
	var (
		write, apply, noBell bool
		launchKey, listKey   string
	)
	cmd := &cobra.Command{
		Use:   "tmux",
		Short: "Use the Claude popup and agents picker from your own tmux",
		Long: "Print the plugin mode configuration for a tmux server you run: prefix+y shows the Claude\n" +
			"session of the current directory in a popup (press it again inside to close it), prefix+u\n" +
			"opens the agents picker, and bells from popup sessions reach the window they were opened from.\n\n" +
			"--write saves the configuration in the lyna-tmux state directory and prints the source-file\n" +
			"line to add to your tmux configuration. --apply installs it on the tmux server this runs in\n" +
			"(the tpm plugin entry calls it) and honors the @claude_launch_key, @claude_list_key and\n" +
			"@claude_forward_bell options unless a flag overrides them. An empty key leaves it unbound.",
		Example: "  lyna-tmux plugin tmux --write\n" +
			"  lyna-tmux plugin tmux --launch-key C-y --no-bell > ~/.config/tmux/lyna-tmux.conf",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			req := app.PluginRequest{NoBell: noBell}
			if cmd.Flags().Changed("launch-key") {
				req.LaunchKey = &launchKey
			}
			if cmd.Flags().Changed("list-key") {
				req.ListKey = &listKey
			}
			if apply {
				s, err := app.OpenPluginServer(cmd.Context(), h)
				if err != nil {
					return err
				}
				_, err = s.ApplyPlugin(cmd.Context(), h, req)
				return err
			}
			opts, err := app.PluginOptionsFor(h.Exe, tmux.PluginUserOptions{}, req)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !write {
				_, err := fmt.Fprint(out, tmux.PluginConf(opts))
				return err
			}
			path, err := app.WritePluginConf(h, opts)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Wrote %s. Add this line to your tmux configuration:\n%s\n", path, app.PluginSourceLine(path))
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&write, "write", false, "write the configuration to the state directory and print the line that loads it")
	f.BoolVar(&apply, "apply", false, "install plugin mode on the tmux server this runs in ($TMUX)")
	f.StringVar(&launchKey, "launch-key", app.DefaultPluginLaunchKey, "prefix key that toggles the Claude popup")
	f.StringVar(&listKey, "list-key", app.DefaultPluginListKey, "prefix key that opens the agents picker")
	f.BoolVar(&noBell, "no-bell", false, "do not forward bells from popup sessions to their window")
	cmd.MarkFlagsMutuallyExclusive("write", "apply")
	return cmd
}
