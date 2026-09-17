// Package hookevent names the Claude Code hook events lyna-tmux registers in
// the per-launch settings file and handles with `lmux hook <event>`.
//
// It is the contract between the settings generator (which writes the hook
// entries) and the hook handler (which maps each event to agent state), so both
// sides agree on event names and matchers.
package hookevent

import "slices"

// Event is a Claude Code hook event name, exactly as it appears in settings.
type Event string

// Events lyna-tmux handles.
const (
	SessionStart      Event = "SessionStart"
	UserPromptSubmit  Event = "UserPromptSubmit"
	PreToolUse        Event = "PreToolUse"
	PermissionRequest Event = "PermissionRequest"
	Notification      Event = "Notification"
	PostToolUse       Event = "PostToolUse"
	SubagentStart     Event = "SubagentStart"
	SubagentStop      Event = "SubagentStop"
	Stop              Event = "Stop"
	SessionEnd        Event = "SessionEnd"
)

// Matchers used by registrations.
const (
	// MatchAskUser limits PreToolUse to the tool that asks the user a question.
	MatchAskUser = "AskUserQuestion"
	// MatchPermissionPrompt limits Notification to permission prompts.
	MatchPermissionPrompt = "permission_prompt"
	// MatchFileEdits limits PostToolUse to tools that change files.
	MatchFileEdits = "Edit|MultiEdit|Write|NotebookEdit"
)

// Registration is one hook entry: an event and an optional matcher.
type Registration struct {
	Event   Event
	Matcher string
}

// Registrations returns the hook entries of the settings file, in the order
// they are written.
func Registrations() []Registration {
	return []Registration{
		{Event: SessionStart},
		{Event: UserPromptSubmit},
		{Event: PreToolUse, Matcher: MatchAskUser},
		{Event: PermissionRequest},
		{Event: Notification, Matcher: MatchPermissionPrompt},
		{Event: PostToolUse},
		{Event: SubagentStart},
		{Event: SubagentStop},
		{Event: Stop},
		{Event: SessionEnd},
	}
}

// Parse returns the event for a command-line argument and whether it is one
// lyna-tmux handles.
func Parse(s string) (Event, bool) {
	e := Event(s)
	ok := slices.ContainsFunc(Registrations(), func(r Registration) bool { return r.Event == e })
	return e, ok
}
