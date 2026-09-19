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

// taskCommands returns the commands that show the shared task list of a team.
func taskCommands(d Deps) []*cobra.Command { return []*cobra.Command{newTasksCmd(d)} }

func newTasksCmd(d Deps) *cobra.Command {
	var req app.TasksRequest
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "Show the shared task list of the team a workspace runs",
		Long: "Show every task of the team, who holds it, what it waits on and what it says,\n" +
			"refreshed as the team works through it. In a tmux pane or popup the list follows\n" +
			"the workspace of that pane; elsewhere --session names the workspace on the\n" +
			"lyna-tmux server. A workspace running a single agent shares no task list.",
		Example: "  lmux tasks\n" +
			"  lmux tasks --session api",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("tasks draws a live view and needs a terminal; lyna-tmux starts it in a pane or popup")
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			view, err := app.OpenTasks(cmd.Context(), h, req)
			if err != nil {
				return err
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			view.Options.Now = d.now
			// The host environment, not the parent process, decides the colors.
			return tasksRun(cmd.Context(), view, streams(cmd), tea.WithEnvironment(h.Environ))
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Session, "session", "", "workspace to follow outside tmux")
	f.BoolVar(&req.Popup, "popup", false, "run inside a tmux popup: q and esc close the list")
	_ = f.MarkHidden("popup")
	_ = cmd.RegisterFlagCompletionFunc("session", cobra.NoFileCompletions)
	return cmd
}

// tasksRun draws the task list until it quits. The refresh loop feeds the
// model through a channel; both stop together, and no goroutine outlives the
// call. A reading that draws what the last one did is dropped, so a list
// nothing happened to is never redrawn.
func tasksRun(ctx context.Context, view app.TasksView, s Streams, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	updates := make(chan tui.TasksUpdate)
	var wg sync.WaitGroup
	// The loop stops on the canceled context alone, so the wait is registered
	// first and runs last: a panic out of the program cancels before waiting
	// instead of blocking on a goroutine nothing stopped.
	defer wg.Wait()
	defer cancel()
	wg.Go(func() {
		// Closing tells a list still waiting for a reading that none will come.
		defer close(updates)
		loop := watch.Loop{
			Signal:   view.Signal,
			Debounce: app.AgentBarDebounce,
			Idle:     app.AgentBarIdle,
			Refresh: func(ctx context.Context) {
				u, fresh := view.Read(ctx)
				if !fresh {
					return
				}
				select {
				case updates <- u:
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
	program := tea.NewProgram(tui.NewTasks(options), append(base, opts...)...)
	_, err := program.Run()
	cancel()
	return err
}
