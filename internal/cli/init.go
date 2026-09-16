package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// errInitCanceled reports a confirmation the user declined.
var errInitCanceled = errors.New("canceled: nothing was written")

func initCommand(d Deps) *cobra.Command {
	var project, yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write the lyna-tmux configuration, or the sandbox settings of a project",
		Long: "Without --project, write the documented user configuration file and print the next\n" +
			"steps. With --project, add the settings a repository shares with everyone who opens it:\n" +
			"the sandbox part of .claude/settings.json that Claude Code honors in a project, the\n" +
			".worktreeinclude patterns for local environment files, and the .gitignore entries for\n" +
			"worktrees and personal settings. Existing files are merged, never replaced: unknown\n" +
			"keys, list entries and comments stay as they are. Every change is shown first and\n" +
			"confirmed unless --yes.",
		Example: "  lyna-tmux init\n" +
			"  lyna-tmux init --project --dry-run\n" +
			"  lyna-tmux init --project ~/src/api --yes",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			if !project {
				if len(args) > 0 || yes || dryRun {
					return errors.New("a directory, --yes and --dry-run belong to `lyna-tmux init --project`")
				}
				return initConfig(cmd, h)
			}
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			dir, err := d.infraDir(h, arg)
			if err != nil {
				return err
			}
			plan, err := app.InitProjectPlan(h, dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			initWritePlan(out, plan)
			if !plan.Changed() {
				_, err = fmt.Fprintf(out, "%s already has the lyna-tmux project settings.\n", sanitize.Line(plan.Root))
				return err
			}
			if dryRun {
				_, err = fmt.Fprintln(out, "Dry run: nothing was written.")
				return err
			}
			if !yes {
				ok, err := d.infraConfirm(cmd, fmt.Sprintf("Apply these changes to %s?", sanitize.Line(plan.Root)),
					"init --project changes files in the project; pass --yes to apply them without a terminal, or --dry-run to see them")
				if err != nil {
					return err
				}
				if !ok {
					return errInitCanceled
				}
			}
			applied, err := plan.Apply()
			if err != nil {
				return err
			}
			if applied.Backup != "" {
				fmt.Fprintf(out, "Saved the previous file to %s\n", sanitize.Line(applied.Backup))
			}
			for _, p := range applied.Written {
				fmt.Fprintf(out, "Wrote %s\n", sanitize.Line(p))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&project, "project", false, "write the project settings instead of the user configuration")
	f.BoolVar(&yes, "yes", false, "apply the changes without asking")
	f.BoolVar(&dryRun, "dry-run", false, "show the changes and write nothing")
	return cmd
}

// initConfig writes the user configuration file, keeping an existing one.
func initConfig(cmd *cobra.Command, h app.Host) error {
	out := cmd.OutOrStdout()
	res, err := app.InitConfig(h, false)
	switch {
	case errors.Is(err, app.ErrConfigExists):
		fmt.Fprintf(out, "Kept %s\n", sanitize.Line(res.Path))
	case errors.Is(err, app.ErrConfigSymlink):
		fmt.Fprintf(out, "Kept the file %s points to\n", sanitize.Line(res.Path))
	case err != nil:
		return err
	default:
		fmt.Fprintf(out, "Wrote %s\n", sanitize.Line(res.Path))
	}
	_, err = fmt.Fprint(out, "\nNext steps:\n"+
		"  lyna-tmux doctor            check tmux, Claude Code and the sandbox on this machine\n"+
		"  lyna-tmux init --project    add the sandbox settings to a project you share\n"+
		"  lyna-tmux create            open the workspace of the current project\n")
	return err
}

// initWritePlan prints one section per project file: its path, what changes
// and the diff, with every line sanitized because the existing content comes
// from the repository.
func initWritePlan(w io.Writer, plan app.ProjectPlan) {
	fmt.Fprintf(w, "Project %s (sandbox profile %s)\n\n", sanitize.Line(plan.Root), plan.Profile)
	for _, f := range plan.Files {
		state := "unchanged"
		switch {
		case f.Changed && !f.Exists:
			state = "new file"
		case f.Changed:
			state = "updated"
		}
		fmt.Fprintf(w, "%s (%s)\n", sanitize.Line(f.Rel), state)
		for line := range strings.SplitSeq(strings.TrimSuffix(f.Diff, "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(w, "  %s\n", sanitize.Terminal(line))
			}
		}
		fmt.Fprintln(w)
	}
}
