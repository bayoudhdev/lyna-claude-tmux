package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
)

// doctorSystem is the machine doctor and sandbox status inspect. Tests
// replace it with a fake that runs no real program.
var doctorSystem = doctor.System

// errDoctorFailed is returned, with exit code 1, when a check fails.
var errDoctorFailed = errors.New("doctor found a failing check; run the fix shown under it")

func doctorCommand(d Deps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check tmux, Claude Code, the sandbox, the terminal and the review editor",
		Long: "Check everything lyna-tmux and Claude Code need on this machine, with the\n" +
			"configuration applied: tmux, Claude Code, git, the sandbox of the configured profile\n" +
			"and isolation level, terminal colors, clipboard, Option or Alt keys, key collisions,\n" +
			"Docker, Neovim and the review editor. Every warning and failure shows the command or\n" +
			"setting that fixes it. doctor only reads; it installs nothing. It exits 1 when a\n" +
			"check fails.",
		Example: "  lyna-tmux doctor\n" +
			"  lyna-tmux doctor --json | jq '.results[] | select(.status == \"fail\")'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			// A failing working directory only skips the project rows.
			cwd, _ := d.Getwd()
			r := app.DoctorRun(cmd.Context(), h, doctorSystem(), cwd)
			if asJSON {
				err = doctor.WriteJSON(cmd.OutOrStdout(), r)
			} else {
				err = doctor.WriteText(cmd.OutOrStdout(), r, d.Terminal().Width)
			}
			if err != nil {
				return err
			}
			if r.Failed() {
				return &exitError{code: 1, err: errDoctorFailed}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}
