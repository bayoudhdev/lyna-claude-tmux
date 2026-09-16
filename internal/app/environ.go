// Package app holds the lyna-tmux use cases. Commands in internal/cli parse
// flags and call into this package; adapters (tmux, Claude, the filesystem)
// are reached through the values a Host provides, so every use case runs
// against isolated servers and temporary directories in tests.
package app

import (
	"slices"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
)

// inheritedOnly lists variables that describe the process lyna-tmux was
// started from rather than the user's environment. The tmux server copies the
// environment of the client that starts it into every pane, so these are
// removed: a pane must not believe it runs inside another tmux pane, a
// managed workspace pane, or a Claude Code session (whose messaging token
// would otherwise reach every process in the workspace).
var inheritedOnly = []string{
	"TMUX",
	"TMUX_PANE",
	session.EnvManaged,
	session.EnvSocket,
	session.EnvSession,
	session.EnvSandbox,
	"CLAUDECODE",
	"CLAUDE_PID",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_EXECPATH",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_CODE_MESSAGING_SOCKET",
	"CLAUDE_CODE_MESSAGING_TOKEN",
}

// ServerEnviron returns environ without the variables that only describe the
// calling process, for the tmux client that may start the workspace server.
// Entries without '=' are dropped.
func ServerEnviron(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || slices.Contains(inheritedOnly, key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
