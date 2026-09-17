package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// dashRefreshEvery re-lists sessions and agents while the dashboard is open.
const dashRefreshEvery = 5 * time.Second

// rootUI is what the full-screen commands share: the host, the loaded
// configuration and the terminal they draw on.
type rootUI struct {
	host   app.Host
	config config.Config
	term   Terminal
}

// styles resolves the configured look against the terminal environment.
func (ui rootUI) styles() (tui.Styles, error) {
	th, err := tui.ThemeFromConfig(ui.config.UI, ui.host.Getenv)
	if err != nil {
		return tui.Styles{}, err
	}
	return tui.NewStyles(th), nil
}

// rootProgram runs a full-screen model on the command's streams until it
// quits. The environment of the host, not of the test or parent process,
// decides the color profile.
func rootProgram(cmd *cobra.Command, ui rootUI, m tea.Model) error {
	opts := []tea.ProgramOption{
		tea.WithContext(cmd.Context()),
		tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(cmd.OutOrStdout()),
		tea.WithEnvironment(ui.host.Environ),
	}
	if ui.term.Width > 0 && ui.term.Height > 0 {
		opts = append(opts, tea.WithWindowSize(ui.term.Width, ui.term.Height))
	}
	_, err := tea.NewProgram(m, opts...).Run()
	return err
}

// rootExecCmd is a program a full-screen model runs with the terminal handed
// over (tmux attach, claude attach).
type rootExecCmd struct{ *exec.Cmd }

func (c rootExecCmd) SetStdin(r io.Reader)  { c.Stdin = r }
func (c rootExecCmd) SetStdout(w io.Writer) { c.Stdout = w }
func (c rootExecCmd) SetStderr(w io.Writer) { c.Stderr = w }

// rootExec turns an attach description into a terminal handover command.
func (d Deps) rootExec(att app.Attach) (tea.ExecCommand, error) {
	if len(att.Argv) == 0 {
		return nil, errors.New("empty attach command")
	}
	path, err := d.LookPath(att.Argv[0])
	if err != nil {
		return nil, err
	}
	c := exec.Command(path, att.Argv[1:]...)
	c.Env = att.Env
	return rootExecCmd{c}, nil
}

// rootRunE runs lyna-tmux without a command: the dashboard on a terminal,
// the workspace list otherwise.
func rootRunE(d Deps) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		term := d.Terminal()
		if !term.Interactive {
			ls := newLsCmd(d)
			ls.SetContext(cmd.Context())
			ls.SetOut(cmd.OutOrStdout())
			ls.SetErr(cmd.ErrOrStderr())
			return ls.RunE(ls, nil)
		}
		_, h, s, err := d.openServer(cmd)
		if err != nil {
			return err
		}
		cwd, err := d.Getwd()
		if err != nil {
			return err
		}
		defaults, err := dashDefaults(s.Config, cwd)
		if err != nil {
			return err
		}
		return d.dashRun(cmd, rootUI{host: h, config: s.Config, term: term}, dashBackend{
			sessions: s,
			agents:   app.NewAgentsView(h, s, d.now),
			actions:  dashActions{d: d, h: h, s: s, term: term, cmd: cmd},
			layouts:  layoutChoices(d),
			defaults: defaults,
			validateDir: func(dir string) error {
				_, err := dashDir(dir, h.Home, d.Getwd)
				return err
			},
		})
	}
}

// dashBackend is what the dashboard lists and acts on.
type dashBackend struct {
	sessions    tui.SessionSource
	agents      agentsBackend
	actions     tui.DashboardActions
	layouts     []string
	defaults    tui.CreateRequest
	validateDir func(string) error
}

// dashRun runs the dashboard, then the screen it chose: the agents picker or
// the setup wizard.
func (d Deps) dashRun(cmd *cobra.Command, ui rootUI, b dashBackend) error {
	styles, err := ui.styles()
	if err != nil {
		return err
	}
	m := tui.NewDashboard(tui.DashboardOptions{
		Styles:       styles,
		Sessions:     b.sessions,
		Agents:       b.agents,
		Actions:      b.actions,
		Context:      cmd.Context(),
		Width:        ui.term.Width,
		Height:       ui.term.Height,
		Now:          d.now,
		Home:         ui.host.Home,
		Layouts:      b.layouts,
		Defaults:     b.defaults,
		ValidateDir:  b.validateDir,
		RefreshEvery: dashRefreshEvery,
	})
	if err := rootProgram(cmd, ui, m); err != nil {
		return err
	}
	switch m.Next() {
	case tui.NextAgents:
		// The dashboard owns the whole terminal, never a popup.
		return d.agentsPick(cmd, ui, b.agents, false)
	case tui.NextSetup:
		return d.setupRun(cmd, ui, app.ReviewPlugin{}, false)
	case tui.NextNone:
	}
	return nil
}

