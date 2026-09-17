package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// newSetupCmd builds `setup` with the production review plugin source.
func newSetupCmd(d Deps) *cobra.Command { return newSetupCmdWith(d, app.ReviewPlugin{}) }

// newSetupCmdWith builds `setup` for a review plugin source; tests install
// from a local repository instead of the network.
func newSetupCmdWith(d Deps, src app.ReviewPlugin) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Choose the look, layout, Claude and sandbox defaults in a guided wizard",
		Long: "Walk through the main settings starting from the current configuration. Saving\n" +
			"writes config.toml (an existing file is kept as config.toml.bak) and applies it\n" +
			"to a running lyna-tmux server, then offers to carry out what the choices need to\n" +
			"work: the dev container of container isolation, shell completion, the review\n" +
			"plugin. Canceling writes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := d.Terminal()
			if !term.Interactive {
				return errors.New("setup needs a terminal; edit the configuration with lyna-tmux config edit, or write the documented defaults with lyna-tmux config init")
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			_, cfg, err := app.LoadConfig(h)
			if err != nil {
				return err
			}
			return d.setupRun(cmd, rootUI{host: h, config: cfg, term: term}, src, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "carry out what the saved settings need without asking")
	return cmd
}

// setupModel is the wizard as the command drives it.
type setupModel interface {
	tea.Model
	Result() (tui.SetupResult, bool)
}

// setupRun runs the setup wizard over the loaded configuration.
func (d Deps) setupRun(cmd *cobra.Command, ui rootUI, src app.ReviewPlugin, yes bool) error {
	// A linked configuration file is refused before any question is asked.
	if _, err := app.SetupTarget(ui.host); err != nil {
		return err
	}
	styles, err := ui.styles()
	if err != nil {
		return err
	}
	return d.setupWizard(cmd, ui, tui.NewSetup(tui.SetupOptions{
		Styles: styles, Config: ui.config, Getenv: ui.host.Getenv, Width: ui.term.Width, Height: ui.term.Height,
	}), src, yes)
}

// setupWizard runs a wizard model, then saves and applies what it confirmed.
func (d Deps) setupWizard(cmd *cobra.Command, ui rootUI, m setupModel, src app.ReviewPlugin, yes bool) error {
	if err := rootProgram(cmd, ui, m); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	res, done := m.Result()
	if !done || !res.Saved {
		_, err := fmt.Fprintln(out, "Setup canceled; nothing was written")
		return err
	}
	saved, err := app.SaveSetup(ui.host, res.Config)
	if err != nil {
		return err
	}
	if saved.Backup != "" {
		fmt.Fprintf(out, "Saved the previous file to %s\n", saved.Backup)
	}
	fmt.Fprintf(out, "Wrote %s\n", saved.Path)
	for _, c := range res.Changes {
		fmt.Fprintf(out, "  %s: %s -> %s\n", c.Key, setupValue(c.From), setupValue(c.To))
	}
	running, err := app.ApplySetup(cmd.Context(), ui.host)
	switch {
	case err != nil:
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not apply the settings to the lyna-tmux server: %v\n", err)
	case running:
		fmt.Fprintln(out, "Applied to the running lyna-tmux server")
	default:
		fmt.Fprintln(out, "The lyna-tmux server is not running; the next workspace starts with these settings")
	}
	if _, err := fmt.Fprint(out, setupGuidance(ui.host.Getenv, runtime.GOOS, res.Config.UI.AltKeys)); err != nil {
		return err
	}
	return d.setupApply(cmd, ui, src, res.Config, yes)
}

// setupApply carries out what the saved settings need to work. A wizard that
// only writes a file leaves the next command to fail on the choice just made:
// container isolation with no container built, process isolation with no
// sandbox runtime, a review editor with no plugin. Each step is offered on its
// own and refusing one still leaves the configuration saved.
func (d Deps) setupApply(cmd *cobra.Command, ui rootUI, src app.ReviewPlugin, cfg config.Config, yes bool) error {
	out := cmd.OutOrStdout()
	ask := func(question string) bool {
		if yes {
			return true
		}
		ok, err := d.infraConfirm(cmd, question, "")
		return err == nil && ok
	}
	switch cfg.Sandbox.Isolation {
	case string(sandbox.IsolationContainer):
		if err := d.setupContainer(cmd, ask); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", sanitize.Line(err.Error()))
		}
	case string(sandbox.IsolationProcess):
		// The runtime is never installed for the user: it is a global npm
		// package, and which Node.js it lands in is their decision.
		if _, err := d.LookPath(sandbox.RuntimeBinary); err != nil {
			fmt.Fprintf(out, "\nProcess isolation runs Claude inside %s, which is not on PATH:\n  %s\n",
				sandbox.RuntimeBinary, sandbox.RuntimeInstall)
		}
	}
	if err := d.setupCompletion(cmd, ui.host, ask); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", sanitize.Line(err.Error()))
	}
	return d.setupReview(cmd, ui.host, src, cfg, ask)
}

// setupContainer builds and starts the dev container of the project the user
// stands in, so container isolation works from the next create on.
func (d Deps) setupContainer(cmd *cobra.Command, ask func(string) bool) error {
	cwd, err := d.Getwd()
	if err != nil {
		return err
	}
	root, err := session.ProjectRoot(cwd)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "\nContainer isolation needs a dev container per project. In each project run:\n  lyna-tmux sandbox devcontainer init\n  lyna-tmux sandbox devcontainer up\n")
		return nil
	}
	target, err := app.DevcontainerTarget(root, true)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", sanitize.Line(err.Error()))
		return nil
	}
	docker, err := d.devcontainerDocker(cmd)
	if err != nil {
		return err
	}
	if !ask(fmt.Sprintf("Build and start the dev container %s for %s now?", target.Container(), sanitize.Line(target.Dir))) {
		fmt.Fprintln(cmd.OutOrStdout(), "  Skipped; run lyna-tmux sandbox devcontainer up when you want it.")
		return nil
	}
	if err := docker.Up(cmd.Context(), target); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Container %s is running\n", target.Container())
	return err
}

