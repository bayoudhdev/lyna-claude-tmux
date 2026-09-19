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
	bar := &agentBar{
		client: client, session: name, claudeHome: claudes, inside: inside,
		usage: NewTranscriptUsage(claudes),
	}
	if agentBarSpawns(h, req, target) {
		bar.exe, bar.pane = h.Exe, target
	}
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

// agentBarSpawns reports a rail that opens forms: the one that starts an
// agent, and the ones that message an agent and stop a teammate.
//
// A form opens in a popup over the rail's own pane and acts on the workspace
// of that name on the lyna-tmux server, so the rail has to be a pane of that
// server: on a server of the user's own, a session of the same name on ours is
// a different workspace. A rail that is itself a popup has no pane to open the
// form over, and a display-popup run from inside a popup replaces that popup
// rather than opening another.
func agentBarSpawns(h Host, req AgentBarRequest, target string) bool {
	if req.Popup || h.Exe == "" || !tmux.ValidPaneID(target) {
		return false
	}
	name, err := serverSocketName(h)
	return err == nil && insideServer(h, name)
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
	// usage follows the transcripts of the agents on the rail, so a reading
	// costs what the transcripts gained since the last one. It is owned by the
	// refresh loop that calls read, which is its only caller; a rail built
	// without one reads no usage.
	usage *TranscriptUsage
	// exe is the lyna-tmux binary the popups run and pane the pane they are
	// drawn over. Both are empty for a rail that opens no popup.
	exe, pane string
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
	view := team.Build(in)
	// What the agents have spent is read last and never keeps the rows off the
	// rail: a transcript that cannot be read is said so under rows that are
	// otherwise complete.
	if b.usage != nil {
		if err := b.usage.Fill(ctx, view.Rows); err != nil {
			return tui.AgentBarUpdate{View: view, Err: err, At: now}
		}
	}
	return tui.AgentBarUpdate{View: view, At: now}
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
			// The transcript the hooks named is what the usage of the row, and
			// of every subagent of the pane, is read from.
			Transcript: p.Transcript,
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

// actions are what the rail's keys do to an agent, and what the workspace
// does when the rail asks for one more.
func (b *agentBar) actions() tui.AgentBarActions {
	a := tui.AgentBarActions{
		Focus:  b.focus,
		Zoom:   b.zoom,
		Window: b.window,
	}
	if b.exe != "" && b.pane != "" {
		a.Spawn, a.Message, a.Stop = b.spawn, b.message, b.stop
		a.Tasks, a.Transcript = b.tasks, b.transcript
	}
	return a
}

// Size of the popups the rail opens, as tmux reads it.
const (
	agentBarPopupWidth  = "80%"
	agentBarPopupHeight = "80%"
)

// The titles drawn on the borders of the rail's popups: the name of the
// command each one runs.
const (
	popupSpawnTitle      = "spawn"
	popupMessageTitle    = "message"
	popupStopTitle       = "stop"
	popupTasksTitle      = "tasks"
	popupTranscriptTitle = "transcript"
)

// spawn opens the form that starts an agent, in a popup over the rail.
func (b *agentBar) spawn() tea.Cmd { return b.open(b.spawnPopup) }

// message opens the form that types a message into the agent's pane.
func (b *agentBar) message(row team.Row) tea.Cmd { return b.steer(row, popupMessageTitle) }

// stop opens the form that stops a teammate.
func (b *agentBar) stop(row team.Row) tea.Cmd { return b.steer(row, popupStopTitle) }

// tasks opens the shared task list of the team, in a popup over the rail.
func (b *agentBar) tasks() tea.Cmd { return b.open(b.tasksPopup) }

// transcript opens what the agent of one row is writing. A subagent runs in
// the pane of the agent that started it and is named by its own identifier,
// which is how the reader tells the two transcripts of one pane apart.
func (b *agentBar) transcript(row team.Row) tea.Cmd {
	pane, ok := row.Target()
	if !ok {
		return agentBarSay(agentBarNoPane(row))
	}
	agent := row.AgentID
	return b.open(func() (tmux.Command, error) { return b.transcriptPopup(pane, agent) })
}

// steer opens a form about the agent of one row. The form checks the agent
// again when it opens and again before it acts; the rail only needs a pane to
// name it by.
func (b *agentBar) steer(row team.Row, command string) tea.Cmd {
	pane, ok := row.Target()
	if !ok {
		return agentBarSay(agentBarNoPane(row))
	}
	return b.open(func() (tmux.Command, error) { return b.steerPopup(command, pane) })
}

// open runs a popup of the rail.
//
// The command runs with no deadline of its own: display-popup returns when the
// popup closes, and the form is open for as long as the user is answering it.
// It ends with the rail, which is the process it runs in.
func (b *agentBar) open(popup func() (tmux.Command, error)) tea.Cmd {
	return func() tea.Msg {
		cmd, err := popup()
		if err != nil {
			return tui.AgentBarNote(err.Error())
		}
		if _, err := b.client.Batch(context.Background(), cmd); err != nil {
			return tui.AgentBarNote(err.Error())
		}
		return nil
	}
}

// spawnPopup is the popup that asks for an agent of the workspace the rail
// follows.
func (b *agentBar) spawnPopup() (tmux.Command, error) {
	return b.popup(popupSpawnTitle, "spawn", "--session", b.session)
}

// steerPopup is the popup that runs command on the agent in pane, which the
// command checks is an agent of the workspace the rail follows.
func (b *agentBar) steerPopup(command, pane string) (tmux.Command, error) {
	return b.popup(command, command, "--session", b.session, "--to", pane)
}

// tasksPopup is the popup that shows the task list of the team the workspace
// runs, read from the workspace the rail follows.
func (b *agentBar) tasksPopup() (tmux.Command, error) {
	return b.popup(popupTasksTitle, "tasks", "--session", b.session, "--popup")
}

// transcriptPopup is the popup that reads the transcript of the agent in pane,
// or of the subagent of that pane when one is named.
func (b *agentBar) transcriptPopup(pane, agent string) (tmux.Command, error) {
	args := []string{"transcript", "--session", b.session, "--to", pane}
	if agent != "" {
		args = append(args, "--agent", agent)
	}
	return b.popup(popupTranscriptTitle, args...)
}

// popup is a form of ours: our own binary, drawn over the rail's own pane. A
// form that fails keeps the popup open, since the error it printed is the only
// place the failure is reported.
func (b *agentBar) popup(title string, args ...string) (tmux.Command, error) {
	return tmux.PopupSpec{
		Pane: b.pane, Width: agentBarPopupWidth, Height: agentBarPopupHeight, Title: title,
		Argv: append([]string{b.exe}, args...), KeepOnFailure: true,
	}.Command()
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
