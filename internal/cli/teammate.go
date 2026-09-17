package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// teammateClaudeFlag names the agent the launcher runs. The workspace resolved
// it when it wrote the launcher, so a teammate starts the same agent the lead
// runs and no lookup on PATH decides it here.
const teammateClaudeFlag = "--claude"

// teammateCommand is what Claude Code runs in place of the agent when it opens
// a teammate: the generated launcher execs it with the agent to run, then the
// arguments Claude Code appended for that teammate.
func teammateCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "teammate --claude <path> -- [agent arguments]",
		Short: "Run a Claude Code teammate in a pane of the workspace",
		Long: "Claude Code runs this through the launcher a workspace generates for its agent teams.\n" +
			"It labels the pane the teammate was opened in, gives it the workspace look and state,\n" +
			"then becomes the agent with the arguments it was given. It is never run by hand.",
		Hidden: true,
		// The arguments after -- belong to the agent: cobra must not read a
		// flag of its own out of them, reorder them or reject one it does not
		// know. They are passed on exactly as they arrived.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			claudePath, rest, err := teammateArgs(args)
			if err != nil {
				return err
			}
			h, err := d.Host()
			if err != nil {
				// Without a host there is nothing to label the pane with, and
				// the teammate still has to start.
				return d.Exec(claudePath, append([]string{claudePath}, rest...), os.Environ())
			}
			defaults := config.Default()
			req := app.TeammateRequest{
				ClaudePath: claudePath, Args: rest, Now: d.Now,
				AgentPanes: defaults.Workspace.AgentPanes,
				Sidebar:    defaults.UI.AgentsSidebar,
			}
			// The configuration decides how many teammates share the lead's
			// window and whether the workspace opens the agents rail with the
			// first of them. One that cannot be read leaves the defaults in
			// place: a teammate is started here, not a workspace.
			if paths, err := xdg.Resolve(h.Getenv, h.Home); err == nil {
				req.LogPath = paths.LogFile()
				if cfg, _, err := config.Load(paths.ConfigFile()); err == nil {
					req.AgentPanes = cfg.Workspace.AgentPanes
					req.Sidebar = cfg.UI.AgentsSidebar
				}
			}
			run, err := app.Teammate(cmd.Context(), h, req)
			if err != nil {
				return err
			}
			return d.Exec(run.Path, run.Argv, run.Env)
		},
	}
}

// errTeammateArgs reports a command line the launcher does not write.
var errTeammateArgs = errors.New("teammate: expected --claude <path> -- [agent arguments]")

// teammateArgs splits the launcher's own arguments from the agent's. Only
// --claude is read, in both spellings, and everything after -- belongs to the
// agent, including anything that looks like a flag of ours.
func teammateArgs(args []string) (claudePath string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = args[i+1:]
			break
		}
		switch {
		case arg == teammateClaudeFlag:
			if i+1 == len(args) {
				return "", nil, errTeammateArgs
			}
			i++
			claudePath = args[i]
		case strings.HasPrefix(arg, teammateClaudeFlag+"="):
			claudePath = strings.TrimPrefix(arg, teammateClaudeFlag+"=")
		default:
			return "", nil, errTeammateArgs
		}
	}
	if claudePath == "" {
		return "", nil, errTeammateArgs
	}
	return claudePath, rest, nil
}
