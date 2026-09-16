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
// edits files, and the changes pane waits on. Session names are already
// restricted to [A-Za-z0-9_-], so the channel needs no escaping.
func ChangesChannel(session string) string {
	return "lt-changes-" + session
}

// Pane roles.
const (
	RoleClaude  = "claude"
	RoleShell   = "shell"
	RoleChanges = "changes"
	RoleScratch = "scratch"
)
