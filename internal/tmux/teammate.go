package tmux

import (
	"strconv"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
)

// Teammate is the pane Claude Code opened for a teammate, with what the
// teammate was started as.
type Teammate struct {
	// Pane is the pane id, which the teammate reads from $TMUX_PANE.
	Pane string
	// Agent is the teammate's name, AgentType the agent definition it runs and
	// Team the team it belongs to. Each one is empty when the arguments the
	// teammate was started with did not carry it.
	Agent, AgentType, Team string
}

// claudeStyles are the pane options Claude Code sets on a teammate's pane
// before it starts the agent in it: its own background and its own border,
// with a border format holding the teammate's name.
var claudeStyles = []string{"window-style", "pane-border-style", "pane-active-border-style", "pane-border-format"}

// AdoptTeammate returns the commands that make the pane Claude Code opened for
// a teammate a pane of the workspace.
//
// It labels the pane the way every other pane of ours is labeled, so the
// border and the pickers read the teammate's name, its type and its team, and
// hooks find a pane they know how to update. It keeps the pane on screen when
// the agent exits with a failure, so the reason stays readable. It removes the
// styles Claude Code set on the pane rather than replacing them, which leaves
// the window and the server, both ours, to draw it. It allows the escape
// sequences an agent sends to the outer terminal, as a Claude pane does. And
// it signals the agents channel, so the sidebar draws the new teammate as it
// arrives instead of on a timer.
//
// A target that is not a pane id returns nothing: a pane is never selected by
// name or pattern here, since the id comes from the environment of a process
// Claude Code started.
func AdoptTeammate(t Teammate) Seq {
	if !ValidPaneID(t.Pane) {
		return nil
	}
	set := func(args ...string) Seq { return Cmd("set-option", append([]string{"-p", "-t", t.Pane}, args...)...) }
	seq := set(OptRole, RoleTeammate).Then(set(OptState, "busy"))
	for _, o := range []struct{ name, value string }{
		{OptAgent, AgentOption(t.Agent)},
		{OptAgentType, AgentOption(t.AgentType)},
		{OptTeam, AgentOption(t.Team)},
	} {
		if o.value != "" {
			seq = seq.Then(set(o.name, o.value))
		}
	}
	seq = seq.Then(set("remain-on-exit", "failed")).Then(set(OptPassthrough, "on"))
	for _, name := range claudeStyles {
		seq = seq.Then(Cmd("set-option", "-p", "-u", "-t", t.Pane, name))
	}
	return seq.Then(Cmd("run-shell", "-C", "-t", t.Pane, AgentsSignal))
}

// fmtKeepLayout is the value RememberLayout stores: the arrangement of the
// window, unless a teammate is in it, in which case the value already stored
// is kept. Claude Code tiles the whole window for every teammate it opens, so
// an arrangement read while one is there is its arrangement and not the user's;
// the moment the last teammate is gone, the window is followed again.
var fmtKeepLayout = "#{?#{m:*x*,#{P:#{?#{==:#{" + OptRole + "}," + RoleTeammate + "},x,}}}," +
	"#{" + OptWinLayout + "},#{window_layout}}"

// RememberLayout returns the command that keeps OptWinLayout up to date for
// the window holding target, which is a pane id or a window id. tmux expands
// the value itself (set-option -F), so the arrangement is read and stored in
// one command with nothing to carry between two of them.
func RememberLayout(target string) Seq {
	return Cmd("set-option", "-w", "-t", target, "-F", OptWinLayout, fmtKeepLayout)
}

// TileAgents returns the commands that arrange a window shared by a lead and
// its teammates: the lead in a column of its own, the teammates stacked in
// what is left of the width. Claude Code has just tiled the window for itself,
// with the lead cut down to a third; this is the same arrangement with the
// share the workspace gives its lead.
// A window that carries the agents rail is arranged around it: main-vertical
// gives the leftmost pane the column of its own, which is the rail, so the
// rail is put back to its own width, railWidth cells, and the lead stacks with
// the teammates in what it leaves.
func TileAgents(window, lead, rail string, railWidth int) Seq {
	if !ValidPaneID(lead) {
		return nil
	}
	seq := Cmd("select-layout", "-t", window, "main-vertical")
	if ValidPaneID(rail) {
		return seq.Then(Cmd("resize-pane", "-t", rail, "-x", strconv.Itoa(layout.RailCells(railWidth))))
	}
	return seq.Then(Cmd("resize-pane", "-t", lead, "-x", strconv.Itoa(layout.AgentLeadRatio)+"%"))
}

// BreakOutTeammate returns the commands that move a teammate's pane into a
// window of its own, named after the teammate, and put the window it leaves
// back to the arrangement it had before any agent was opened in it.
//
// The window is not selected: a teammate opens while the user is working in
// the lead, and a pane that moves must not move the user with it. The name is
// prepared the way a drawn value is, since the status line draws it.
func BreakOutTeammate(pane, window, name, remembered string) Seq {
	if !ValidPaneID(pane) || !validWindowID(window) {
		return nil
	}
	// break-pane names the pane to move with -s: its -t is the window the
	// pane moves into, which is a window tmux creates here.
	seq := Cmd("break-pane", "-d", "-s", pane)
	if drawn := AgentOption(name); drawn != "" {
		seq = Cmd("break-pane", "-d", "-n", drawn, "-s", pane)
	}
	// The window the pane left, named by its own id: the pane is in another
	// window by now and no longer names this one.
	if remembered != "" {
		seq = seq.Then(Cmd("select-layout", "-t", window, remembered))
	}
	return seq
}

// AgentsSignal signals the agents channel of the session holding a pane. The
// session id is not known where this is written, so the format is expanded by
// run-shell -C before the command it builds is run. The id goes into a
// single-quoted token, where the characters a session id is made of carry no
// meaning.
var AgentsSignal = "wait-for -S '" + AgentsChannel("#{session_id}") + "'"

// OpenRail returns the command that opens the agents rail of a window beside
// anchor, the window's leftmost pane, and prints the id of the pane it made.
//
// The rail does not take the cursor with it (-d): it opens by itself while the
// user is typing in a pane of their own. Its width, width cells, is given at
// the split, since the rail holds a fixed set of columns rather than a share
// of a window. An anchor that is not a pane id returns nothing.
func OpenRail(anchor string, width int, proc PaneProcess) Command {
	if !ValidPaneID(anchor) {
		return nil
	}
	return append(Command{
		"split-window", "-b", "-h", "-d", "-P", "-F", "#{pane_id}",
		"-l", strconv.Itoa(layout.RailCells(width)), "-t", anchor,
	}, proc.args()...)
}

// AdoptRail returns the commands that make a pane the agents rail: the role
// the views, the tiling and the toggle read it by. The pane keeps tmux's own
// behavior when its program exits, which is how a rail that opened by itself
// takes itself off the screen again.
func AdoptRail(pane string) Seq {
	if !ValidPaneID(pane) {
		return nil
	}
	return Cmd("set-option", "-p", "-t", pane, OptRole, RoleAgents)
}
