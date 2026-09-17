package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// teammateTimeout bounds the tmux work done before the agent starts. A wedged
// server delays a teammate by this much and never more: the agent is what the
// user is waiting for, the label is not.
const teammateTimeout = 3 * time.Second

// TeammateRequest is one run of the launcher Claude Code opens a teammate
// through: the agent to run, and the arguments Claude Code appended to it.
type TeammateRequest struct {
	// ClaudePath is the agent to become, absolute, resolved by the workspace
	// when it wrote the launcher rather than looked up here.
	ClaudePath string
	// Args are Claude Code's own arguments for this teammate, passed on
	// unchanged whatever is read from them.
	Args []string
	// Tmux builds a client for the server the pane belongs to; nil builds one
	// for the host's tmux binary.
	Tmux func(tmux.Socket) *tmux.Client
	// AgentPanes is workspace.agent_panes: how many teammates share the lead's
	// window before the next one opens as a window of its own.
	AgentPanes int
	// Sidebar is ui.agents_sidebar: whether a workspace opens the agents rail
	// by itself when the first agent arrives in it.
	Sidebar string
	// LogPath is the diagnostic log; empty logs nothing.
	LogPath string
	// Now timestamps log lines; nil uses the clock.
	Now func() time.Time
}

// TeammateRun is the agent the launcher becomes.
type TeammateRun struct {
	// Path is the program to execute and Argv its arguments, the program
	// itself first.
	Path string
	Argv []string
	// Env is the environment to execute it with.
	Env []string
}

// ErrTeammateAgent reports a launcher that was not told which agent to run.
var ErrTeammateAgent = errors.New("teammate: the agent to run is named with --claude and must be an absolute path")

// Teammate takes over the pane Claude Code opened for a teammate and returns
// the agent to become in it.
//
// Nothing here can keep a teammate from starting. The pane is labeled when it
// is a pane of a workspace of ours and the server answers; every other case,
// from a teammate opened outside a workspace to a tmux that does not reply,
// leaves the pane as Claude Code prepared it, writes the reason to the
// diagnostic log and returns the same agent with the same arguments. The one
// error is a launcher that names no agent, which is a launcher lyna-tmux did
// not write.
func Teammate(ctx context.Context, h Host, req TeammateRequest) (TeammateRun, error) {
	if !filepath.IsAbs(req.ClaudePath) || strings.ContainsAny(req.ClaudePath, "\n\x00") {
		return TeammateRun{}, fmt.Errorf("%w (got %q)", ErrTeammateAgent, sanitize.Line(req.ClaudePath))
	}
	env := h.Environ
	ws, err := adoptTeammatePane(ctx, h, req)
	if err != nil {
		hook.Log(req.LogPath, req.now(), "teammate", "the pane keeps the look Claude Code gave it: %v", err)
	} else {
		// The teammate is a pane of the workspace, so its own hooks, status
		// line and commands reach the server the way the lead's do.
		env = withEnv(env,
			session.EnvManaged+"=1",
			session.EnvSocket+"="+ws.socket,
			session.EnvSession+"="+ws.session,
		)
	}
	return TeammateRun{
		Path: req.ClaudePath,
		Argv: append([]string{req.ClaudePath}, req.Args...),
		Env:  env,
	}, nil
}

// teammateWorkspace is what the pane answered about the workspace it is in.
type teammateWorkspace struct {
	socket  string
	session string
}

// teammatePaneFields are what one display-message asks about the pane the
// teammate runs in: whether it is in a workspace of ours, the workspace, the
// window and its size, and the arrangement that window has without agents.
var teammatePaneFields = []string{
	"#{" + tmux.OptManaged + "}",
	"#{session_name}",
	"#{window_id}",
	"#{window_width}",
	"#{window_height}",
	"#{" + tmux.OptWinLayout + "}",
}

// adoptTeammatePane labels the pane the teammate runs in, and returns the
// workspace it belongs to.
func adoptTeammatePane(ctx context.Context, h Host, req TeammateRequest) (teammateWorkspace, error) {
	pane := h.Getenv("TMUX_PANE")
	socket, inTmux := tmux.SocketFromEnv(h.Getenv("TMUX"))
	if !inTmux || !tmux.ValidPaneID(pane) {
		return teammateWorkspace{}, fmt.Errorf("no pane of a workspace to label (TMUX_PANE=%q)", sanitize.Line(pane))
	}
	// The server Claude Code builds for itself when it is not already inside
	// tmux is never a target: lyna-tmux sends no command to a server it did
	// not create, not even one that reads.
	if strings.HasPrefix(filepath.Base(socket.Path), team.SwarmSocket) {
		return teammateWorkspace{}, fmt.Errorf("the team runs on the agent's own server (%s)", sanitize.Line(socket.Path))
	}
	client := req.tmux(h, socket)
	ctx, cancel := context.WithTimeout(ctx, teammateTimeout)
	defer cancel()
	out, err := client.Display(ctx, pane, tmux.FieldSep(teammatePaneFields...))
	if err != nil {
		return teammateWorkspace{}, err
	}
	fields := tmux.SplitFields(out)
	if len(fields) != len(teammatePaneFields) || fields[0] != "1" {
		return teammateWorkspace{}, errors.New("the pane is not in a workspace of ours")
	}
	spawn := team.ParseSpawn(req.Args)
	cmds := tmux.AdoptTeammate(tmux.Teammate{
		Pane: pane, Agent: spawn.Name, AgentType: spawn.AgentType, Team: spawn.Team,
	})
	if _, err := client.Batch(ctx, cmds...); err != nil {
		return teammateWorkspace{}, err
	}
	ws := teammateWorkspace{socket: socket.Path, session: fields[1]}
	// The pane is ours from here on: where it belongs is a question of its
	// own, and a window that cannot be arranged is not a teammate that failed
	// to open.
	place := teammatePlace{
		pane: pane, window: fields[2], remembered: fields[5], agent: spawn.Name,
		size: layout.AgentWindow{Width: cells(fields[3]), Height: cells(fields[4]), Max: req.AgentPanes},
		rail: railProcess(h.Exe, req.Sidebar),
	}
	if err := placeTeammatePane(ctx, client, place); err != nil {
		hook.Log(req.LogPath, req.now(), "teammate", "the pane stays where Claude Code opened it: %v", err)
	}
	return ws, nil
}

