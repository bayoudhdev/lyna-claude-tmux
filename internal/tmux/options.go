package tmux

import "strings"

// User options lyna-tmux stores on its tmux objects. They carry live state from
// hooks to the status line and pickers without any extra process or file.
const (
	OptState     = "@lt_state"     // pane: busy | waiting | idle
	OptRole      = "@lt_role"      // pane: claude | shell | changes | scratch
	OptSubagents = "@lt_subagents" // pane: number of running subagents
	OptManaged   = "@lt_managed"   // session: "1" when created by lyna-tmux
	OptProject   = "@lt_project"   // session: project root directory
	OptSandbox   = "@lt_sandbox"   // session: sandbox profile name
	OptIsolation = "@lt_isolation" // session: sandbox isolation level
	OptLayout    = "@lt_layout"    // session: layout name
	OptBranch    = "@lt_branch"    // session: git branch of the project, set by hooks
	OptOrigin    = "@lt_origin"    // session: window id a popup session was launched from
	OptParent    = "@lt_parent"    // global: client that last opened the agents picker
	OptConfHash  = "@lt_conf"      // global: fingerprint of the loaded generated configuration
	OptSettings  = "@lt_settings"  // pane: per-launch Claude settings file of a Claude pane

	// Names shared with the tmux plugin this project derives from, used when
	// lyna-tmux runs inside the user's own tmux server so existing sessions and
	// configuration keep working.
	OptClaudeOrigin = "@claude_origin"
	OptClaudeParent = "@claude_parent"
)

// OptPassthrough is the tmux option that lets a program in a pane send an
// escape sequence straight to the outer terminal. The generated configuration
// turns it off for the server; a Claude pane turns it back on for itself,
// which is what carries the agent's desktop notifications and its progress
// bar out of tmux without giving every shell pane the same channel.
const OptPassthrough = "allow-passthrough"

// MaxBranchRunes bounds the branch name drawn in the status line.
const MaxBranchRunes = 32

// BranchOption prepares a git branch name for the @lt_branch option: control
// and escape characters removed, shortened to MaxBranchRunes with an ellipsis,
// and '#'-escaped because the status line draws expanded values with style
// markup (DrawEscape). strftime runs on the template before values are inserted, so '%'
// needs no escaping.
func BranchOption(branch string) string {
	var b strings.Builder
	n := 0
	for _, r := range branch {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		if n == MaxBranchRunes {
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

// Pane roles.
const (
	RoleClaude  = "claude"
	RoleShell   = "shell"
	RoleChanges = "changes"
	RoleScratch = "scratch"
)
