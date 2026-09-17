package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/statusline"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// glueCommands are the commands Claude Code and tmux call: hook, statusline,
// bell-forward, and the theme command.
func glueCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{hookCommand(d), statusCommand(d), bellCommand(d), themeCommand(d)}
}

// pluginClaudeCommands are the subcommands of `plugin` for Claude Code.
func pluginClaudeCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{pluginClaudeCommand(d)}
}

// hookPluginFlag marks invocations from the Claude plugin's hooks.json.
const hookPluginFlag = "--plugin"

func hookCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "hook [--plugin] <event>",
		Short: "Mirror a Claude Code hook event onto the tmux pane of the agent",
		Long: "Claude Code runs this for every registered hook event, with the event JSON on stdin.\n" +
			"It sets the pane state the status line, borders and pickers show. It never prints and\n" +
			"always exits 0; problems go to the lyna-tmux log file.",
		Hidden: true,
		// Arguments are read here instead of by cobra: a missing event, an
		// extra argument or an unknown flag must still exit 0 without output,
		// since Claude Code reports every hook failure to the user.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return nil //nolint:nilerr // a hook never fails; without a host there is no pane to update
			}
			event, plugin := hookArgs(args)
			hook.Run(cmd.Context(), hook.Input{Event: event, Plugin: plugin, Stdin: cmd.InOrStdin(), Getenv: h.Getenv}, hookDeps(d, h))
			return nil
		},
	}
}

// hookArgs returns the event, the first argument that is not the plugin flag,
// and whether the plugin flag was given. Anything else is ignored.
func hookArgs(args []string) (event string, plugin bool) {
	found := false
	for _, a := range args {
		switch {
		case a == hookPluginFlag:
			plugin = true
		case !found:
			event, found = a, true
		}
	}
	return event, plugin
}

// hookDeps are the handler dependencies for the host. Paths are resolved
// without loading the configuration or opening the server: hooks run on every
// tool call and must not depend on a valid config file or on tmux -V.
func hookDeps(d Deps, h app.Host) hook.Deps {
	deps := hook.Deps{
		Tmux:  func(s tmux.Socket) *tmux.Client { return tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: s}) },
		Getwd: d.Getwd,
		Now:   d.Now,
	}
	if h.Getenv != nil {
		if paths, err := xdg.Resolve(h.Getenv, h.Home); err == nil {
			deps.LogPath = paths.LogFile()
		}
	}
	return deps
}

func statusCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:    "statusline",
		Short:  "Render the Claude Code status line from the session JSON on stdin",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var getenv func(string) string
			if h, err := d.Host(); err == nil {
				getenv = h.Getenv
			}
			return glueStatus(statusline.Run(cmd.InOrStdin(), cmd.OutOrStdout(), getenv, d.now()))
		},
	}
}

func bellCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "bell-forward <session-id>",
		Short: "Forward the bell of a popup session to the window it was opened from",
		Long: "The alert-bell hook of the plugin-mode tmux configuration runs this with the ID of\n" +
			"the session whose window rang. The server comes from $TMUX. It never prints and\n" +
			"always exits 0; problems go to the lyna-tmux log file.",
		Hidden: true,
		// Arguments are read here instead of by cobra: a missing session ID,
		// an extra argument or an unknown flag must still exit 0 without
		// output, since tmux shows a failing hook to the user.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return nil //nolint:nilerr // a hook never fails; without a host there is no server to ring
			}
			// A broken configuration must not silence bells: the empty prefix
			// selects the default one.
			var prefix string
			if _, cfg, err := app.LoadConfig(h); err == nil {
				prefix = cfg.Popup.SessionPrefix
			}
			// A missing ID stays empty: the handler logs it as invalid.
			var id string
			if len(args) > 0 {
				id = args[0]
			}
			in := hook.BellForwardInput{Session: id, Prefix: prefix, Getenv: h.Getenv}
			hook.BellForward(cmd.Context(), in, hookDeps(d, h))
			return nil
		},
	}
}

// glueStatus turns the exit status of a handler into the command result.
func glueStatus(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code, err: fmt.Errorf("exit status %d", code)}
}

// pluginClaudeSource is the marketplace source of the Claude Code plugin: the
// GitHub repository publishing .claude-plugin/marketplace.json.
const pluginClaudeSource = "bayoudhdev/lyna-claude-tmux"

func pluginClaudeCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "claude",
		Short: "Show how to install the Claude Code plugin for tmux sessions you start yourself",
		Long: "Print the steps that install the lmux plugin in Claude Code, and check that the\n" +
			"lyna-tmux its hooks run from PATH is this binary.",
		Example: "  lmux plugin claude",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			found, warning := pluginClaudeBinary(d, h)
			if _, err := fmt.Fprint(cmd.OutOrStdout(), pluginClaudeSteps(found)); err != nil {
				return err
			}
			if warning != "" {
				_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning)
			}
			return err
		},
	}
}

// pluginClaudeSteps renders the install steps. found is the lyna-tmux on PATH
// when it is this binary, empty otherwise.
func pluginClaudeSteps(found string) string {
	id := hook.PluginName + "@" + hook.PluginName
	var b strings.Builder
	b.WriteString("The lmux plugin for Claude Code shows the state of each Claude session (busy,\n")
	b.WriteString("waiting, idle) on the tmux pane it runs in and rings the tmux bell when Claude needs\n")
	b.WriteString("you, in tmux sessions you start yourself. It also adds the Lyna themes to /theme.\n\n")
	b.WriteString("Install it inside Claude Code:\n")
	b.WriteString("  /plugin marketplace add " + pluginClaudeSource + "\n")
	b.WriteString("  /plugin install " + id + "\n")
	b.WriteString("  /reload-plugins\n\n")
	b.WriteString("Or from a shell:\n")
	b.WriteString("  claude plugin marketplace add " + pluginClaudeSource + "\n")
	b.WriteString("  claude plugin install " + id + "\n\n")
	if found != "" {
		b.WriteString("The plugin hooks run " + hook.PluginBinary + " from PATH, which is this binary (" + sanitize.Line(found) + ").\n")
	}
	b.WriteString("Workspaces started by lmux create already have these hooks and do not need the\n")
	b.WriteString("plugin; inside them the plugin stays idle.\n")
	return b.String()
}

// pluginClaudeBinary resolves the command the plugin hooks run (hooks.json
// looks it up with command -v, under the current name and then the name of
// 1.0.0) and compares it with the running binary.
func pluginClaudeBinary(d Deps, h app.Host) (found, warning string) {
	var name, path string
	for _, try := range []string{hook.PluginBinary, hook.PluginBinaryWas} {
		p, err := d.LookPath(try)
		if err == nil && p != "" {
			name, path = try, p
			break
		}
	}
	if path == "" {
		return "", fmt.Sprintf("%s is not on PATH, so the plugin hooks do nothing. Put this binary (%s) in a directory on the PATH Claude Code starts with.",
			hook.PluginBinary, sanitize.Line(h.Exe))
	}
	if !pluginClaudeSameFile(path, h.Exe) {
		return "", fmt.Sprintf("%s on PATH is %s, not this binary (%s). The plugin hooks run the one on PATH.",
			name, sanitize.Line(path), sanitize.Line(h.Exe))
	}
	return path, ""
}

// pluginClaudeSameFile reports whether two paths name the same file, through
// symbolic links.
func pluginClaudeSameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}