// dashDefaults prefill the create form from the configuration. A
// configuration that turns the sandbox off is refused here as everywhere
// else: prefilling another profile would hide it, and prefilling off would
// honor a choice the configuration file cannot make.
func dashDefaults(cfg config.Config, cwd string) (tui.CreateRequest, error) {
	if cfg.Sandbox.Profile == string(sandbox.Off) {
		return tui.CreateRequest{}, fmt.Errorf("%w: pass --sandbox off to launch without a sandbox", app.ErrSandboxOffInConfig)
	}
	return tui.CreateRequest{Dir: cwd, Layout: cfg.Workspace.Layout, Sandbox: cfg.Sandbox.Profile}, nil
}

// dashDir resolves the create form's directory, which the user types, so it
// must name a directory that exists.
func dashDir(dir, home string, getwd func() (string, error)) (string, error) {
	return existingDir(dir, home, getwd)
}

// dashActions are the dashboard's effects on the lyna-tmux server.
type dashActions struct {
	d    Deps
	h    app.Host
	s    *app.Server
	term Terminal
	// cmd carries the streams a workspace that is created outside this host
	// reports on, and its context.
	cmd *cobra.Command
}

func (x dashActions) Attach(ctx context.Context, sess tmux.Session) (tea.ExecCommand, error) {
	if _, err := x.s.Sync(ctx); err != nil {
		return nil, err
	}
	att, err := x.s.AttachCommand(ctx, x.h, sess.Name)
	if err != nil {
		return nil, err
	}
	return x.d.rootExec(att)
}

func (x dashActions) Kill(ctx context.Context, sess tmux.Session) error {
	return x.s.Kill(ctx, sess.Name)
}

func (x dashActions) Create(ctx context.Context, req tui.CreateRequest) (tea.ExecCommand, error) {
	dir, err := dashDir(req.Dir, x.h.Home, x.d.Getwd)
	if err != nil {
		return nil, err
	}
	creq := app.CreateRequest{
		Dir: dir, Layout: req.Layout, Width: x.term.Width, Height: x.term.Height,
		Launch: app.LaunchOptions{Sandbox: req.Sandbox},
	}
	// A workspace that belongs in a dev container is opened there instead, and
	// the dashboard hands the terminal over to it just like an attach.
	argv, handled, err := x.d.containerCreateArgv(x.cmd, x.s, creq, false, x.term.Interactive)
	if err != nil {
		return nil, err
	}
	if handled {
		return x.d.rootExec(app.Attach{Argv: argv})
	}
	res, err := x.s.Create(ctx, x.h, creq)
	if err != nil {
		return nil, err
	}
	att, err := x.s.AttachCommand(ctx, x.h, res.Name)
	if err != nil {
		return nil, err
	}
	exe, err := x.d.rootExec(att)
	if err != nil {
		return nil, err
	}
	return dashHold(exe, dashCreateNotice(res)), nil
}

// dashCreateNotice is what a create has to say before the terminal goes to
// tmux: the project whose workspace was already running, and the housekeeping
// the server could not finish.
func dashCreateNotice(res app.CreateResult) []string {
	var lines []string
	if res.Existing {
		lines = append(lines, fmt.Sprintf("Workspace %s is already open for %s", res.Name, sanitize.Line(res.Project)))
	}
	for _, w := range res.Warnings {
		lines = append(lines, "Warning: "+sanitize.Line(w))
	}
	return lines
}

// dashHold holds the terminal on lines before the handover starts. Printing
// them and handing over at once would lose them: tmux clears the screen as it
// takes the terminal. Nothing to say hands the terminal over as it is.
func dashHold(exe tea.ExecCommand, lines []string) tea.ExecCommand {
	if len(lines) == 0 {
		return exe
	}
	return &dashNotice{ExecCommand: exe, lines: lines}
}

// dashNotice is a terminal handover that shows lines and waits for a key
// first. The dashboard has released the terminal by then, so the lines land
// on the screen the user sees.
type dashNotice struct {
	tea.ExecCommand
	lines []string
	in    io.Reader
	out   io.Writer
}

func (n *dashNotice) SetStdin(r io.Reader) {
	n.in = r
	n.ExecCommand.SetStdin(r)
}

func (n *dashNotice) SetStdout(w io.Writer) {
	n.out = w
	n.ExecCommand.SetStdout(w)
}

func (n *dashNotice) Run() error {
	if n.out != nil && n.in != nil {
		for _, l := range n.lines {
			fmt.Fprintln(n.out, l)
		}
		fmt.Fprint(n.out, "\nPress any key to continue.")
		sandboxWaitKey(n.in)
		fmt.Fprintln(n.out)
	}
	return n.ExecCommand.Run()
}
