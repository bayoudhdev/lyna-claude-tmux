package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// doctorSystem is the machine doctor and sandbox status inspect. Tests
// replace it with a fake that runs no real program.
var doctorSystem = doctor.System

// errDoctorFailed is returned, with exit code 1, when a check fails.
var errDoctorFailed = errors.New("doctor found a failing check; run the fix shown under it")

// errDoctorUnfixed is returned, with exit code 1, when --fix leaves a failing
// check behind, either because nobody can fix it here or because it was
// skipped.
var errDoctorUnfixed = errors.New("doctor left a failing check unfixed")

// doctorCommand builds `doctor` with the production plugin source.
func doctorCommand(d Deps) *cobra.Command { return doctorCommandWith(d, app.ReviewPlugin{}) }

// doctorCommandWith builds `doctor` for a review plugin source; tests install
// from a local repository instead of the network.
func doctorCommandWith(d Deps, src app.ReviewPlugin) *cobra.Command {
	var asJSON, fix, yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check tmux, Claude Code, the sandbox, the terminal and the review editor",
		Long: "Check everything lyna-tmux and Claude Code need on this machine, with the\n" +
			"configuration applied: tmux, Claude Code, git, the sandbox of the configured profile\n" +
			"and isolation level, terminal colors, clipboard, Option or Alt keys, key collisions,\n" +
			"Docker, Neovim and the review editor. Every warning and failure shows the command or\n" +
			"setting that fixes it. Without --fix, doctor only reads and installs nothing. It\n" +
			"exits 1 when a check fails.\n\n" +
			"With --fix it offers to apply the fixes it can carry out itself, one at a time, and\n" +
			"prints the rest for you to apply: a terminal setting, an account and a package\n" +
			"manager that asks for a password are yours to run. It never edits ~/.tmux.conf or\n" +
			"the Claude Code settings.",
		Example: "  lyna-tmux doctor\n" +
			"  lyna-tmux doctor --fix\n" +
			"  lyna-tmux doctor --json | jq '.results[] | select(.status == \"fail\")'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON && fix {
				return errors.New("--json prints a report to read; run --fix without it")
			}
			if (yes || dryRun) && !fix {
				return errors.New("--yes and --dry-run apply to --fix; pass --fix as well")
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			// A failing working directory only skips the project rows.
			cwd, _ := d.Getwd()
			r := app.DoctorRun(cmd.Context(), h, doctorSystem(), cwd, src)
			if asJSON {
				err = doctor.WriteJSON(cmd.OutOrStdout(), r)
			} else {
				err = doctor.WriteText(cmd.OutOrStdout(), r, d.Terminal().Width)
			}
			if err != nil {
				return err
			}
			if fix {
				return d.doctorFix(cmd, src, r, yes, dryRun)
			}
			if r.Failed() {
				return &exitError{code: 1, err: errDoctorFailed}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "print the report as JSON")
	f.BoolVar(&fix, "fix", false, "offer to apply the fixes lyna-tmux can carry out")
	f.BoolVar(&yes, "yes", false, "with --fix, apply every fix without asking")
	f.BoolVar(&dryRun, "dry-run", false, "with --fix, print what would be applied and change nothing")
	return cmd
}

// doctorAnswer is what the user said about one fix.
type doctorAnswer int

const (
	doctorSkip doctorAnswer = iota
	doctorApply
	doctorApplyAll
	doctorQuit
)

// doctorFix walks the rows that carry an action and applies the ones the user
// accepts. Rows whose fix only the user can carry out are listed at the end,
// so a machine that needs a terminal setting or a package manager still ends
// the run with the exact steps in one place.
func (d Deps) doctorFix(cmd *cobra.Command, src app.ReviewPlugin, r doctor.Report, yes, dryRun bool) error {
	var manual, applied, failed, skipped []doctor.Result
	all := yes
	out := cmd.OutOrStdout()
	fmt.Fprintln(out)
	reader := bufio.NewReader(cmd.InOrStdin())
	for _, res := range r.Results {
		if res.Status != doctor.StatusWarn && res.Status != doctor.StatusFail {
			continue
		}
		if res.Action == nil || res.Action.Kind == doctor.FixManual {
			manual = append(manual, res)
			continue
		}
		fmt.Fprintf(out, "%s: %s\n  %s\n", sanitize.Line(res.Title), sanitize.Line(res.Action.What), doctorActionLine(res.Action))
		if dryRun {
			skipped = append(skipped, res)
			continue
		}
		if !all {
			answer, err := d.doctorAsk(cmd, reader)
			if err != nil {
				return err
			}
			switch answer {
			case doctorQuit:
				fmt.Fprintln(out, "Stopped; nothing else was applied.")
				return doctorFixResult(r, applied, failed, append(skipped, res))
			case doctorSkip:
				skipped = append(skipped, res)
				continue
			case doctorApplyAll:
				all = true
			case doctorApply:
			}
		}
		if err := d.doctorApply(cmd, src, res.Action); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", sanitize.Line(res.Title), sanitize.Line(err.Error()))
			failed = append(failed, res)
			continue
		}
		applied = append(applied, res)
	}
	if len(manual) > 0 {
		fmt.Fprintln(out, "Left for you to apply:")
		for _, res := range manual {
			fmt.Fprintf(out, "  %s: %s\n", sanitize.Line(res.Title), strings.Join(cleanFixLines(res.Fix), "\n    "))
		}
	}
	if len(applied) > 0 && !dryRun {
		fmt.Fprintf(out, "\n%d fix applied; run lyna-tmux doctor again to confirm.\n", len(applied))
	}
	return doctorFixResult(r, applied, failed, skipped)
}

