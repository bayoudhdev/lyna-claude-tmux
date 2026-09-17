package tmux_test

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestStartSubagent(t *testing.T) {
	const count = "set-option -p -t %2 -F @lt_subagents '#{e|+:#{@lt_subagents},1}'"
	cases := []struct {
		name            string
		pane, id, agent string
		want            string
	}{
		{
			name: "a named subagent", pane: "%2", id: "agent-1", agent: "security-auditor",
			want: count + " ; set-option -p -t %2 -F @lt_running " +
				"'#{?#{e|<:#{n:@lt_running},512},#{@lt_running}agent-1=security-auditor#,,#{@lt_running}}'",
		},
		{name: "a subagent the agent did not name", pane: "%2", agent: "Explore", want: count},
		{
			name: "an identifier that would end the entry", pane: "%2", id: "a,b=c", agent: "x,y",
			want: count + " ; set-option -p -t %2 -F @lt_running " +
				"'#{?#{e|<:#{n:@lt_running},512},#{@lt_running}abc=xy#,,#{@lt_running}}'",
		},
		{name: "a pane named instead of identified", pane: "claude", id: "agent-1", want: ""},
		{name: "no pane at all", id: "agent-1", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.StartSubagent(tc.pane, tc.id, tc.agent).String(); got != tc.want {
				t.Fatalf("StartSubagent:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestStopSubagent(t *testing.T) {
	const count = "set-option -p -t %2 -F @lt_subagents '#{?#{e|>:#{@lt_subagents},0},#{e|-:#{@lt_subagents},1},0}'"
	cases := []struct {
		name            string
		pane, id, agent string
		want            string
	}{
		{
			name: "a named subagent", pane: "%2", id: "agent-1", agent: "security-auditor",
			want: count + " ; set-option -p -t %2 -F @lt_running '#{s/agent-1=security-auditor,//:@lt_running}'",
		},
		{name: "a subagent the agent did not name", pane: "%2", agent: "Explore", want: count},
		{name: "a pane named instead of identified", pane: "agents", id: "agent-1", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.StopSubagent(tc.pane, tc.id, tc.agent).String(); got != tc.want {
				t.Fatalf("StopSubagent:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestClearSubagents(t *testing.T) {
	want := "set-option -p -u -t %2 @lt_subagents ; set-option -p -u -t %2 @lt_running"
	if got := tmux.ClearSubagents("%2").String(); got != want {
		t.Fatalf("ClearSubagents:\n got %s\nwant %s", got, want)
	}
	if got := tmux.ClearSubagents("claude").String(); got != "" {
		t.Fatalf("ClearSubagents of a pane named instead of identified = %s", got)
	}
}

// TestSubagentEntryIsOneToken drives what an agent can call itself into the
// commands it is recorded with: whatever the name holds, the entry stays one
// argument of one command, and the pattern that takes it off again holds
// nothing the format reads as anything but text.
func TestSubagentEntryIsOneToken(t *testing.T) {
	ids := []string{"a ; kill-server", "#{pane_pid}", "a'b\\c", strings.Repeat("n", 80), "a\nb"}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			start := tmux.StartSubagent("%2", id, "x")
			stop := tmux.StopSubagent("%2", id, "x")
			if len(start) != 2 || len(stop) != 2 {
				t.Fatalf("StartSubagent %d commands, StopSubagent %d", len(start), len(stop))
			}
			entry := team.RunningEntry(id, "x")
			if strings.ContainsAny(strings.TrimSuffix(entry, ","), ",;'\\\n #") {
				t.Fatalf("the entry %q is not one token", entry)
			}
			if !strings.Contains(stop[1][len(stop[1])-1], entry) {
				t.Fatalf("the removal %q does not name the entry %q", stop[1], entry)
			}
		})
	}
}
