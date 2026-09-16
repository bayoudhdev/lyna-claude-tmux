package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

func newSetupCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Choose the look, layout, Claude and sandbox defaults in a guided wizard",
		Long: "Walk through the main settings starting from the current configuration. Saving\n" +
			"writes config.toml (an existing file is kept as config.toml.bak) and applies it\n" +
			"to a running lyna-tmux server. Canceling writes nothing.",
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
			return d.setupRun(cmd, rootUI{host: h, config: cfg, term: term})
		},
	}
}

// setupModel is the wizard as the command drives it.
type setupModel interface {
	tea.Model
	Result() (tui.SetupResult, bool)
}

// setupRun runs the setup wizard over the loaded configuration.
func (d Deps) setupRun(cmd *cobra.Command, ui rootUI) error {
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
	}))
}

// setupWizard runs a wizard model, then saves and applies what it confirmed.
func (d Deps) setupWizard(cmd *cobra.Command, ui rootUI, m setupModel) error {
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
	_, err = fmt.Fprint(out, setupGuidance(ui.host.Getenv, runtime.GOOS, res.Config.UI.AltKeys))
	return err
}

func setupValue(v string) string {
	if v == "" {
		return "(default)"
	}
	return v
}

// setupCompletions are the install commands of the shell completion scripts,
// one shell per entry, written to per-user directories each shell loads.
var setupCompletions = []struct {
	shell string
	lines []string
}{
	{"bash", []string{
		"mkdir -p ~/.local/share/bash-completion/completions",
		"lyna-tmux completion bash > ~/.local/share/bash-completion/completions/lyna-tmux",
	}},
	{"zsh", []string{
		"mkdir -p ~/.zfunc",
		"lyna-tmux completion zsh > ~/.zfunc/_lyna-tmux",
		"# then add fpath=(~/.zfunc $fpath) before compinit in ~/.zshrc",
	}},
	{"fish", []string{
		"mkdir -p ~/.config/fish/completions",
		"lyna-tmux completion fish > ~/.config/fish/completions/lyna-tmux.fish",
	}},
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
		for _, l := range c.lines {
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
