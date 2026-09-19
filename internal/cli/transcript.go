package cli

import (
	"context"
	"errors"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// transcriptCommands returns the commands that read the transcript of an agent.
func transcriptCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{newTranscriptCmd(d)}
}

func newTranscriptCmd(d Deps) *cobra.Command {
	var session, to, agent string
	cmd := &cobra.Command{
		Use:   "transcript",
		Short: "Read what an agent of the workspace is writing",
		Long: "Follow the transcript Claude Code writes for the agent of a pane: the prompts it\n" +
			"was given, what it answered, and the tools it called. With --agent it follows a\n" +
			"subagent of that pane, which runs in no pane of its own. The view follows the end\n" +
			"of the transcript while it is at the end, and q or esc closes it.",
		Example: "  lmux transcript --to %3\n" +
			"  lmux transcript --to %3 --agent a3f2e1d0c9b8a7f6e",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("the transcript reader draws a live view and needs a terminal; lyna-tmux opens it in a popup")
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			view, err := app.OpenTranscript(cmd.Context(), h, app.TranscriptRequest{Session: session, Pane: to, Agent: agent})
			if err != nil {
				return err
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			view.Options.Now = d.now
			// The host environment, not the parent process, decides the colors.
			return transcriptRun(cmd.Context(), view, streams(cmd), tea.WithEnvironment(h.Environ))
		},
	}
	f := cmd.Flags()
	f.StringVar(&to, "to", "", "pane of the agent to read (a tmux pane id)")
	f.StringVar(&session, "session", "", "workspace the agent belongs to, outside tmux")
	f.StringVar(&agent, "agent", "", "subagent of that pane to read instead of the agent itself")
	_ = cmd.MarkFlagRequired("to")
	for _, name := range []string{"to", "session", "agent"} {
		_ = cmd.RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)
	}
	return cmd
}

// transcriptRun draws the reader on its own refresh loop: the transcript's own
// writes, the agents of the workspace, and the idle fallback. The loop and the
// program stop together, and no goroutine outlives the call.
func transcriptRun(ctx context.Context, view app.TranscriptView, s Streams, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	updates := make(chan tui.TranscriptUpdate)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Go(func() {
		// Closing tells a reader still waiting for a reading that none will come.
		defer close(updates)
		loop := watch.Loop{
			Signal:   view.Signal,
			Watch:    view.Watch,
			Debounce: app.TranscriptDebounce,
			Idle:     app.TranscriptIdle,
			Refresh: func(ctx context.Context) {
				select {
				case updates <- view.Read(ctx):
				case <-ctx.Done():
				}
			},
		}
		loop.Run(ctx)
	})
	options := view.Options
	options.Updates = updates
	base := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(s.In), tea.WithOutput(s.Out)}
	if options.Width > 0 && options.Height > 0 {
		// Without a size the program draws nothing until the terminal reports one.
		base = append(base, tea.WithWindowSize(options.Width, options.Height))
	}
	program := tea.NewProgram(tui.NewTranscript(options), append(base, opts...)...)
	_, err := program.Run()
	cancel()
	return err
}