// setupReview installs the review plugin when reviews are meant to open in the
// editor lyna-tmux installs and there is nothing installed yet.
func (d Deps) setupReview(cmd *cobra.Command, h app.Host, src app.ReviewPlugin, cfg config.Config, ask func(string) bool) error {
	if editor, err := domain.ParseEditorMode(cfg.Review.Editor); err == nil && editor == domain.EditorUser {
		return nil
	}
	state, err := app.ReviewStatus(cmd.Context(), h, src)
	if err != nil {
		return nil
	}
	if state.Report.Installed && state.Report.PluginOK() {
		return nil
	}
	if !ask("Install the review plugin (codediff.nvim) now?") {
		fmt.Fprintln(cmd.OutOrStdout(), "  Skipped; run lyna-tmux review install when you want it.")
		return nil
	}
	res, err := app.ReviewInstall(cmd.Context(), h, src, false)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", sanitize.Line(err.Error()))
		return nil
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Installed codediff.nvim %s in %s\n", res.Version, sanitize.Line(res.PluginDir))
	return err
}

func setupValue(v string) string {
	if v == "" {
		return "(default)"
	}
	return v
}

// setupCompletion is one shell's completion script: where it goes, how the
// command tree writes it, and what the shell still needs from the user.
type setupCompletionTarget struct {
	shell string
	// file is the path below the home directory the shell loads it from.
	file []string
	gen  func(root *cobra.Command, w io.Writer) error
	// note is a line the user still has to add themselves; their shell
	// startup file is theirs, so lyna-tmux never edits it.
	note string
}

// setupCompletions are the completion scripts, one shell per entry, written to
// per-user directories each shell loads on its own.
var setupCompletions = []setupCompletionTarget{
	{
		shell: "bash", file: []string{".local", "share", "bash-completion", "completions", "lyna-tmux"},
		gen: func(root *cobra.Command, w io.Writer) error { return root.GenBashCompletionV2(w, true) },
	},
	{
		shell: "zsh", file: []string{".zfunc", "_lyna-tmux"},
		gen:  func(root *cobra.Command, w io.Writer) error { return root.GenZshCompletion(w) },
		note: "add fpath=(~/.zfunc $fpath) before compinit in ~/.zshrc",
	},
	{
		shell: "fish", file: []string{".config", "fish", "completions", "lyna-tmux.fish"},
		gen: func(root *cobra.Command, w io.Writer) error { return root.GenFishCompletion(w, true) },
	},
}

// setupCompletionLines is how a target reads as commands to run by hand.
func (c setupCompletionTarget) lines() []string {
	path := "~/" + strings.Join(c.file, "/")
	lines := []string{
		"mkdir -p " + strings.TrimSuffix(path, "/"+c.file[len(c.file)-1]),
		"lyna-tmux completion " + c.shell + " > " + path,
	}
	if c.note != "" {
		lines = append(lines, "# then "+c.note)
	}
	return lines
}

// setupCompletion offers to write the completion script of the login shell.
// Only the script is written: it lives in a directory the shell reads on its
// own, and the startup file that may still need a line is the user's.
func (d Deps) setupCompletion(cmd *cobra.Command, h app.Host, ask func(string) bool) error {
	target, ok := setupShell(h.Getenv("SHELL"))
	if !ok || !filepath.IsAbs(h.Home) {
		return nil
	}
	path := filepath.Join(append([]string{h.Home}, target.file...)...)
	if !ask(fmt.Sprintf("Write %s completion to %s?", target.shell, sanitize.Line(path))) {
		fmt.Fprintln(cmd.OutOrStdout(), "  Skipped; the commands are above.")
		return nil
	}
	var buf bytes.Buffer
	if err := target.gen(cmd.Root(), &buf); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", sanitize.Line(path))
	if target.note != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  One line is still yours to add: %s\n", target.note)
	}
	return nil
}

// setupShell matches the login shell against the completion targets.
func setupShell(shell string) (setupCompletionTarget, bool) {
	name := filepath.Base(shell)
	for _, c := range setupCompletions {
		if c.shell == name {
			return c, true
		}
	}
	return setupCompletionTarget{}, false
}

// setupGuidance tells how to install shell completion for the login shell
// (every supported shell when it is unknown) and, when Alt keys are on, how
// to make the detected terminal send Option or Alt as Meta.
func setupGuidance(getenv func(string) string, goos string, altKeys bool) string {
	var b strings.Builder
	shell := filepath.Base(getenv("SHELL"))
	matched := false
	for _, c := range setupCompletions {
		matched = matched || c.shell == shell
	}
	for _, c := range setupCompletions {
		if matched && c.shell != shell {
			continue
		}
		fmt.Fprintf(&b, "\nShell completion for %s:\n", c.shell)
		for _, l := range c.lines() {
			b.WriteString("  " + l + "\n")
		}
	}
	if !altKeys {
		return b.String()
	}
	program := termx.Detect(getenv).Program
	g := termx.OptionAsMeta(program, goos)
	if g.Needed {
		fmt.Fprintf(&b, "\nAlt keys need Option sent as Meta in %s:\n  %s\n", program.Name(), g.Setting)
	} else {
		fmt.Fprintf(&b, "\nAlt keys in %s:\n  %s\n", program.Name(), g.Setting)
	}
	return b.String()
}
