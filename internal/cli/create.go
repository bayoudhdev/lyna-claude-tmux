package cli

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// createCommand describes one workspace-opening command. create and team
// differ only in their texts and in the launch they ask for.
type createCommand struct {
	// Teams turns agent teams on for the launches this command starts.
	Teams bool
	// Existing and Running render the messages for a workspace that is
	// already open and for one started detached.
	Existing func(res app.CreateResult) string
	Running  func(res app.CreateResult) string
}

func newCreateCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "create [dir] [-- claude arguments]",
		Aliases: []string{"new"},
		Short:   "Open the Claude workspace for a project, or attach to its running one",
		Long: "Open a Claude workspace for the project that contains dir (the current directory\n" +
			"by default) and attach this terminal to it. A project that already has a running\n" +
			"workspace is attached as it is. Arguments after -- are passed to claude.",
		Example: "  lmux create\n" +
			"  lmux create ~/src/api -l trio --model opus\n" +
			"  lyna-tmux new -c -- --verbose",
	}
	return createFlags(cmd, d, createCommand{
		Existing: func(res app.CreateResult) string {
			return fmt.Sprintf("Workspace %s is already open for %s\n", res.Name, sanitize.Line(res.Project))
		},
		Running: func(res app.CreateResult) string {
			return fmt.Sprintf("Workspace %s is running. Attach with: lmux attach %s\n", res.Name, res.Name)
		},
	})
}

// createFlags gives cmd the arguments, flags, completions and behavior of
// create, launching with what c asks for.
func createFlags(cmd *cobra.Command, d Deps, c createCommand) *cobra.Command {
	var (
		req            app.CreateRequest
		detach         bool
		nested         bool
		startContainer bool
	)
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if n := cmd.ArgsLenAtDash(); n > 1 || n < 0 && len(args) > 1 {
			return errors.New("pass at most one directory; put claude arguments after --")
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		req.Launch.Teams = c.Teams
		start := containerStartAsk
		if startContainer {
			start = containerStartAlways
		}
		return d.runCreate(cmd, args, req, detach, nested, start, c)
	}
	f := cmd.Flags()
	f.StringVarP(&req.Layout, "layout", "l", "", "layout: solo, duo, trio, quad, review, auto or a custom layout")
	f.StringVarP(&req.Name, "name", "n", "", "workspace name (default: the project directory name)")
	f.StringVar(&req.Launch.Model, "model", "", "Claude model, for example opus")
	f.StringVar(&req.Launch.Effort, "effort", "", "reasoning effort")
	f.StringVar(&req.Launch.PermissionMode, "mode", "", "permission mode")
	f.StringVar(&req.Launch.Sandbox, "sandbox", "", "sandbox profile: standard, strict or off")
	f.StringVar(&req.Launch.Isolation, "isolation", "", "isolation level")
	f.BoolVarP(&req.Launch.Continue, "continue", "c", false, "continue the most recent conversation")
	f.BoolVarP(&detach, "detach", "d", false, "start the workspace without attaching")
	f.BoolVar(&nested, "nested", false, "attach even from inside another tmux session")
	f.BoolVar(&startContainer, "start-container", false, "at container isolation, build and start the dev container without asking")
	// The size of a terminal this process cannot measure: container isolation
	// starts create inside the dev container, where the layout is chosen for
	// the terminal the user sits at, on the host.
	f.IntVar(&req.Width, "width", 0, "terminal width in cells")
	f.IntVar(&req.Height, "height", 0, "terminal height in cells")
	_ = f.MarkHidden("width")
	_ = f.MarkHidden("height")

	complete := func(values func(d Deps) []string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return values(d), cobra.ShellCompDirectiveNoFileComp
		}
	}
	for flag, values := range map[string]func(Deps) []string{
		"layout":    layoutChoices,
		"effort":    func(Deps) []string { return claudecfg.Efforts() },
		"mode":      func(Deps) []string { return claudecfg.PermissionModes() },
		"sandbox":   func(Deps) []string { return names(sandbox.Profiles()) },
		"isolation": func(Deps) []string { return names(sandbox.Isolations()) },
	} {
		_ = cmd.RegisterFlagCompletionFunc(flag, complete(values))
	}
	return cmd
}

// runCreate opens the workspace of req and attaches this terminal to it, or
// reports where it runs when detached.
func (d Deps) runCreate(cmd *cobra.Command, args []string, req app.CreateRequest, detach, nested bool, start containerStart, c createCommand) error {
	ctx, h, s, err := d.openServer(cmd)
	if err != nil {
		return err
	}
	dir, extra := splitDash(cmd, args)
	if req.Dir, err = expandDir(dir, h.Home, d.Getwd); err != nil {
		return err
	}
	req.Launch.ExtraArgs = extra
	term := d.Terminal()
	// A size given on the command line is the size of the terminal the user
	// sits at, which this process may not be attached to.
	if req.Width == 0 && req.Height == 0 {
		req.Width, req.Height = term.Width, term.Height
	}
	if !detach && !term.Interactive {
		return errors.New("this is not a terminal to attach; pass --detach to start the workspace in the background")
	}
	handled, err := d.createIsolated(cmd, s, req, detach, start)
	if handled || err != nil {
		return err
	}
	res, err := s.Create(ctx, h, req)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", w)
	}
	out := cmd.OutOrStdout()
	if res.Existing {
		fmt.Fprint(out, c.Existing(res))
	}
	if detach {
		_, err := fmt.Fprint(out, c.Running(res))
		return err
	}
	return d.attach(cmd, h, s, res.Name, attachOptions(nested)...)
}

// attachOptions turns the --nested flag into the option AttachCommand takes.
func attachOptions(nested bool) []app.AttachOption {
	if nested {
		return []app.AttachOption{app.AllowNested()}
	}
	return nil
}

// splitDash separates the optional directory from the arguments after --.
func splitDash(cmd *cobra.Command, args []string) (dir string, extra []string) {
	before := args
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		before, extra = args[:n], args[n:]
	}
	if len(before) == 1 {
		dir = before[0]
	}
	return dir, extra
}

// attach hands this terminal to a workspace: tmux replaces the process.
func (d Deps) attach(cmd *cobra.Command, h app.Host, s *app.Server, name string, opts ...app.AttachOption) error {
	att, err := s.AttachCommand(cmd.Context(), h, name, opts...)
	if err != nil {
		return err
	}
	path, err := d.LookPath(att.Argv[0])
	if err != nil {
		return err
	}
	return d.Exec(path, att.Argv, att.Env)
}

// layoutChoices lists the built-in layouts and the custom layouts of the
// configuration, for completion.
func layoutChoices(d Deps) []string {
	choices := layout.Names()
	h, err := d.Host()
	if err != nil {
		return choices
	}
	if _, cfg, err := app.LoadConfig(h); err == nil {
		choices = append(choices, slices.Sorted(maps.Keys(cfg.Layouts))...)
	}
	return choices
}

func names[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
