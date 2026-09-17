package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// Picker refresh intervals. Each listing runs claude once, so the list
// refreshes slowly; a pane capture is cheap and follows the agent closely.
const (
	agentsRefreshEvery = 5 * time.Second
	agentsPreviewEvery = time.Second
)

// agentCommands are the agents picker and the setup wizard.
func agentCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{newAgentsCmd(d), newSetupCmd(d)}
}

func newAgentsCmd(d Deps) *cobra.Command {
	var popup, asJSON, rail bool
	var session string
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Pick a Claude agent: jump to its pane, attach a background job or stop it",
		Long: "List the Claude Code agents on this machine, waiting ones first, and jump to the\n" +
			"pane an agent runs in. Background jobs open with claude attach. Agents are located\n" +
			"in the tmux server this command runs inside, or in the lyna-tmux server.\n" +
			"Without a terminal the list is printed as a table.",
		Example: "  lmux agents\n" +
			"  lmux agents --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rail {
				return d.agentsRailRun(cmd, app.AgentBarRequest{Session: session, Popup: popup})
			}
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			view := app.NewAgentsView(h, s, d.now)
			if popup && !view.Inside() {
				return errors.New("--popup runs inside a tmux popup, but $TMUX is not set; run lmux agents")
			}
			term := d.Terminal()
			switch {
			case asJSON:
				snap, err := view.Refresh(ctx)
				if err != nil {
					return err
				}
				return agentsWriteJSON(cmd.OutOrStdout(), snap)
			case !term.Interactive && popup:
				return errors.New("--popup needs the terminal of a tmux popup")
			case !term.Interactive:
				snap, err := view.Refresh(ctx)
				if err != nil {
					return err
				}
				return agentsWriteTable(cmd.OutOrStdout(), snap, d.now(), h.Home)
			}
			return d.agentsPick(cmd, rootUI{host: h, config: s.Config, term: term}, view, popup)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&popup, "popup", false, "run inside a tmux popup: a jump switches the popup's client and closes the popup")
	f.BoolVar(&asJSON, "json", false, "print a fresh agents snapshot as JSON")
	f.BoolVar(&rail, "rail", false, "draw the live agents rail of one workspace instead of the picker")
	f.StringVar(&session, "session", "", "workspace the rail follows outside tmux")
	_ = f.MarkHidden("popup")
	_ = f.MarkHidden("rail")
	_ = f.MarkHidden("session")
	_ = cmd.RegisterFlagCompletionFunc("session", cobra.NoFileCompletions)
	cmd.MarkFlagsMutuallyExclusive("popup", "json")
	cmd.MarkFlagsMutuallyExclusive("rail", "json")
	return cmd
}

// agentsRailRun draws the agents rail until it quits. The refresh loop feeds
// the model through a channel; both stop together, and no goroutine outlives
// the call.
func (d Deps) agentsRailRun(cmd *cobra.Command, req app.AgentBarRequest) error {
	term := d.Terminal()
	if !term.Interactive {
		return errors.New("the agents rail draws a live view and needs a terminal; lyna-tmux starts it in a pane or popup")
	}
	h, err := d.Host()
	if err != nil {
		return err
	}
	view, err := app.OpenAgentBar(cmd.Context(), h, req)
	if err != nil {
		return err
	}
	view.Options.Width, view.Options.Height = term.Width, term.Height
	view.Options.Now = d.now
	// The host environment, not the parent process, decides the colors.
	return agentBarRun(cmd.Context(), view, streams(cmd), tea.WithEnvironment(h.Environ))
}

// agentBarRun runs the rail on its own refresh loop.
func agentBarRun(ctx context.Context, view app.AgentBarView, s Streams, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	updates := make(chan tui.AgentBarUpdate)
	var wg sync.WaitGroup
	// The loop stops on the canceled context alone, so the wait is registered
	// first and runs last: a panic out of the program cancels before waiting
	// instead of blocking on a goroutine nothing stopped.
	defer wg.Wait()
	defer cancel()
	wg.Go(func() {
		// Closing tells a rail still waiting for a reading that none will come.
		defer close(updates)
		loop := watch.Loop{
			Signal:   view.Signal,
			Debounce: app.AgentBarDebounce,
			Idle:     app.AgentBarIdle,
			Refresh: func(ctx context.Context) {
				select {
				case updates <- view.Read(ctx):
				case <-ctx.Done():
				}
			},
		}
		loop.Run(ctx)
	})
	options := view.Options
	options.Updates = updates
	base := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(s.In), tea.WithOutput(s.Out)}
	if options.Width > 0 && options.Height > 0 {
		// Without a size the program draws nothing until the terminal reports one.
		base = append(base, tea.WithWindowSize(options.Width, options.Height))
	}
	program := tea.NewProgram(tui.NewAgentBar(options), append(base, opts...)...)
	_, err := program.Run()
	cancel()
	return err
}

