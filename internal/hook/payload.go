package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxStdinBytes caps the hook payload read from standard input. PostToolUse
// payloads carry tool input and output, which can be large; everything the
// handler needs sits in a few scalar fields.
const MaxStdinBytes = 1 << 20

// Payload holds the fields of a Claude Code hook input the handler reads.
// Every other field is ignored. Field names follow the hook reference
// (https://code.claude.com/docs/en/hooks): common fields on every event plus
// tool_name (PreToolUse, PermissionRequest, PostToolUse), notification_type
// (Notification), source (SessionStart) and reason (SessionEnd). agent_id is
// present only when the event fires inside a subagent.
type Payload struct {
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	HookEventName    string `json:"hook_event_name"`
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
	AgentID          string `json:"agent_id"`
	AgentType        string `json:"agent_type"`
	Source           string `json:"source"`
	Reason           string `json:"reason"`
}

// ErrNotObject is returned when the payload is not a JSON object.
var ErrNotObject = errors.New("hook: payload is not a JSON object")

// DecodePayload parses a hook payload. Unknown fields are ignored; a known
// field of the wrong type fails the decode, because the input then does not
// have the documented shape and none of it can be trusted to mean what the
// handler expects. Nothing in the payload is ever executed or used as a command.
func DecodePayload(data []byte) (Payload, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		if !json.Valid(data) {
			return Payload{}, errors.New("hook: decode payload: invalid JSON")
		}
		return Payload{}, ErrNotObject
	}
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return Payload{}, fmt.Errorf("hook: decode payload: %w", err)
	}
	return p, nil
}
