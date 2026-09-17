package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// The rail redraws on the agents of the workspace and on nothing else: the
// hooks signal every change, the debounce coalesces the burst a spawning team
// produces, and the fallback catches what no hook reports (a pane the user
// closed, a teammate Claude Code opened its own way).
const (
	AgentBarDebounce = 120 * time.Millisecond
	AgentBarIdle     = 10 * time.Second
	// agentBarTimeout bounds one reading of the server.
	agentBarTimeout = 3 * time.Second
)

// ErrAgentBarNoWorkspace reports a rail with no workspace to follow.
var ErrAgentBarNoWorkspace = errors.New("the agents rail follows a workspace")

// AgentBarRequest is one run of the agents rail.
type AgentBarRequest struct {
	// Session is the workspace to follow outside tmux; inside a pane the pane
	// says which workspace it belongs to.
	Session string
	// Popup makes q and esc close the rail.
	Popup bool
	// CloseWhenEmpty ends a rail that opened by itself once the agents it
	// opened for are gone.
	CloseWhenEmpty bool
}

// AgentBarView is everything the rail runs on: how to read the agents, how to
// wait for them to change, and the model's own options.
type AgentBarView struct {
	Options tui.AgentBarOptions
	// Read takes one reading of the agents of the workspace.
	Read func(ctx context.Context) tui.AgentBarUpdate
	// Signal blocks until an agent of the workspace changes.
	Signal func(ctx context.Context) error
}

// OpenAgentBar prepares the agents rail for the workspace the caller is in.
//
// Inside a workspace pane the pane names the workspace; outside tmux the rail
// follows the workspace named in the request on the lyna-tmux server. The
// reading is one list-panes plus the team files Claude Code writes, which are
// only ever read.
func OpenAgentBar(ctx context.Context, h Host, req AgentBarRequest) (AgentBarView, error) {
	var (
		client  *tmux.Client
		cfg     config.Config
		name    string
		target  string
		inside  bool
		claudes = xdg.ClaudeHome(h.Getenv, h.Home)
	)
	if req.Session != "" {
		if err := session.Validate(req.Session); err != nil {
			return AgentBarView{}, err
		}
	}
	if socket, ok := tmux.SocketFromEnv(h.Getenv("TMUX")); ok {
		_, loaded, err := LoadConfig(h)
		if err != nil {
			return AgentBarView{}, err
		}
		cfg = loaded
		client = tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: agentBarEnviron(h)})
		inside = true
		switch pane := h.Getenv("TMUX_PANE"); {
		case tmux.ValidPaneID(pane):
			target = pane
			if name, err = client.Display(ctx, pane, "#{session_name}"); err != nil || name == "" {
				return AgentBarView{}, fmt.Errorf("cannot read the workspace of pane %s: %w", sanitize.Line(pane), err)
			}
		case req.Session != "":
			target, name = tmux.ExactSession(req.Session), req.Session
		default:
			return AgentBarView{}, fmt.Errorf("%w: pass --session", ErrAgentBarNoWorkspace)
		}
	} else {
		if req.Session == "" {
			return AgentBarView{}, fmt.Errorf("%w: outside tmux, pass --session with a workspace name", ErrAgentBarNoWorkspace)
		}
		s, err := OpenServer(ctx, h)
		if err != nil {
			return AgentBarView{}, err
		}
		ok, err := s.Client.HasSession(ctx, req.Session)
		if err != nil {
			return AgentBarView{}, err
		}
		if !ok {
			return AgentBarView{}, fmt.Errorf("%w: %s", ErrNoWorkspace, req.Session)
		}
		client, cfg = s.Client, s.Config
		target, name = tmux.ExactSession(req.Session), req.Session
	}

	look, err := tui.ThemeFromConfig(cfg.UI, h.Getenv)
	if err != nil {
		return AgentBarView{}, err
	}
	bar := &agentBar{client: client, session: name, claudeHome: claudes, inside: inside}
	return AgentBarView{
		Options: tui.AgentBarOptions{
			Styles:         tui.NewStyles(look),
			Popup:          req.Popup,
			CloseWhenEmpty: req.CloseWhenEmpty,
			Actions:        bar.actions(),
		},
		Read:   bar.read,
		Signal: watch.TmuxAgentsSignal(client, target),
	}, nil
}

