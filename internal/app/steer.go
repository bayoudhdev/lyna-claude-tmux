package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// SteerRequest names the agent a message or a stop is for, and the workspace
// it has to belong to.
type SteerRequest struct {
	// Target is the workspace, resolved the way spawn resolves it: the pane
	// the command runs in, or the workspace named on the lyna-tmux server.
	Target WindowTarget
	// To is the pane id of the agent.
	To string
}

// SteerOutcome is what a message or a stop did.
type SteerOutcome struct {
	// Session is the workspace it happened in, and Agent the agent steered.
	Session, Agent string
	// Pane is the pane that was typed into: the agent's own for a message, the
	// lead's for a teammate the lead was asked to shut down. It is empty for a
	// teammate stopped here.
	Pane string
	// Stopped is set when the teammate's pane was closed.
	Stopped bool
}

// ErrMessage reports a message the workspace will not type.
var ErrMessage = errors.New("cannot send the message")

// ErrStop reports a teammate the workspace will not stop.
var ErrStop = errors.New("cannot stop the teammate")

// MessageView is what the message form is drawn with, and what sends the
// message it finishes on.
type MessageView struct {
	Options tui.MessageOptions
	// Send types a finished form's message. A form the user canceled sent
	// nothing and is never passed here.
	Send func(ctx context.Context, res tui.MessageResult) (SteerOutcome, error)
}

// StopView is what the stop form is drawn with, and what stops the teammate
// once the form is finished.
type StopView struct {
	Options tui.StopOptions
	// Stop carries out a finished form. A form the user canceled stopped
	// nothing and is never passed here.
	Stop func(ctx context.Context, res tui.StopResult) (SteerOutcome, error)
}

// OpenMessage prepares the form that types a message into an agent of the
// workspace the caller is in: its lead, or one of its teammates.
func (s *Server) OpenMessage(ctx context.Context, h Host, req SteerRequest) (MessageView, error) {
	spot, panes, err := s.steerLocate(ctx, h, req, ErrMessage)
	if err != nil {
		return MessageView{}, err
	}
	agent, err := messageTarget(panes, spot.pane.Session, req.To)
	if err != nil {
		return MessageView{}, err
	}
	look, err := tui.ThemeFromConfig(s.Config.UI, h.Getenv)
	if err != nil {
		return MessageView{}, err
	}
	return MessageView{
		Options: tui.MessageOptions{Styles: tui.NewStyles(look), Agent: agentName(agent), Workspace: agent.SessionName},
		Send: func(ctx context.Context, res tui.MessageResult) (SteerOutcome, error) {
			return s.sendMessage(ctx, agent, res)
		},
	}, nil
}

// OpenStop prepares the form that stops a teammate of the workspace the caller
// is in, and says whether there is a lead to ask to do it.
func (s *Server) OpenStop(ctx context.Context, h Host, req SteerRequest) (StopView, error) {
	spot, panes, err := s.steerLocate(ctx, h, req, ErrStop)
	if err != nil {
		return StopView{}, err
	}
	mate, err := stopTarget(panes, spot.pane.Session, req.To)
	if err != nil {
		return StopView{}, err
	}
	look, err := tui.ThemeFromConfig(s.Config.UI, h.Getenv)
	if err != nil {
		return StopView{}, err
	}
	_, lead := spawnLead(panes, spot.pane.Session, spot.pane.ID)
	return StopView{
		Options: tui.StopOptions{
			Styles: tui.NewStyles(look), Teammate: agentName(mate), Workspace: mate.SessionName, Lead: lead,
		},
		Stop: func(ctx context.Context, res tui.StopResult) (SteerOutcome, error) {
			return s.stopTeammate(ctx, spot, mate, res)
		},
	}, nil
}

// steerLocate resolves the workspace of a request and reads its panes. A
// target that is not a pane id is refused before anything is asked of the
// server: a name or a pattern is resolved by tmux against whatever runs now.
func (s *Server) steerLocate(ctx context.Context, h Host, req SteerRequest, refused error) (wsSpot, []tmux.Pane, error) {
	if !tmux.ValidPaneID(req.To) {
		return wsSpot{}, nil, fmt.Errorf("%w: %q is not a pane id such as %%3", refused, sanitize.Line(req.To))
	}
	spot, err := s.wsLocate(ctx, h, req.Target)
	if err != nil {
		return wsSpot{}, nil, err
	}
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession(spot.pane.Session))
	if err != nil {
		return wsSpot{}, nil, workspaceErr(spot.pane.Session, err)
	}
	return spot, panes, nil
}

// agentName is what the agent of a pane is called, the way the rail names it
// from the pane alone: the name it was spawned under, the window a teammate
// opened in when it carries none, and the workspace for its lead. It is
// prepared for the terminal, since every use of it ends up drawn there.
func agentName(p tmux.Pane) string {
	switch {
	case p.Agent != "":
		return sanitize.Line(p.Agent)
	case p.Role == tmux.RoleTeammate:
		return sanitize.Line(p.WindowName)
	}
	return sanitize.Line(p.SessionName)
}

