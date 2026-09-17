package termx

// Notification channels Claude Code accepts for preferredNotifChannel. A
// channel Claude Code picks by itself is wrong inside a workspace: the agent
// runs in a tmux pane, where the terminal it can see is tmux, not the terminal
// the user is looking at, so the outer terminal is passed in from here.
const (
	// NotifyBell rings the terminal bell. tmux turns it into a bell on the
	// pane, which the workspace forwards to the status line and the window
	// tab, so the alert is visible even in a terminal that shows nothing.
	NotifyBell = "terminal_bell"
	// NotifyITerm2Bell posts a notification through the OSC 9 sequence and
	// rings the bell as well, so an alert still arrives when the terminal was
	// told not to post notifications.
	NotifyITerm2Bell = "iterm2_with_bell"
	// NotifyKitty posts a desktop notification through the terminal's own
	// escape sequence.
	NotifyKitty = "kitty"
)

// NotifyChannel is the channel to hand Claude Code for a pane drawn by
// program. A terminal that posts desktop notifications from an escape sequence
// gets the channel that uses it, which reaches the user through the pane
// because a Claude pane allows passthrough; every other terminal, and one we
// cannot name, gets the bell, which needs nothing from the terminal.
func NotifyChannel(program Program) string {
	switch program {
	case ProgramITerm2:
		return NotifyITerm2Bell
	case ProgramKitty:
		return NotifyKitty
	default:
		return NotifyBell
	}
}
