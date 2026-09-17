package hookevent

import "testing"

func TestRegistrations(t *testing.T) {
	regs := Registrations()
	seen := map[Registration]bool{}
	for _, r := range regs {
		if r.Event == "" {
			t.Fatal("empty event")
		}
		if seen[r] {
			t.Fatalf("duplicate registration %+v", r)
		}
		seen[r] = true
	}
	cases := []struct {
		event   Event
		matcher string
	}{
		{PreToolUse, MatchAskUser},
		{Notification, MatchPermissionPrompt},
		{PostToolUse, ""},
		{TeammateIdle, ""},
		{TaskCreated, ""},
		{TaskCompleted, ""},
		{Stop, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.event), func(t *testing.T) {
			if !seen[Registration{Event: tc.event, Matcher: tc.matcher}] {
				t.Fatalf("%s with matcher %q not registered", tc.event, tc.matcher)
			}
		})
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"SessionStart", true},
		{"PermissionRequest", true},
		{"SubagentStop", true},
		{"TeammateIdle", true},
		{"TaskCreated", true},
		{"TaskCompleted", true},
		{"SessionEnd", true},
		{"sessionstart", false},
		{"", false},
		{"PreCompact", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			e, ok := Parse(tc.in)
			if ok != tc.ok || string(e) != tc.in {
				t.Fatalf("Parse(%q) = %q, %v; want ok=%v", tc.in, e, ok, tc.ok)
			}
		})
	}
}