// agentBarEnviron is the environment the rail's tmux commands run with: the
// server environment, plus the client this pane belongs to, which is what a
// jump switches.
func agentBarEnviron(h Host) []string {
	env := ServerEnviron(h.Environ)
	for _, key := range []string{"TMUX", "TMUX_PANE"} {
		if val := h.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}

// agentBar reads the agents of one workspace and acts on them.
type agentBar struct {
	client     *tmux.Client
	session    string
	claudeHome string
	// inside reports a rail running in a pane of the server it reads, where a
	// jump switches the client it runs on.
	inside bool
}

// read takes one reading: the panes of the server, and the team Claude Code
// wrote down, which are joined into the rows the rail draws.
func (b *agentBar) read(ctx context.Context) tui.AgentBarUpdate {
	ctx, cancel := context.WithTimeout(ctx, agentBarTimeout)
	defer cancel()
	now := time.Now()
	panes, err := b.client.ListPanes(ctx, "")
	if err != nil {
		return tui.AgentBarUpdate{Err: err, At: now}
	}
	in := team.Input{Session: b.session, Panes: agentBarPanes(panes)}
	if name := agentBarTeam(in); name != "" {
		cfg, err := claude.ReadTeam(b.claudeHome, name)
		if err != nil {
			return tui.AgentBarUpdate{View: team.Build(in), Err: err, At: now}
		}
		in.Config = cfg
		if in.Tasks, err = claude.ReadTasks(b.claudeHome, name); err != nil {
			return tui.AgentBarUpdate{View: team.Build(in), Err: err, At: now}
		}
	}
	return tui.AgentBarUpdate{View: team.Build(in), At: now}
}

// agentBarPanes is what the view reads about the panes of a server.
func agentBarPanes(panes []tmux.Pane) []team.Pane {
	out := make([]team.Pane, 0, len(panes))
	for _, p := range panes {
		out = append(out, team.Pane{
			ID: p.ID, Session: p.SessionName, Window: p.WindowID, WindowName: p.WindowName,
			Role: p.Role, State: p.State, Agent: p.Agent, AgentType: p.AgentType, Team: p.Team,
			Subagents: cells(p.Subagents), Running: team.ParseRunning(p.Running),
			Active: p.Active && p.WindowActive, Dead: p.Dead,
		})
	}
	return out
}

// agentBarTeam is the team the workspace runs, which is the team its own panes
// belong to. Nothing else knows it: the name is Claude Code's own, and the
// panes of the team carry it because the teammates were labeled with it.
func agentBarTeam(in team.Input) string {
	for _, p := range in.Panes {
		if p.Session == in.Session && p.Team != "" {
			return p.Team
		}
	}
	return ""
}

// actions are what the rail's keys do to an agent.
func (b *agentBar) actions() tui.AgentBarActions {
	return tui.AgentBarActions{
		Focus:  b.focus,
		Zoom:   b.zoom,
		Window: b.window,
	}
}

// focus brings the client to the agent's pane.
func (b *agentBar) focus(row team.Row) tea.Cmd {
	return b.on(row, func(pane string) []tmux.Command { return agentBarFocus(pane, b.inside) })
}

// agentBarFocus is how a client is brought to a pane. A rail drawn in a pane
// of the server it reads moves the client it runs on; one drawn outside it can
// only say which pane the next client to attach lands on.
//
// Every command names the pane by its id, which tmux resolves to the session
// and the window holding it, whatever they were called when the pane was
// listed.
func agentBarFocus(pane string, inside bool) []tmux.Command {
	if inside {
		return []tmux.Command{{"switch-client", "-t", pane}, {"select-pane", "-t", pane}}
	}
	return []tmux.Command{{"select-window", "-t", pane}, {"select-pane", "-t", pane}}
}

// zoom makes the agent's pane the whole window, and takes it back.
func (b *agentBar) zoom(row team.Row) tea.Cmd {
	return b.on(row, func(pane string) []tmux.Command {
		return []tmux.Command{{"resize-pane", "-Z", "-t", pane}}
	})
}

// on runs commands about the pane of a row, and says so when the row has none:
// a member of the team that Claude Code opened somewhere else is on the rail to
// be seen, not to be steered from here.
func (b *agentBar) on(row team.Row, cmds func(pane string) []tmux.Command) tea.Cmd {
	pane, ok := row.Target()
	if !ok {
		return agentBarSay(agentBarNoPane(row))
	}
	return b.run(row, cmds(pane)...)
}

// agentBarNoPane is what the rail says about an agent it cannot reach.
func agentBarNoPane(row team.Row) string { return row.Name + " runs in no pane of this server" }

// window moves the agent to a window of its own, and puts the window it
// leaves back to the arrangement it had before any agent was opened in it.
func (b *agentBar) window(row team.Row) tea.Cmd {
	pane, ok := row.Target()
	if !ok {
		return agentBarSay(agentBarNoPane(row))
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), agentBarTimeout)
		defer cancel()
		remembered, err := b.client.ShowOption(ctx, "-w", row.Window, tmux.OptWinLayout)
		if err != nil {
			return tui.AgentBarNote(err.Error())
		}
		cmds := tmux.BreakOutTeammate(pane, row.Window, row.Name, remembered)
		if len(cmds) == 0 {
			return tui.AgentBarNote(row.Name + " is in no window this rail can rearrange")
		}
		if _, err := b.client.Batch(ctx, cmds...); err != nil {
			return tui.AgentBarNote(err.Error())
		}
		return nil
	}
}

// run sends commands about one agent, and reports what came back as the note
// under the rows: a pane that is gone is what the next reading shows anyway.
func (b *agentBar) run(row team.Row, cmds ...tmux.Command) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), agentBarTimeout)
		defer cancel()
		if _, err := b.client.Batch(ctx, cmds...); err != nil {
			if errors.Is(err, tmux.ErrNotFound) || errors.Is(err, tmux.ErrNoServer) {
				return tui.AgentBarNote(row.Name + ": its pane is gone")
			}
			return tui.AgentBarNote(err.Error())
		}
		return nil
	}
}

// agentBarSay is a note and nothing else.
func agentBarSay(text string) tea.Cmd {
	return func() tea.Msg { return tui.AgentBarNote(text) }
}
