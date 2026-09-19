package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// SpawnRequest names the workspace the spawn form runs for.
type SpawnRequest struct {
	Target WindowTarget
}

// SpawnView is what the spawn form is drawn with, and what carries out the
// request it finishes on.
type SpawnView struct {
	Options tui.SpawnOptions
	// Start carries out a finished form. A form the user canceled started
	// nothing and is never passed here.
	Start func(ctx context.Context, res tui.SpawnResult) (SpawnOutcome, error)
}

// SpawnOutcome is what starting an agent did.
type SpawnOutcome struct {
	// Session is the workspace it happened in.
	Session string
	// Lead is the pane the message was typed into, empty for an agent that
	// runs a conversation of its own.
	Lead string
	// Window is the window an agent of its own opened in; its zero value
	// belongs to a request the lead carries out.
	Window WindowResult
}

// ErrSpawn reports a request the workspace cannot carry out.
var ErrSpawn = errors.New("cannot start the agent")

// OpenSpawn prepares the spawn form for the workspace the caller is in: the
// agent definitions the project and the user carry, whether there is a lead to
// ask, and the launch the workspace already uses as the starting answers.
func (s *Server) OpenSpawn(ctx context.Context, h Host, req SpawnRequest) (SpawnView, error) {
	spot, err := s.wsLocate(ctx, h, req.Target)
	if err != nil {
		return SpawnView{}, err
	}
	look, err := tui.ThemeFromConfig(s.Config.UI, h.Getenv)
	if err != nil {
		return SpawnView{}, err
	}
	_, lead, err := s.spawnLead(ctx, spot)
	if err != nil {
		return SpawnView{}, err
	}
	cfg := s.Config.Claude
	return SpawnView{
		Options: tui.SpawnOptions{
			Styles:   tui.NewStyles(look),
			Agents:   claude.AgentDefs(spot.root, xdg.ClaudeHome(h.Getenv, h.Home)),
			Lead:     lead,
			Worktree: cfg.AgentWorktree,
			Model:    cfg.Model,
			Effort:   cfg.Effort,
		},
		Start: func(ctx context.Context, res tui.SpawnResult) (SpawnOutcome, error) {
			return s.startSpawn(ctx, h, spot, res)
		},
	}, nil
}

// spawnLead reads the workspace and returns the pane an ask-the-lead request
// would be typed into, and whether the workspace runs a lead at all.
func (s *Server) spawnLead(ctx context.Context, spot wsSpot) (tmux.Pane, bool, error) {
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession(spot.pane.Session))
	if err != nil {
		return tmux.Pane{}, false, workspaceErr(spot.pane.Session, err)
	}
	lead, ok := spawnLead(panes, spot.pane.Session, spot.pane.ID)
	return lead, ok, nil
}

// spawnLead is the lead of a workspace: the Claude pane the form was opened
// from, then the pane the user is on, then the first one the server lists.
//
// A teammate is never the lead, whatever window it sits in: a teammate is
// asked for by the lead, and asking one to start another is not how a team is
// run.
func spawnLead(panes []tmux.Pane, session, from string) (tmux.Pane, bool) {
	active, first := -1, -1
	for i, p := range panes {
		if p.SessionName != session || p.Role != tmux.RoleClaude || p.Dead {
			continue
		}
		switch {
		case p.ID == from:
			return p, true
		case active < 0 && p.Active && p.WindowActive:
			active = i
		case first < 0:
			first = i
		}
	}
	switch {
	case active >= 0:
		return panes[active], true
	case first >= 0:
		return panes[first], true
	}
	return tmux.Pane{}, false
}

// startSpawn carries out what the form asked for: the lead is typed at, or an
// agent of our own opens in a window of the workspace.
func (s *Server) startSpawn(ctx context.Context, h Host, spot wsSpot, res tui.SpawnResult) (SpawnOutcome, error) {
	if !res.Sent {
		return SpawnOutcome{}, fmt.Errorf("%w: the form started nothing", ErrSpawn)
	}
	if res.Request.Target == tui.SpawnLead {
		return s.askTheLead(ctx, spot, res)
	}
	req := TaskRequest{
		Target:     WindowTarget{Pane: spot.pane.ID},
		Name:       res.Request.Name,
		Prompt:     res.Request.Prompt,
		Agent:      res.Request.Agent,
		Model:      res.Request.Model,
		Effort:     res.Request.Effort,
		NoWorktree: !res.Request.Worktree,
	}
	win, err := s.OpenTask(ctx, h, req)
	if err != nil {
		return SpawnOutcome{}, err
	}
	return SpawnOutcome{Session: win.Session, Window: win}, nil
}

// askTheLead types the message into the lead's pane and submits it, through
// typeLine, which reads the lead again and leaves one waiting on the user
// alone.
//
// The message sent is the message the form showed: nothing reaches an agent
// that the user has not read, so a result whose text is not the text of its
// own request is refused rather than rebuilt.
func (s *Server) askTheLead(ctx context.Context, spot wsSpot, res tui.SpawnResult) (SpawnOutcome, error) {
	name := spot.pane.Session
	if res.Message != tui.SpawnMessage(res.Request) {
		return SpawnOutcome{}, fmt.Errorf("%w: the text shown is not the text that would be sent", ErrSpawn)
	}
	lead, err := s.typeLine(ctx, lineTyping{
		session: name,
		text:    res.Message,
		pick:    leadPick(spot, ErrSpawn),
		refused: ErrSpawn,
		waiting: "the agent of workspace " + name + " is waiting on you; answer it, then ask again",
	})
	if err != nil {
		return SpawnOutcome{}, err
	}
	return SpawnOutcome{Session: name, Lead: lead.ID}, nil
}
