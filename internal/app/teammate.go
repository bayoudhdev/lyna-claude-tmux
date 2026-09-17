package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	out, err := client.Display(ctx, pane, tmux.FieldSep("#{"+tmux.OptManaged+"}", "#{session_name}"))
	if err != nil {
		return teammateWorkspace{}, err
	}
	fields := tmux.SplitFields(out)
	if len(fields) != 2 || fields[0] != "1" {
		return teammateWorkspace{}, errors.New("the pane is not in a workspace of ours")
	}
	spawn := team.ParseSpawn(req.Args)
	cmds := tmux.AdoptTeammate(tmux.Teammate{
		Pane: pane, Agent: spawn.Name, AgentType: spawn.AgentType, Team: spawn.Team,
	})
	if _, err := client.Batch(ctx, cmds...); err != nil {
		return teammateWorkspace{}, err
	}
	return teammateWorkspace{socket: socket.Path, session: fields[1]}, nil
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
