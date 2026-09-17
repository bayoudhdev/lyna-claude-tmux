package team

import "strings"

// The shape of the running list a pane carries: one entry per subagent, an
// entry being the subagent's identifier, a separator and its type, and every
// entry ending in a separator of its own so an entry is added by appending and
// removed by cutting the exact text out, both of which a tmux format does.
const (
	// runningPair separates a subagent's identifier from its type. It is not
	// ':', which a format substitution reads as the end of its pattern.
	runningPair = "="
	// runningSep ends every entry.
	runningSep = ","
)

// MaxRunningPart bounds the identifier and the type of one entry.
const MaxRunningPart = 32

// MaxRunning bounds the whole list. Past this a pane has lost track of its
// subagents, which happens when Claude Code stops without the stop hook
// running, and the list stops growing rather than filling the option with
// entries nothing will remove.
const MaxRunning = 512

// RunningEntry renders one subagent of the running list.
//
// The identifier and the type come from the agent, so both are reduced to the
// characters an entry is made of: letters, digits, underscore and dash, cut to
// MaxRunningPart. An identifier left with nothing renders no entry, because an
// entry that cannot be told apart from another cannot be removed again.
func RunningEntry(id, agentType string) string {
	id = runningPart(id)
	if id == "" {
		return ""
	}
	return id + runningPair + runningPart(agentType) + runningSep
}

// runningPart keeps the characters an entry is made of.
func runningPart(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !ok {
			continue
		}
		if n == MaxRunningPart {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// ParseRunning reads the running list of a pane. Anything that is not an entry
// is skipped: the list is written by formats the server evaluates, so a value
// that was cut short by a limit or by a removal is read for the entries it
// still holds rather than refused whole.
func ParseRunning(value string) []Subagent {
	var out []Subagent
	for _, entry := range strings.Split(value, runningSep) {
		id, agentType, ok := strings.Cut(entry, runningPair)
		if !ok || runningPart(id) != id || id == "" || runningPart(agentType) != agentType {
			continue
		}
		out = append(out, Subagent{ID: id, Type: agentType})
	}
	return out
}