// doctorActionLine renders what an action runs, for the line above the
// question.
func doctorActionLine(a *doctor.Action) string {
	if a.Kind == doctor.FixCommand {
		parts := make([]string, len(a.Argv))
		for i, arg := range a.Argv {
			parts[i] = sanitize.Line(arg)
		}
		return strings.Join(parts, " ")
	}
	return "lyna-tmux " + doctorBuiltinCommand(a.ID)
}

// doctorBuiltinCommand names the lyna-tmux command a builtin fix is the
// equivalent of, so the line above the question is one the user could type.
func doctorBuiltinCommand(id string) string {
	switch id {
	case doctor.FixReviewInstall:
		return "review install"
	case doctor.FixReviewReinstall:
		return "review install --force"
	}
	return id
}

// doctorApply carries out one action: a command as it stands, or a step of
// lyna-tmux's own.
func (d Deps) doctorApply(cmd *cobra.Command, src app.ReviewPlugin, a *doctor.Action) error {
	switch a.Kind {
	case doctor.FixCommand:
		if len(a.Argv) == 0 {
			return errors.New("the fix names no command")
		}
		return d.Run(cmd.Context(), a.Argv, streams(cmd))
	case doctor.FixBuiltin:
		return d.doctorBuiltin(cmd, src, a.ID)
	}
	return fmt.Errorf("fix of kind %q cannot be applied here", string(a.Kind))
}

// doctorBuiltin runs a step lyna-tmux carries out itself.
func (d Deps) doctorBuiltin(cmd *cobra.Command, src app.ReviewPlugin, id string) error {
	switch id {
	case doctor.FixReviewInstall, doctor.FixReviewReinstall:
		h, err := d.Host()
		if err != nil {
			return err
		}
		res, err := app.ReviewInstall(cmd.Context(), h, src, id == doctor.FixReviewReinstall)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Installed codediff.nvim %s in %s\n", res.Version, sanitize.Line(res.PluginDir))
		return err
	}
	return fmt.Errorf("unknown fix %q", id)
}

// doctorAsk reads one answer: apply, skip, all or quit. Without a terminal
// nobody can answer, which --yes is for.
func (d Deps) doctorAsk(cmd *cobra.Command, r *bufio.Reader) (doctorAnswer, error) {
	if !d.Terminal().Interactive {
		return doctorQuit, errors.New("this is not a terminal to answer on; pass --yes to apply every fix, or --dry-run to see them")
	}
	fmt.Fprint(cmd.OutOrStdout(), "  apply? [y]es / [n]o / [a]ll / [q]uit ")
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return doctorQuit, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return doctorApply, nil
	case "a", "all":
		return doctorApplyAll, nil
	case "q", "quit":
		return doctorQuit, nil
	}
	return doctorSkip, nil
}

// doctorFixResult decides the exit status of a --fix run: a failing check that
// is still failing is an error, whether it was skipped, could not be applied
// or has no fix lyna-tmux can carry out.
func doctorFixResult(r doctor.Report, applied, failed, skipped []doctor.Result) error {
	fixed := map[string]bool{}
	for _, res := range applied {
		fixed[res.ID] = true
	}
	for _, res := range append(failed, skipped...) {
		fixed[res.ID] = false
	}
	for _, res := range r.Results {
		if res.Status == doctor.StatusFail && !fixed[res.ID] {
			return &exitError{code: 1, err: errDoctorUnfixed}
		}
	}
	return nil
}

// cleanFixLines splits a fix into sanitized lines for the manual list.
func cleanFixLines(fix string) []string {
	var out []string
	for line := range strings.SplitSeq(fix, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, sanitize.Line(line))
		}
	}
	return out
}
