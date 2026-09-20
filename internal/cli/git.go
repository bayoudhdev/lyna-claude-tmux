package cli

import (
	"context"
	"errors"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// gitCommand is the git workstation: the refs of a project, its history, what
// is in front of you and every operation that moves it on. It runs in the git
// pane of a layout, in the pane the workstation key opens, and on its own in
// any terminal.
func gitCommand(d Deps) *cobra.Command {
	var req app.GitWorkRequest
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Open the git workstation on a working tree",
		Long: "Open the branches, the history and the working tree of a project side by side, with\n" +
			"every operation on a key: stage and commit, branch, merge, rebase, stash, tag, and\n" +
			"what a stopped rebase is answered with. Anything that cannot be undone asks first.\n" +
			"In a tmux pane or popup the view follows the workspace of that pane; elsewhere\n" +
			"--session names the workspace on the lyna-tmux server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("git draws a live view and needs a terminal; lyna-tmux starts it in a pane or popup")
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			if req.Dir, err = expandDir(req.Dir, h.Home, d.Getwd); err != nil {
				return err
			}
			view, err := app.OpenGitWork(cmd.Context(), h, req)
			if err != nil {
				return err
			}
			view.Options.Width, view.Options.Height = term.Width, term.Height
			// The host environment, not the parent process, decides the colors.
			return gitRun(cmd.Context(), view, streams(cmd), tea.WithEnvironment(h.Environ))
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

// gitRun runs the workstation until it quits. The reader feeds it through a
// channel; both stop together, and no goroutine outlives the call. A known
// terminal size in the options sizes the first frame.
func gitRun(ctx context.Context, view app.GitWork, s Streams, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	states := make(chan tui.GitState)
	var wg sync.WaitGroup
	// The reader stops on the canceled context alone, so the wait is
	// registered first and runs last: a panic out of the program cancels
	// before waiting instead of blocking on a goroutine nothing stopped.
	defer wg.Wait()
	defer cancel()
	wg.Go(func() {
		// Closing tells a view still waiting for a reading that none will come.
		defer close(states)
		view.Reader.Run(ctx, func(st tui.GitState) {
			select {
			case states <- st:
			case <-ctx.Done():
			}
		})
	})
	options := view.Options
	options.States = states
	base := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(s.In), tea.WithOutput(s.Out)}
	if options.Width > 0 && options.Height > 0 {
		// Without a size the program draws nothing until the terminal reports one.
		base = append(base, tea.WithWindowSize(options.Width, options.Height))
	}
	program := tea.NewProgram(tui.NewGitWork(options), append(base, opts...)...)
	_, err := program.Run()
	cancel()
	return err
}
