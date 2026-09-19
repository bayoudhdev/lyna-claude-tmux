package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

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

// ErrTasksNoWorkspace reports a task list with no workspace to follow.
var ErrTasksNoWorkspace = errors.New("the task list follows a workspace")

// TasksRequest is one run of the task list.
type TasksRequest struct {
	// Session is the workspace to follow outside tmux; inside a pane the pane
	// says which workspace it belongs to.
	Session string
	// Popup makes q and esc close the list.
	Popup bool
}

// TasksView is everything the task list runs on: how to read the list, how to
// wait for it to change, and the model's own options.
type TasksView struct {
	Options tui.TasksOptions
	// Read takes one reading of the list. fresh is false for a reading that
	// draws what the last one did, which the caller drops: a list with nothing
	// new to show draws nothing. Read is called from one goroutine at a time,
	// which is what the refresh loop guarantees.
	Read func(ctx context.Context) (u tui.TasksUpdate, fresh bool)
	// Signal blocks until an agent of the workspace changes, which is when a
	// hook reports a task created or finished.
	Signal func(ctx context.Context) error
}

// OpenTasks prepares the shared task list of the team the workspace runs.
//
// It follows the workspace the way the agents rail does: the one the pane
// belongs to inside tmux, the one named in the request on the lyna-tmux
// server outside it. It reads the list on the rail's own signal, since the
// hooks that tell the rail a task was created or finished are the ones that
// change the list; the files Claude Code writes are only ever read.
func OpenTasks(ctx context.Context, h Host, req TasksRequest) (TasksView, error) {
	ws, err := followWorkspace(ctx, h, req.Session, ErrTasksNoWorkspace)
	if err != nil {
		return TasksView{}, err
	}
	look, err := tui.ThemeFromConfig(ws.config.UI, h.Getenv)
	if err != nil {
		return TasksView{}, err
	}
	list := &taskList{client: ws.client, session: ws.name, claudeHome: xdg.ClaudeHome(h.Getenv, h.Home)}
	return TasksView{
		Options: tui.TasksOptions{Styles: tui.NewStyles(look), Popup: req.Popup},
		Read:    list.read,
		Signal:  watch.TmuxAgentsSignal(ws.client, ws.target),
	}, nil
}

// followedWorkspace is a workspace a live view follows, and the server it is
// read from.
type followedWorkspace struct {
	client *tmux.Client
	config config.Config
	// name is the workspace, and target what names it to tmux: the pane the
	// view runs in, or the exact session.
	name, target string
}

// followWorkspace finds the workspace a live view follows.
//
// Inside tmux the pane the view runs in names the workspace, and a view with
// no pane, which is a popup on a server that gives it none, follows the
// workspace the request names. Outside tmux the request names a workspace on
// the lyna-tmux server, which has to be running. noWorkspace is the error of a
// view that was told of no workspace at all.
func followWorkspace(ctx context.Context, h Host, name string, noWorkspace error) (followedWorkspace, error) {
	if name != "" {
		if err := session.Validate(name); err != nil {
			return followedWorkspace{}, err
		}
	}
	socket, ok := tmux.SocketFromEnv(h.Getenv("TMUX"))
	if !ok {
		if name == "" {
			return followedWorkspace{}, fmt.Errorf("%w: outside tmux, pass --session with a workspace name", noWorkspace)
		}
		s, err := OpenServer(ctx, h)
		if err != nil {
			return followedWorkspace{}, err
		}
		running, err := s.Client.HasSession(ctx, name)
		if err != nil {
			return followedWorkspace{}, err
		}
		if !running {
			return followedWorkspace{}, fmt.Errorf("%w: %s", ErrNoWorkspace, name)
		}
		return followedWorkspace{client: s.Client, config: s.Config, name: name, target: tmux.ExactSession(name)}, nil
	}
	_, cfg, err := LoadConfig(h)
	if err != nil {
		return followedWorkspace{}, err
	}
	ws := followedWorkspace{
		client: tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: agentBarEnviron(h)}),
		config: cfg,
	}
	switch pane := h.Getenv("TMUX_PANE"); {
	case tmux.ValidPaneID(pane):
		ws.target = pane
		if ws.name, err = ws.client.Display(ctx, pane, "#{session_name}"); err != nil || ws.name == "" {
			return followedWorkspace{}, fmt.Errorf("cannot read the workspace of pane %s: %w", sanitize.Line(pane), err)
		}
	case name != "":
		ws.name, ws.target = name, tmux.ExactSession(name)
	default:
		return followedWorkspace{}, fmt.Errorf("%w: pass --session", noWorkspace)
	}
	return ws, nil
}

// taskList reads the shared task list of the team one workspace runs.
type taskList struct {
	client     *tmux.Client
	session    string
	claudeHome string
	// last is the reading last delivered, and delivered tells whether there
	// is one: the first reading is delivered whatever it holds.
	last      tui.TasksUpdate
	delivered bool
}

// read takes a reading and says whether it draws anything the last one did
// not.
func (l *taskList) read(ctx context.Context) (tui.TasksUpdate, bool) {
	u := l.take(ctx)
	fresh := !l.delivered || !sameTasks(l.last, u)
	l.last, l.delivered = u, true
	return u, fresh
}

// take takes one reading: the panes of the server, which say which team the
// workspace runs, and the list that team shares.
func (l *taskList) take(ctx context.Context) tui.TasksUpdate {
	ctx, cancel := context.WithTimeout(ctx, agentBarTimeout)
	defer cancel()
	panes, err := l.client.ListPanes(ctx, "")
	if err != nil {
		return tui.TasksUpdate{Err: err}
	}
	name := agentBarTeam(team.Input{Session: l.session, Panes: agentBarPanes(panes)})
	if name == "" {
		return tui.TasksUpdate{}
	}
	tasks, err := claude.ReadTasks(l.claudeHome, name)
	if err != nil {
		return tui.TasksUpdate{Team: name, Err: err}
	}
	return tui.TasksUpdate{Team: name, Tasks: tasks}
}

// sameTasks reports two readings that draw the same list: the same team, the
// same tasks field for field, and the same failure. A list read as missing
// and one read as empty are the same list.
func sameTasks(a, b tui.TasksUpdate) bool {
	return a.Team == b.Team && errText(a.Err) == errText(b.Err) && slices.EqualFunc(a.Tasks, b.Tasks, sameTask)
}

func sameTask(a, b team.Task) bool {
	return a.ID == b.ID && a.Subject == b.Subject && a.Description == b.Description &&
		a.ActiveForm == b.ActiveForm && a.Owner == b.Owner && a.Status == b.Status &&
		slices.Equal(a.Blocks, b.Blocks) && slices.Equal(a.BlockedBy, b.BlockedBy)
}

// errText is what a failure says, empty for none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