// agentsBackend lists agents and performs the picker's effects; app.AgentsView
// is the real one.
type agentsBackend interface {
	tui.AgentSource
	tui.PreviewSource
	Jump(ctx context.Context, a agent.Agent) (*app.Attach, error)
	AttachJob(a agent.Agent) (app.Attach, error)
	Kill(a agent.Agent) error
}

// agentsActions adapts a backend to the picker: a terminal handover becomes a
// command the picker runs with the terminal released.
type agentsActions struct {
	d Deps
	b agentsBackend
}

func (x agentsActions) Jump(ctx context.Context, a agent.Agent) (tea.ExecCommand, error) {
	att, err := x.b.Jump(ctx, a)
	if err != nil || att == nil {
		return nil, err
	}
	return x.d.rootExec(*att)
}

func (x agentsActions) Attach(_ context.Context, a agent.Agent) (tea.ExecCommand, error) {
	att, err := x.b.AttachJob(a)
	if err != nil {
		return nil, err
	}
	return x.d.rootExec(att)
}

func (x agentsActions) Kill(_ context.Context, a agent.Agent) error { return x.b.Kill(a) }

// agentsPick runs the agents picker until the user jumps, attaches or quits.
// popup tells the picker it draws in a tmux popup, which a jump closes.
func (d Deps) agentsPick(cmd *cobra.Command, ui rootUI, b agentsBackend, popup bool) error {
	styles, err := ui.styles()
	if err != nil {
		return err
	}
	return rootProgram(cmd, ui, tui.NewAgents(tui.AgentsOptions{
		Styles:       styles,
		Source:       b,
		Preview:      b,
		Actions:      agentsActions{d: d, b: b},
		Popup:        popup,
		Context:      cmd.Context(),
		Width:        ui.term.Width,
		Height:       ui.term.Height,
		Now:          d.now,
		Home:         ui.host.Home,
		RefreshEvery: agentsRefreshEvery,
		PreviewEvery: agentsPreviewEvery,
	}))
}

// agentsWriteJSON prints a snapshot in its versioned cache format, indented.
func agentsWriteJSON(w io.Writer, snap agent.Snapshot) error {
	data, err := agent.EncodeSnapshot(snap)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}

// agentsWriteTable prints a snapshot for a terminal-less caller. Names, paths
// and window names come from other programs and are sanitized.
func agentsWriteTable(w io.Writer, snap agent.Snapshot, now time.Time, home string) error {
	if len(snap.Agents) == 0 {
		_, err := fmt.Fprintln(w, "No Claude agents running. Start one with: lmux create")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tNAME\tWHERE\tSOURCE\tPID\tAGE")
	for _, a := range snap.Agents {
		pid := "-"
		if a.Record.PID > 0 {
			pid = strconv.Itoa(a.Record.PID)
		}
		age := "-"
		if a.Record.StartedAt > 0 {
			age = agentsAge(now.Sub(a.Record.Started()))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Status, dash(sanitize.Line(a.Title())),
			dash(agentsWhere(a, home)), a.Source, pid, age)
	}
	return tw.Flush()
}

// agentsWhere names where an agent runs: its pane, its job state, or its
// directory outside tmux.
func agentsWhere(a agent.Agent, home string) string {
	switch l := a.Location; {
	case l != nil:
		return sanitize.Line(l.Session + ":" + strconv.Itoa(l.WindowIndex) + " " + l.WindowName + " " + l.PaneID)
	case a.Source == agent.SourceBackground && a.Record.State != "":
		return "job " + sanitize.Line(string(a.Record.State))
	case a.Source == agent.SourceBackground:
		return "background"
	}
	dir := sanitize.Line(a.Record.CWD)
	if home != "" {
		if rest, ok := strings.CutPrefix(dir, filepath.Clean(home)+"/"); ok {
			return "~/" + rest
		}
	}
	return dir
}

// agentsAge is a compact duration: seconds, minutes, hours, then days.
func agentsAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(max(int(d/time.Second), 0)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}
