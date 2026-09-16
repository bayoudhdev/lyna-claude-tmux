package hook

import (
	"context"
	"errors"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// The fields of a display-message reply are separated by the unit separator
// of the tmux package, which rejects session names holding control characters
// and never produces one in a window option set to a window ID, so it cannot
// occur inside a field. tmux.SplitFields reads the reply back, including from
// the version that prints the separator as its octal escape.

// BellForwardInput is one `lyna-tmux bell-forward <session-id>` invocation,
// run by the alert-bell hook of the plugin-mode configuration with
// #{hook_session}.
type BellForwardInput struct {
	// Session is the ID ("$3") of the session whose window rang. An ID, unlike
	// a name, is always safe to pass through the shell run-shell uses.
	Session string
	// Prefix is the per-directory popup session prefix (popup.session_prefix);
	// empty means session.DefaultPopupPrefix.
	Prefix string
	// Getenv reads the environment; $TMUX selects the server.
	Getenv func(string) string
}

// BellForward forwards a bell from a per-directory popup session to the window
// the popup was opened from, so the user's own session shows the alert even
// though the ringing pane lives in another session. It ports the upstream
// plugin's alert-bell forwarding and keeps its semantics:
//
//   - only sessions whose name starts with the popup prefix forward;
//   - the target is the window recorded in the session option @lt_origin, or
//     @claude_origin for sessions created by the upstream plugin;
//   - nothing is forwarded when that window itself belongs to a popup session,
//     which would otherwise bounce the bell between popups;
//   - the bell is a BEL written to the terminal of the origin window's active
//     pane, which makes tmux flag and announce that window on its own.
//
// Every value tmux returns is used as a command argument or a file path,
// never handed to a shell. It returns the exit status, always 0; failures are
// logged.
func BellForward(ctx context.Context, in BellForwardInput, deps Deps) (status int) {
	deps = deps.withDefaults()
	const name = "bell-forward"
	defer func() {
		if r := recover(); r != nil {
			deps.logf(name, "internal error: %v", r)
			status = 0
		}
	}()
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if !validID(in.Session, '$') {
		deps.logf(name, "invalid session ID %q", in.Session)
		return 0
	}
	sock, ok := tmux.SocketFromEnv(getenv(envTmux))
	if !ok {
		deps.logf(name, "not running inside tmux")
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	client := deps.Tmux(sock)

	out, err := client.Run(ctx, "display-message", "-p", "-t", in.Session,
		tmux.FieldSep("#{session_name}", "#{"+tmux.OptOrigin+"}", "#{"+tmux.OptClaudeOrigin+"}"))
	if err != nil {
		deps.logf(name, "%v", err)
		return 0
	}
	fields := tmux.SplitFields(strings.TrimRight(out, "\n"))
	if len(fields) != 3 || !session.IsPopup(in.Prefix, fields[0]) {
		return 0
	}
	origin := fields[1]
	if origin == "" {
		origin = fields[2]
	}
	if origin == "" {
		return 0
	}
	if !validID(origin, '@') && !validID(origin, '%') {
		deps.logf(name, "origin %q is not a window or pane ID", origin)
		return 0
	}

	out, err = client.Run(ctx, "display-message", "-p", "-t", origin, tmux.FieldSep("#{session_name}", "#{pane_tty}"))
	if err != nil {
		// A popup routinely outlives the window it was opened from.
		if !errors.Is(err, tmux.ErrNotFound) {
			deps.logf(name, "%v", err)
		}
		return 0
	}
	fields = tmux.SplitFields(strings.TrimRight(out, "\n"))
	// display-message expands against no target when the origin is gone
	// (its target lookup may fail), which yields empty fields.
	if len(fields) != 2 || fields[1] == "" || session.IsPopup(in.Prefix, fields[0]) {
		return 0
	}
	if err := ringTTY(deps.OpenTTY, fields[1]); err != nil {
		deps.logf(name, "bell: %v", err)
	}
	return 0
}
