package tmux

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

// AgentsSignal signals the agents channel of the session holding a pane. The
// session id is not known where this is written, so the format is expanded by
// run-shell -C before the command it builds is run. The id goes into a
// single-quoted token, where the characters a session id is made of carry no
// meaning.
var AgentsSignal = "wait-for -S '" + AgentsChannel("#{session_id}") + "'"
