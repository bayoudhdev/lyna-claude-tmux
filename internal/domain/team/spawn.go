package team

import "strings"

// Spawn is what Claude Code tells a teammate about itself on the command line
// of the process it starts for it. It is appended to whatever launcher the
// teammate opens through, so a launcher of ours reads the team from the
// arguments it was handed rather than from a file that is still being written.
type Spawn struct {
	// AgentID identifies the teammate inside its session.
	AgentID string
	// Name is the teammate's name, the one the lead addresses it by.
	Name string
	// AgentType is the agent definition it runs, empty for the default one.
	AgentType string
	// Team is the name of the team it belongs to.
	Team string
	// Color is the color Claude Code gave it.
	Color string
	// Model is the model it was started on, empty when it takes the default.
	Model string
}

// ParseSpawn reads what it recognizes out of the arguments Claude Code
// appended to a teammate's launcher. Both spellings are read, "--flag value"
// and "--flag=value", and every other argument is passed over, so a release
// that adds a flag, drops one or renames one still starts its teammates: the
// arguments are the agent's own and are never rewritten from what is read
// here.
func ParseSpawn(args []string) Spawn {
	var s Spawn
	fields := map[string]*string{
		"--agent-id":    &s.AgentID,
		"--agent-name":  &s.Name,
		"--agent-type":  &s.AgentType,
		"--team-name":   &s.Team,
		"--agent-color": &s.Color,
		"--model":       &s.Model,
	}
	for i := 0; i < len(args); i++ {
		name, value, joined := strings.Cut(args[i], "=")
		field, known := fields[name]
		if !known {
			continue
		}
		if !joined {
			// A flag at the end of the line has no value to take, and a flag
			// is never the value of another one: the flags Claude Code passes
			// values to are not all known here, so a value of "--agent-name"
			// belonging to a flag this parser reads past would otherwise be
			// read as the flag it spells.
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				continue
			}
			i++
			value = args[i]
		}
		*field = value
	}
	return s
}
