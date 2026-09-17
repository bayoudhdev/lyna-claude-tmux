package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// teamCommand is create with agent teams turned on for the launch: the same
// arguments, flags and attach behavior.
func teamCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team [dir] [-- claude arguments]",
		Short: "Open a Claude workspace with agent teams turned on",
		Long: "Open a Claude workspace for the project that contains dir, like create, with experimental\n" +
			"agent teams turned on for its Claude panes: teammates open as tmux panes in the workspace.\n" +
			"A project that already has a running workspace is attached as it is. Arguments after -- are\n" +
			"passed to claude.",
		Example: "  lmux team\n  lmux team ~/src/api -l solo --model opus",
	}
	return createFlags(cmd, d, createCommand{
		Teams: true,
		Existing: func(res app.CreateResult) string {
			return fmt.Sprintf("Workspace %s is already open for %s; agent teams apply to workspaces this command starts\n",
				res.Name, sanitize.Line(res.Project))
		},
		Running: func(res app.CreateResult) string {
			return fmt.Sprintf("Workspace %s is running with agent teams. Attach with: lmux attach %s\n", res.Name, res.Name)
		},
	})
}