// findPane is the pane of that id in a reading.
func findPane(panes []tmux.Pane, id string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.ID == id {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

// messageTarget is the agent a message is for: pane id of workspace session,
// checked by messageable.
func messageTarget(panes []tmux.Pane, session, id string) (tmux.Pane, error) {
	p, ok := findPane(panes, id)
	if !ok || p.SessionName != session {
		return tmux.Pane{}, fmt.Errorf("%w: %s is not a pane of workspace %s", ErrMessage, id, session)
	}
	return p, messageable(p)
}

// messageable refuses a pane that takes no message. Only an agent reads a line
// typed into its pane as a message, the lead or a teammate; anything else
// takes it as input of its own, and a shell runs it as a command. An agent
// that stopped reads nothing at all.
func messageable(p tmux.Pane) error {
	switch {
	case p.Role != tmux.RoleClaude && p.Role != tmux.RoleTeammate:
		return fmt.Errorf("%w: %s runs no agent; only the lead and its teammates take messages", ErrMessage, p.ID)
	case p.Dead:
		return fmt.Errorf("%w: %s has stopped; its pane shows how it ended", ErrMessage, agentName(p))
	}
	return nil
}

// stopTarget is the teammate a stop is for: pane id of workspace session,
// checked by stoppable.
func stopTarget(panes []tmux.Pane, session, id string) (tmux.Pane, error) {
	p, ok := findPane(panes, id)
	if !ok || p.SessionName != session {
		return tmux.Pane{}, fmt.Errorf("%w: %s is not a pane of workspace %s", ErrStop, id, session)
	}
	return p, stoppable(p)
}

// stoppable refuses a pane that is not a teammate. The lead is the
// conversation the workspace was opened for, and closing it is the user's to
// do in its own pane; every other pane runs no agent to stop. A teammate is
// stopped by its name, so one that carries none is refused too.
func stoppable(p tmux.Pane) error {
	switch {
	case p.Role == tmux.RoleClaude:
		return fmt.Errorf("%w: %s is the lead of workspace %s; only a teammate is stopped here", ErrStop, agentName(p), p.SessionName)
	case p.Role != tmux.RoleTeammate:
		return fmt.Errorf("%w: %s runs no teammate", ErrStop, p.ID)
	case agentName(p) == "":
		return fmt.Errorf("%w: the teammate in %s carries no name to stop it by", ErrStop, p.ID)
	}
	return nil
}

// sameAgent finds, in a new reading, the agent a form was opened for, and
// refuses a pane that is no longer that agent: gone, moved to another
// workspace, running something else or under another name, or refused by
// check now. The form was on screen while the workspace kept running.
func sameAgent(panes []tmux.Pane, was tmux.Pane, check func(tmux.Pane) error, refused error) (tmux.Pane, error) {
	name := agentName(was)
	now, ok := findPane(panes, was.ID)
	switch {
	case !ok:
		return tmux.Pane{}, fmt.Errorf("%w: %s is gone from workspace %s", refused, name, was.SessionName)
	case now.SessionName != was.SessionName:
		return tmux.Pane{}, fmt.Errorf("%w: %s moved to workspace %s", refused, name, sanitize.Line(now.SessionName))
	case now.Role != was.Role:
		return tmux.Pane{}, fmt.Errorf("%w: the pane of %s runs something else now", refused, name)
	case agentName(now) != name:
		return tmux.Pane{}, fmt.Errorf("%w: the pane of %s runs %s now", refused, name, agentName(now))
	}
	if err := check(now); err != nil {
		return tmux.Pane{}, err
	}
	return now, nil
}

// sendMessage types a finished message form into the agent it was opened for.
func (s *Server) sendMessage(ctx context.Context, was tmux.Pane, res tui.MessageResult) (SteerOutcome, error) {
	if !res.Sent {
		return SteerOutcome{}, fmt.Errorf("%w: the form sent nothing", ErrMessage)
	}
	name := agentName(was)
	p, err := s.typeLine(ctx, lineTyping{
		session: was.SessionName,
		text:    res.Text,
		pick: func(panes []tmux.Pane) (tmux.Pane, error) {
			return sameAgent(panes, was, messageable, ErrMessage)
		},
		refused: ErrMessage,
		waiting: name + " is waiting on you; answer it in its pane, then send again",
	})
	if err != nil {
		return SteerOutcome{}, err
	}
	return SteerOutcome{Session: was.SessionName, Agent: name, Pane: p.ID}, nil
}

// stopTeammate carries out a finished stop form: the lead is asked to shut the
// teammate down, or the teammate's pane is closed.
func (s *Server) stopTeammate(ctx context.Context, spot wsSpot, was tmux.Pane, res tui.StopResult) (SteerOutcome, error) {
	if !res.Sent {
		return SteerOutcome{}, fmt.Errorf("%w: the form stopped nothing", ErrStop)
	}
	name := agentName(was)
	switch res.Way {
	case tui.StopAskLead:
		if res.Message != tui.StopMessage(name) {
			return SteerOutcome{}, fmt.Errorf("%w: the text shown is not the text that would be sent", ErrStop)
		}
		lead, err := s.typeLine(ctx, lineTyping{
			session: was.SessionName,
			text:    res.Message,
			pick: func(panes []tmux.Pane) (tmux.Pane, error) {
				if _, err := sameAgent(panes, was, stoppable, ErrStop); err != nil {
					return tmux.Pane{}, err
				}
				return leadPick(spot, ErrStop)(panes)
			},
			refused: ErrStop,
			waiting: fmt.Sprintf("the lead of workspace %s is waiting on you; answer it, then ask again", was.SessionName),
		})
		if err != nil {
			return SteerOutcome{}, err
		}
		return SteerOutcome{Session: was.SessionName, Agent: name, Pane: lead.ID}, nil
	case tui.StopNow:
		// The name typed out is what makes closing a pane a decision rather
		// than a key pressed on the wrong row.
		if name == "" || res.Typed != name {
			return SteerOutcome{}, fmt.Errorf("%w: the name typed is not %s; nothing was stopped", ErrStop, name)
		}
		return s.closeTeammate(ctx, was)
	}
	return SteerOutcome{}, fmt.Errorf("%w: the form chose no way to stop it", ErrStop)
}

// closeTeammate closes the pane of the teammate a form was opened for. The
// workspace is read again right before, and a pane that is no longer that
// teammate is left alone: a pane id is never reused by a running server, but
// what runs in it and what it is called can change while the form is open.
func (s *Server) closeTeammate(ctx context.Context, was tmux.Pane) (SteerOutcome, error) {
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession(was.SessionName))
	if err != nil {
		return SteerOutcome{}, workspaceErr(was.SessionName, err)
	}
	now, err := sameAgent(panes, was, stoppable, ErrStop)
	if err != nil {
		return SteerOutcome{}, err
	}
	cmds := tmux.StopPane(now.ID)
	if len(cmds) == 0 {
		return SteerOutcome{}, fmt.Errorf("%w: %s is not a pane this workspace can close", ErrStop, now.ID)
	}
	if _, err := s.Client.Batch(ctx, cmds...); err != nil {
		return SteerOutcome{}, workspaceErr(was.SessionName, err)
	}
	return SteerOutcome{Session: was.SessionName, Agent: agentName(was), Stopped: true}, nil
}

// lineTyping is one line typed into an agent's pane: the text, the pane it
// goes to, and how a refusal is worded.
type lineTyping struct {
	session, text string
	// pick finds the pane in a reading of the workspace taken just before the
	// line is typed, or says why there is none to type into.
	pick func(panes []tmux.Pane) (tmux.Pane, error)
	// refused is the error every refusal wraps, and waiting what is said about
	// an agent waiting on the user.
	refused error
	waiting string
}

// typeLine types one line into the pane t picks and submits it. It is the one
// way anything the user asked for is typed at an agent: a spawn the lead
// carries out, a message, and a teammate the lead is asked to shut down.
//
// The line typed is the line given, and nothing else. PasteLine types one line
// of printable text, so text it would change on the way is refused rather than
// typed as something the user did not read. The workspace is read again
// because the form that asked for this was on screen while the workspace kept
// running, and an agent waiting on the user is left alone: it is showing a
// question or a permission prompt, and the Enter that submits the line would
// answer it.
func (s *Server) typeLine(ctx context.Context, t lineTyping) (tmux.Pane, error) {
	if t.text == "" || sanitize.Line(t.text) != t.text {
		return tmux.Pane{}, fmt.Errorf("%w: the text shown is not the text that would be sent", t.refused)
	}
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession(t.session))
	if err != nil {
		return tmux.Pane{}, workspaceErr(t.session, err)
	}
	p, err := t.pick(panes)
	if err != nil {
		return tmux.Pane{}, err
	}
	if p.State == hook.StateWaiting {
		return tmux.Pane{}, fmt.Errorf("%w: %s", t.refused, t.waiting)
	}
	cmds := tmux.PasteLine(p.ID, t.text)
	if len(cmds) == 0 {
		return tmux.Pane{}, fmt.Errorf("%w: %s is not a pane this workspace can type into", t.refused, p.ID)
	}
	if _, err := s.Client.Batch(ctx, cmds...); err != nil {
		return tmux.Pane{}, workspaceErr(t.session, err)
	}
	return p, nil
}

// leadPick finds the lead a request is typed at in a reading of the workspace,
// as spawnLead finds it.
func leadPick(spot wsSpot, refused error) func([]tmux.Pane) (tmux.Pane, error) {
	return func(panes []tmux.Pane) (tmux.Pane, error) {
		lead, ok := spawnLead(panes, spot.pane.Session, spot.pane.ID)
		if !ok {
			return tmux.Pane{}, fmt.Errorf("%w: workspace %s runs no agent to ask", refused, spot.pane.Session)
		}
		return lead, nil
	}
}
