package hook

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Payloads shaped like the examples in the hook reference, one per handled event.
var documentedPayloads = map[string]string{
	"SessionStart":      `{"session_id":"abc123","transcript_path":"/u/.claude/projects/p/t.jsonl","cwd":"/u/project","hook_event_name":"SessionStart","source":"resume","model":"claude-opus-5","seconds_since_last_response":5400,"context_tokens":182340,"prompt_cache_likely_expired":true,"estimated_cache_write_usd":1.1396}`,
	"UserPromptSubmit":  `{"session_id":"abc123","transcript_path":"/t.jsonl","cwd":"/u/project","permission_mode":"default","hook_event_name":"UserPromptSubmit","prompt":"Write a function"}`,
	"PreToolUse":        `{"session_id":"abc123","prompt_id":"550e8400","cwd":"/u/project","permission_mode":"default","effort":{"level":"medium"},"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which?","header":"H","options":[{"label":"A"}],"multiSelect":false}]},"tool_use_id":"toolu_01"}`,
	"PermissionRequest": `{"session_id":"abc123","cwd":"/u/project","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/build"},"permission_suggestions":[]}`,
	"Notification":      `{"session_id":"abc123","transcript_path":"/t.jsonl","cwd":"/u/project","hook_event_name":"Notification","message":"Claude needs your permission","title":"Permission needed","notification_type":"permission_prompt"}`,
	"PostToolUse":       `{"session_id":"abc123","cwd":"/u/project","permission_mode":"default","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"/path/to/file.txt","content":"file content"},"tool_response":{"filePath":"/path/to/file.txt","type":"create"},"tool_use_id":"toolu_01","duration_ms":12}`,
	"SubagentStart":     `{"session_id":"abc123","cwd":"/u/project","hook_event_name":"SubagentStart","agent_id":"agent-abc123","agent_type":"Explore"}`,
	"SubagentStop":      `{"session_id":"abc123","cwd":"/u/project","permission_mode":"default","hook_event_name":"SubagentStop","stop_hook_active":false,"agent_id":"def456","agent_type":"Explore","agent_transcript_path":"/t/sub.jsonl","last_assistant_message":"done","background_tasks":[],"session_crons":[]}`,
	"Stop":              `{"session_id":"abc123","cwd":"/u/project","permission_mode":"default","hook_event_name":"Stop","stop_hook_active":true,"last_assistant_message":"I've completed it","background_tasks":[{"id":"task-001","type":"shell","status":"running"}],"session_crons":[]}`,
	"SessionEnd":        `{"session_id":"abc123","transcript_path":"/t.jsonl","cwd":"/u/project","hook_event_name":"SessionEnd","reason":"other"}`,
}

func TestDecodePayloadDocumented(t *testing.T) {
	cases := []struct {
		event string
		want  Payload
	}{
		{"SessionStart", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "SessionStart", Source: "resume"}},
		{"UserPromptSubmit", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "UserPromptSubmit"}},
		{"PreToolUse", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "PreToolUse", ToolName: "AskUserQuestion"}},
		{"PermissionRequest", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "PermissionRequest", ToolName: "Bash"}},
		{"Notification", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "Notification", NotificationType: "permission_prompt"}},
		{"PostToolUse", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "PostToolUse", ToolName: "Write"}},
		{"SubagentStart", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "SubagentStart", AgentID: "agent-abc123", AgentType: "Explore"}},
		{"SubagentStop", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "SubagentStop", AgentID: "def456", AgentType: "Explore"}},
		{"Stop", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "Stop"}},
		{"SessionEnd", Payload{SessionID: "abc123", Cwd: "/u/project", HookEventName: "SessionEnd", Reason: "other"}},
	}
	if len(cases) != len(documentedPayloads) {
		t.Fatalf("%d cases for %d documented payloads", len(cases), len(documentedPayloads))
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			got, err := DecodePayload([]byte(documentedPayloads[tc.event]))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("DecodePayload = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestDecodePayloadFailures(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		notObject bool
	}{
		{name: "empty", in: ""},
		{name: "whitespace", in: " \n\t"},
		{name: "truncated", in: `{"cwd":"/a"`},
		{name: "garbage", in: "{not json"},
		{name: "trailing garbage", in: `{"cwd":"/a"} x`},
		{name: "null", in: "null", notObject: true},
		{name: "array", in: `[{"cwd":"/a"}]`, notObject: true},
		{name: "string", in: `"cwd"`, notObject: true},
		{name: "number", in: "42", notObject: true},
		{name: "wrong type for cwd", in: `{"cwd":7,"tool_name":"Write"}`},
		{name: "wrong type for tool_name", in: `{"tool_name":["Write"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePayload([]byte(tc.in))
			if err == nil {
				t.Fatalf("DecodePayload(%q) = %+v; want an error", tc.in, got)
			}
			if got != (Payload{}) {
				t.Fatalf("failed decode returned fields %+v", got)
			}
			if errors.Is(err, ErrNotObject) != tc.notObject {
				t.Fatalf("error %v; ErrNotObject expected %v", err, tc.notObject)
			}
		})
	}
}

func TestDecodePayloadIgnoresUnknownAndKeepsText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Payload
	}{
		{name: "unknown fields", in: `{"future":{"x":[1,2]},"tool_name":"Edit","extra":null}`, want: Payload{ToolName: "Edit"}},
		{name: "null known field", in: `{"cwd":null,"tool_name":"Edit"}`, want: Payload{ToolName: "Edit"}},
		{name: "leading whitespace", in: "\n  {\"reason\":\"clear\"}", want: Payload{Reason: "clear"}},
		{name: "escape sequences kept as data", in: mustJSON(map[string]string{"cwd": "/a\x1b]0;x\x07"}), want: Payload{Cwd: "/a\x1b]0;x\x07"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePayload([]byte(tc.in))
			if err != nil || got != tc.want {
				t.Fatalf("DecodePayload = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func FuzzDecodePayload(f *testing.F) {
	for _, p := range documentedPayloads {
		f.Add([]byte(p))
	}
	for _, s := range []string{"", "{", "null", "[]", `{"cwd":1}`, `{"tool_name":"\ud800"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := DecodePayload(data)
		if err != nil {
			if p != (Payload{}) {
				t.Fatalf("fields returned with error: %+v", p)
			}
			return
		}
		if !strings.HasPrefix(strings.TrimLeft(string(data), " \t\r\n"), "{") {
			t.Fatalf("non-object input decoded: %q", data)
		}
		// Round trip: what was decoded survives encoding and decoding again.
		enc, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		again, err := DecodePayload(enc)
		if err != nil || again != p {
			t.Fatalf("round trip %+v -> %s -> %+v, %v", p, enc, again, err)
		}
	})
}
