package tmux

import (
	"strconv"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

// OptRunning is a pane option: the subagents running in that pane, as
// team.RunningEntry writes them. It is kept beside the count in OptSubagents,
// which is what the status line shows, and it is what gives each subagent a
// row of its own.
const OptRunning = "@lt_running"

// fmtRunningAdd appends an entry to the list, and stops appending once the
// list is as long as a pane's subagents ever are. A subagent whose stop hook
// never ran leaves its entry behind, so the limit is what keeps a long session
// from filling the option with entries nothing will remove.
func fmtRunningAdd(entry string) string {
	list := "#{" + OptRunning + "}"
	// A comma ends a branch of a conditional, so the one ending the entry is
	// written as the format escapes it.
	return "#{?#{e|<:#{n:" + OptRunning + "}," + strconv.Itoa(team.MaxRunning) + "}," +
		list + escapeFormatCommas(entry) + "," + list + "}"
}

// fmtRunningRemove cuts one entry out of the list, exactly once, and leaves
// the value alone when the entry is not in it. The entry holds only the
// characters team.RunningEntry allows, none of which mean anything to the
// pattern this expands into.
func fmtRunningRemove(entry string) string {
	return "#{s/" + entry + "//:" + OptRunning + "}"
}

// escapeFormatCommas writes a literal comma the way a conditional branch takes
// one.
func escapeFormatCommas(s string) string {
	var out string
	for _, r := range s {
		if r == ',' {
			out += "#,"
			continue
		}
		out += string(r)
	}
	return out
}

// StartSubagent returns the commands that record a subagent a pane started:
// the count the status line draws goes up, and the subagent joins the list the
// agents view reads its row from.
//
// Both are one set-option -F each, which the server evaluates and applies in
// one step, so the parallel subagents of one agent never lose an update
// between them. A subagent the agent did not name is counted and not listed,
// since an entry that cannot be told from another cannot be removed again.
func StartSubagent(pane, id, agentType string) Seq {
	if !ValidPaneID(pane) {
		return nil
	}
	seq := Cmd("set-option", "-p", "-t", pane, "-F", OptSubagents, fmtSubagentsInc)
	if entry := team.RunningEntry(id, agentType); entry != "" {
		seq = seq.Then(Cmd("set-option", "-p", "-t", pane, "-F", OptRunning, fmtRunningAdd(entry)))
	}
	return seq
}

// StopSubagent returns the commands that take a subagent back off a pane.
func StopSubagent(pane, id, agentType string) Seq {
	if !ValidPaneID(pane) {
		return nil
	}
	seq := Cmd("set-option", "-p", "-t", pane, "-F", OptSubagents, fmtSubagentsDec)
	if entry := team.RunningEntry(id, agentType); entry != "" {
		seq = seq.Then(Cmd("set-option", "-p", "-t", pane, "-F", OptRunning, fmtRunningRemove(entry)))
	}
	return seq
}

// ClearSubagents returns the commands that leave a pane with no subagents at
// all, which is what a session ending in it has.
func ClearSubagents(pane string) Seq {
	if !ValidPaneID(pane) {
		return nil
	}
	return Cmd("set-option", "-p", "-u", "-t", pane, OptSubagents).
		Then(Cmd("set-option", "-p", "-u", "-t", pane, OptRunning))
}

// The arithmetic the server evaluates while it applies the count.
const (
	fmtSubagentsInc = "#{e|+:#{" + OptSubagents + "},1}"
	fmtSubagentsDec = "#{?#{e|>:#{" + OptSubagents + "},0},#{e|-:#{" + OptSubagents + "},1},0}"
)
