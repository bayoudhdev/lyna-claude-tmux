package tmux

// StopPane returns the commands that close the pane of a teammate the user
// stopped.
//
// The agents channel of its workspace is signaled first, while the pane still
// names the session it belongs to: a pane closed this way is reported by no
// hook, and the rail would otherwise draw the teammate until its next reading.
// Both commands run in one invocation, so the rail reads the workspace after
// the pane is gone.
//
// The target is a pane id and nothing else, for the reason PasteLine gives: a
// name or a pattern is resolved by tmux against whatever is running now, and a
// pane closed by mistake takes the work of whatever ran in it. An
// unidentified pane returns nothing.
func StopPane(pane string) []Command {
	if !ValidPaneID(pane) {
		return nil
	}
	return []Command{
		{"run-shell", "-C", "-t", pane, AgentsSignal},
		{"kill-pane", "-t", pane},
	}
}
