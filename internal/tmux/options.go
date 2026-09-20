package tmux

import "strings"

// User options lyna-tmux stores on its tmux objects. They carry live state from
// hooks to the status line and pickers without any extra process or file.
const (
	OptState     = "@lt_state"      // pane: busy | waiting | idle
	OptRole      = "@lt_role"       // pane: claude | teammate | shell | changes | scratch
	OptSubagents = "@lt_subagents"  // pane: number of running subagents
	OptManaged   = "@lt_managed"    // session: "1" when created by lyna-tmux
	OptProject   = "@lt_project"    // session: project root directory
	OptSandbox   = "@lt_sandbox"    // session: sandbox profile name
	OptIsolation = "@lt_isolation"  // session: sandbox isolation level
	OptLayout    = "@lt_layout"     // session: layout name
	OptBranch    = "@lt_branch"     // session: git branch of the project, set by hooks
	OptOrigin    = "@lt_origin"     // session: window id a popup session was launched from
	OptParent    = "@lt_parent"     // global: client that last opened the agents picker
	OptConfHash  = "@lt_conf"       // global: fingerprint of the loaded generated configuration
	OptSettings  = "@lt_settings"   // pane: per-launch Claude settings file of a Claude pane
	OptAgent     = "@lt_agent"      // pane: name of the teammate running in it
	OptAgentType = "@lt_agent_type" // pane: agent type of that teammate
	OptTeam      = "@lt_team"       // pane: team that teammate belongs to
	// OptTranscript is a pane option: the transcript Claude Code writes the
	// session of the pane's agent to, as its hooks named it. It is stored as
	// the path itself, never drawn, so it is not escaped for drawing.
	OptTranscript = "@lt_transcript"

	// Names shared with the tmux plugin this project derives from, used when
	// lyna-tmux runs inside the user's own tmux server so existing sessions and
	// configuration keep working.
	OptClaudeOrigin = "@claude_origin"
	OptClaudeParent = "@claude_parent"
)

// OptWinLayout is a window option: the arrangement that window has while no
// teammate is in it, which is the arrangement a teammate leaving it restores.
// It is a window option rather than a session one because every window of a
// workspace has an arrangement of its own.
const OptWinLayout = "@lt_wlayout"

// OptPassthrough is the tmux option that lets a program in a pane send an
// escape sequence straight to the outer terminal. The generated configuration
// turns it off for the server; a Claude pane turns it back on for itself,
// which is what carries the agent's desktop notifications and its progress
// bar out of tmux without giving every shell pane the same channel.
const OptPassthrough = "allow-passthrough" //nolint:gosec // G101: a tmux option name, not a credential

// MaxBranchRunes bounds the branch name drawn in the status line.
const MaxBranchRunes = 32

// MaxAgentRunes bounds the agent name drawn on a pane border, which has far
// less room than the status line.
const MaxAgentRunes = 24

// BranchOption prepares a git branch name for the @lt_branch option: control
// and escape characters removed, shortened to MaxBranchRunes with an ellipsis,
// and '#'-escaped because the status line draws expanded values with style
// markup (DrawEscape). strftime runs on the template before values are inserted, so '%'
// needs no escaping.
func BranchOption(branch string) string {
	return drawnOption(branch, MaxBranchRunes)
}

// AgentOption prepares an agent name for the @lt_agent option. The name comes
// from whoever started the agent, and the border draws the value expanded, so
// it is prepared exactly as a branch name is.
func AgentOption(name string) string {
	return drawnOption(name, MaxAgentRunes)
}

// drawnOption prepares a value for an option the status line or a pane border
// draws: control and escape characters removed, shortened to limit runes with an
// ellipsis, and '#'-escaped for the markup a drawn value goes through.
func drawnOption(value string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		if n == limit {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
		n++
	}
	return DrawEscape(b.String())
}

// ChangesChannel is the tmux wait-for channel a hook signals after Claude
// edits files, and the changes pane waits on. It is keyed on the session id
// ("$0", "$3", ...) rather than the session name: tmux rewrites some names on
// creation (a "$" gains a backslash on 3.3 and 3.4) and escapes them again
// when a format conditional expands them, so a hook and a waiter built from
// the same name can end up on two different channels. An id is "$" plus
// digits, survives both, and stays the same when the session is renamed.
func ChangesChannel(sessionID string) string {
	return "lt-changes-" + sessionID
}

// AgentsChannel is the tmux wait-for channel a hook signals when the agents of
// a session change: a subagent started or finished, a teammate ran out of work,
// the shared task list moved. The agents sidebar waits on it, so it redraws on
// the agent's own events and never on a timer. It is keyed on the session id
// for the reasons ChangesChannel is.
func AgentsChannel(sessionID string) string {
	return "lt-agents-" + sessionID
}

// Pane roles.
const (
	RoleClaude = "claude"
	RoleShell  = "shell"
	// RoleTeammate is a pane running a teammate of a Claude Code team. It is a
	// Claude pane in everything but its label: it carries the teammate's name,
	// its agent type and the team it belongs to, which is what the border and
	// the sidebar show.
	RoleTeammate = "teammate"
	RoleChanges  = "changes"
	RoleScratch  = "scratch"
	// RoleAgents is the rail: the pane that draws every agent of the workspace.
	// It is the one pane of ours that is never an agent itself.
	RoleAgents = "agents"
	// RoleGit is the git workstation: the repository of the workspace, what
	// changed in it and the operations that move it on.
	RoleGit = "git"
)