// teammatePlace is one teammate's pane and the window it was opened in.
type teammatePlace struct {
	// pane is the teammate's pane, window the window it was opened in,
	// remembered that window's arrangement without agents, and agent the name
	// a window of its own takes.
	pane, window, remembered, agent string
	// size is the window as the policy reads it, with its panes still to count.
	size layout.AgentWindow
	// rail is the agents rail to open in the window when it carries none, or
	// the zero process when the workspace opens none by itself.
	rail tmux.PaneProcess
}

// railProcess is the rail a workspace opens by itself when the first agent
// arrives in it, or the zero process when its configuration opens none. Such a
// rail takes itself off the screen again once the agents it opened for are
// gone, which is what makes the mode a mode rather than a pane left to close
// by hand.
func railProcess(exe, sidebar string) tmux.PaneProcess {
	if sidebar != config.SidebarAuto || exe == "" {
		return tmux.PaneProcess{}
	}
	return tmux.PaneProcess{Argv: []string{exe, "agents", "--rail", "--auto"}}
}

// placeTeammatePane applies the pane policy: the teammate keeps its place
// beside the lead, with the window tiled for the two of them, or it moves to a
// window of its own and the window it leaves goes back to the arrangement it
// had before any agent was opened in it.
func placeTeammatePane(ctx context.Context, client *tmux.Client, p teammatePlace) error {
	panes, err := client.ListPanes(ctx, p.window)
	if err != nil {
		return err
	}
	lead, rail, anchor, w := "", "", "", p.size
	for _, pane := range panes {
		if pane.WindowID != p.window {
			continue
		}
		if anchor == "" {
			// tmux numbers the panes of a window by where they are on the
			// screen, so the first one is the one a rail opens to the left of.
			anchor = pane.ID
		}
		switch pane.Role {
		case tmux.RoleTeammate:
			w.Teammates++
		case tmux.RoleClaude:
			if lead == "" {
				lead = pane.ID
			}
		case tmux.RoleAgents:
			// The rail is ours: it keeps its column and the agents share what
			// it leaves, rather than the window being the user's because of it.
			if rail == "" {
				rail, w.Rail = pane.ID, pane.Width
			}
		case "":
			// A pane with no role of ours is one Claude Code has just opened
			// for another teammate, which has not taken it over yet. It is not
			// a pane of the user's and it is not counted as one.
		default:
			// A shell, the changes view, a review: the window is the user's.
			w.Others++
		}
	}
	// The rail opens with the first agent of a workspace that asks for one,
	// wherever that agent then goes: a rail that cannot be opened is a rail the
	// window has none of, never a teammate that failed to be placed.
	var railErr error
	if rail == "" && len(p.rail.Argv) > 0 {
		if rail, railErr = openRail(ctx, client, anchor, p.rail); rail != "" {
			w.Rail = layout.RailWidth
		}
	}
	if layout.PlaceAgent(w) == layout.PlaceHere {
		_, err = client.Batch(ctx, tmux.TileAgents(p.window, lead, rail)...)
		return errors.Join(railErr, err)
	}
	_, err = client.Batch(ctx, tmux.BreakOutTeammate(p.pane, p.window, p.agent, p.remembered)...)
	return errors.Join(railErr, err)
}

// openRail opens the agents rail of a window beside its leftmost pane and
// makes it a pane of ours. It returns the rail, which is empty when none was
// opened.
func openRail(ctx context.Context, client *tmux.Client, anchor string, proc tmux.PaneProcess) (string, error) {
	cmd := tmux.OpenRail(anchor, proc)
	if len(cmd) == 0 {
		return "", fmt.Errorf("no pane to open the agents rail beside (%q)", sanitize.Line(anchor))
	}
	out, err := client.Run(ctx, cmd[0], cmd[1:]...)
	if err != nil {
		return "", fmt.Errorf("open the agents rail: %w", err)
	}
	pane := strings.TrimSpace(out)
	if _, err := client.Batch(ctx, tmux.AdoptRail(pane)...); err != nil {
		return pane, fmt.Errorf("label the agents rail: %w", err)
	}
	return pane, nil
}

func (req TeammateRequest) tmux(h Host, socket tmux.Socket) *tmux.Client {
	if req.Tmux != nil {
		return req.Tmux(socket)
	}
	return tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: ServerEnviron(h.Environ)})
}

func (req TeammateRequest) now() time.Time {
	if req.Now == nil {
		return time.Now()
	}
	return req.Now()
}

// cells reads a window size from tmux. A field that is not a number is a size
// nobody measured, which the policy decides nothing from.
func cells(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// withEnv returns environ with each assignment set: one that names a variable
// already there replaces it in place, so a variable is never given twice and
// the order the process was started with is kept.
func withEnv(environ []string, assignments ...string) []string {
	out := slices.Clone(environ)
	for _, kv := range assignments {
		key, _, _ := strings.Cut(kv, "=")
		prefix := key + "="
		replaced := false
		for i, existing := range out {
			if strings.HasPrefix(existing, prefix) {
				out[i], replaced = kv, true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}
