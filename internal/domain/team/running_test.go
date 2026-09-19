package team_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

func TestRunningEntry(t *testing.T) {
	cases := []struct {
		name, id, agentType, want string
	}{
		{name: "a subagent with a type", id: "agent_01HX", agentType: "security-auditor", want: "agent_01HX=security-auditor,"},
		{name: "a subagent of no type", id: "agent-2", want: "agent-2=,"},
		{name: "an identifier with the separators in it", id: "a=b,c", agentType: "x=y,z", want: "abc=xyz,"},
		{name: "a type a format would read", id: "a1", agentType: "#{pane_pid}", want: "a1=pane_pid,"},
		{name: "an identifier of spaces", id: "  ", agentType: "docs", want: ""},
		{name: "nothing at all", want: ""},
		{
			name: "parts longer than an entry holds",
			id:   strings.Repeat("i", 40), agentType: strings.Repeat("t", 40),
			want: strings.Repeat("i", team.MaxRunningPart) + "=" + strings.Repeat("t", team.MaxRunningPart) + ",",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := team.RunningEntry(tc.id, tc.agentType); got != tc.want {
				t.Fatalf("RunningEntry(%q, %q) = %q, want %q", tc.id, tc.agentType, got, tc.want)
			}
		})
	}
}

func TestParseRunning(t *testing.T) {
	cases := []struct {
		name, in string
		want     []team.Subagent
	}{
		{
			name: "the subagents of a busy pane", in: "a1=security-auditor,a2=,a3=docs,",
			want: []team.Subagent{{ID: "a1", Type: "security-auditor"}, {ID: "a2"}, {ID: "a3", Type: "docs"}},
		},
		{name: "a pane running none", in: ""},
		{
			name: "a list that does not end in the separator", in: "a1=docs,a2=sec",
			want: []team.Subagent{{ID: "a1", Type: "docs"}, {ID: "a2", Type: "sec"}},
		},
		{name: "a value nothing of ours wrote", in: "one two three"},
		{name: "an entry with no identifier", in: "=docs,"},
		{name: "a value holding a format", in: "#{pane_pid}=x,a1=docs,", want: []team.Subagent{{ID: "a1", Type: "docs"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := team.ParseRunning(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseRunning(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRunningRoundTrip is the contract the pane option rests on: an entry that
// was rendered is read back as the subagent it was made from, and cutting that
// exact text out of the list leaves the other entries alone.
func TestRunningRoundTrip(t *testing.T) {
	first := team.RunningEntry("agent-1", "security-auditor")
	second := team.RunningEntry("agent-2", "api-developer")
	list := first + second
	if got := team.ParseRunning(list); len(got) != 2 || got[0].ID != "agent-1" || got[1].Type != "api-developer" {
		t.Fatalf("ParseRunning(%q) = %+v", list, got)
	}
	rest := strings.Replace(list, first, "", 1)
	if got := team.ParseRunning(rest); len(got) != 1 || got[0].ID != "agent-2" {
		t.Fatalf("after removing %q: %+v", first, got)
	}
}

// FuzzRunning drives whatever an agent can call itself through the entry and
// back: an entry is one line of the list, never carries a character the list
// is cut on, and reads back as the identifier it was rendered from.
func FuzzRunning(f *testing.F) {
	f.Add("agent-1", "security-auditor")
	f.Add("", "")
	f.Add("a=b,c", "#{pane_pid}")
	f.Add(strings.Repeat("x", 100), "\n\t")
	f.Fuzz(func(t *testing.T, id, agentType string) {
		entry := team.RunningEntry(id, agentType)
		if entry == "" {
			return
		}
		if strings.ContainsAny(strings.TrimSuffix(entry, ","), ",") || strings.Count(entry, "=") != 1 {
			t.Fatalf("RunningEntry(%q, %q) = %q is not one entry", id, agentType, entry)
		}
		if len(entry) > 2*team.MaxRunningPart+2 {
			t.Fatalf("RunningEntry(%q, %q) = %q is longer than an entry", id, agentType, entry)
		}
		got := team.ParseRunning(entry)
		if len(got) != 1 {
			t.Fatalf("ParseRunning(%q) = %+v, want one subagent", entry, got)
		}
		if !strings.HasPrefix(entry, got[0].ID+"=") {
			t.Fatalf("ParseRunning(%q) read %q", entry, got[0].ID)
		}
	})
}
