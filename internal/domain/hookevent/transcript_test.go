package hookevent

import (
	"encoding/json"
	"testing"
)

func TestTranscript(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a session start as documented",
			in:   `{"session_id":"abc123","transcript_path":"/home/u/.claude/projects/-work-api/abc123.jsonl","cwd":"/work/api","hook_event_name":"SessionStart","source":"startup"}`,
			want: "/home/u/.claude/projects/-work-api/abc123.jsonl",
		},
		{name: "a subagent stop names the session's own transcript", in: `{"transcript_path":"/t/s.jsonl","agent_transcript_path":"/t/s/subagents/agent-a1.jsonl"}`, want: "/t/s.jsonl"},
		{name: "kept as spelled", in: `{"transcript_path":"relative/../x.txt"}`, want: "relative/../x.txt"},
		{name: "control characters kept as data", in: `{"transcript_path":"/t/\u001b]0;x\u0007.jsonl"}`, want: "/t/\x1b]0;x\a.jsonl"},
		{name: "no transcript", in: `{"session_id":"abc123"}`},
		{name: "a null transcript", in: `{"transcript_path":null}`},
		{name: "a transcript of the wrong type", in: `{"transcript_path":["/t/s.jsonl"]}`},
		{name: "a payload that is not an object", in: `["/t/s.jsonl"]`},
		{name: "a payload that is not JSON", in: `{"transcript_path":"/t/s.jsonl"`},
		{name: "nothing", in: ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Transcript([]byte(tc.in)); got != tc.want {
				t.Fatalf("Transcript(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// FuzzTranscript reads any payload: no panic, and a path read back from a
// payload holding only it is the path it held.
func FuzzTranscript(f *testing.F) {
	for _, s := range []string{
		`{"transcript_path":"/t/s.jsonl"}`, `{"transcript_path":1}`, `null`, `{`, ``,
		`{"transcript_path":"\ud800"}`, `{"transcript_path":"a","transcript_path":"b"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := Transcript(data)
		if path == "" {
			return
		}
		enc, err := json.Marshal(map[string]string{"transcript_path": path})
		if err != nil {
			t.Fatal(err)
		}
		if again := Transcript(enc); again != path {
			t.Fatalf("round trip %q -> %s -> %q", path, enc, again)
		}
	})
}
