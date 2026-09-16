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

// watchCommand is the live changes view tmux runs in the changes pane of a
// layout and in the changes popup.
func watchCommand(d Deps) *cobra.Command {
	var req app.WatchRequest
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Show the live git changes of a working tree",
		Long: "Show the changed files of a working tree, refreshed when Claude edits a file, when git\n" +
			"state changes and every few seconds. In a tmux pane or popup the view follows the\n" +
			"workspace of that pane; elsewhere --session names the workspace on the lyna-tmux server.",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("watch draws a live view and needs a terminal; lyna-tmux starts it in a pane or popup")
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			if req.Dir, err = expandDir(req.Dir, h.Home, d.Getwd); err != nil {
				return err
			}
			view, err := app.OpenWatch(cmd.Context(), h, req)
			if err != nil {
				return err
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			// The host environment, not the parent process, decides the colors.
			return watchRun(cmd.Context(), view, streams(cmd), tea.WithEnvironment(h.Environ))
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Session, "session", "", "workspace to follow outside tmux")
	f.StringVar(&req.Dir, "dir", "", "working tree `DIR` (default: the current directory)")
	f.BoolVar(&req.Popup, "popup", false, "run inside a tmux popup: q and esc close the view")
	_ = cmd.MarkFlagDirname("dir")
	_ = cmd.RegisterFlagCompletionFunc("session", cobra.NoFileCompletions)
	return cmd
}

// watchRun runs the changes view until it quits. The watcher feeds the view
// through a channel; both stop together, and no goroutine outlives the call.
// A known terminal size in the options sizes the first frame.
func watchRun(ctx context.Context, view app.WatchView, s Streams, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	updates := make(chan watch.Update)
	var wg sync.WaitGroup
	// The watcher stops on the canceled context alone, so the wait is
	// registered first and runs last: a panic out of the program cancels
	// before waiting instead of blocking on a goroutine nothing stopped.
	defer wg.Wait()
	defer cancel()
	wg.Go(func() {
		// Closing tells a view still waiting for an update that none will come.
		defer close(updates)
		view.Watcher.Run(ctx, func(u watch.Update) {
			select {
			case updates <- u:
			case <-ctx.Done():
			}
		})
	})
	options := view.Options
	options.Updates = updates
	base := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(s.In), tea.WithOutput(s.Out)}
	if options.Width > 0 && options.Height > 0 {
		// Without a size the program draws nothing until the terminal reports one.
		base = append(base, tea.WithWindowSize(options.Width, options.Height))
	}
	program := tea.NewProgram(tui.NewChanges(options), append(base, opts...)...)
	_, err := program.Run()
	cancel()
	return err
}
