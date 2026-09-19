package hook

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/hookevent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func strp(s string) *string { return &s }

func TestCommands(t *testing.T) {
	const pane = "%7"
	set := func(state string) tmux.Command {
		return tmux.Command{"set-option", "-p", "-t", pane, "@lt_state", state}
	}
	tty := tmux.Command{"display-message", "-p", "-t", pane, "#{pane_tty}"}
	signal := tmux.Command{"run-shell", "-C", "-t", pane, "wait-for -S 'lt-changes-#{session_id}'"}
	agents := tmux.Command{"run-shell", "-C", "-t", pane, "wait-for -S 'lt-agents-#{session_id}'"}
	remember := tmux.RememberLayout(pane)[0]
	branchSet := func(v string) tmux.Command { return tmux.Command{"set-option", "-t", pane, "@lt_branch", v} }
	branchUnset := tmux.Command{"set-option", "-u", "-t", pane, "@lt_branch"}
	const path = "/home/u/.claude/projects/-work-api/5f0c2a1e.jsonl"
	keep := tmux.Command{"set-option", "-p", "-t", pane, "@lt_transcript", path}
	drop := tmux.Command{"set-option", "-p", "-u", "-t", pane, "@lt_transcript"}

	cases := []struct {
		name       string
		ev         hookevent.Event
		p          Payload
		unknown    bool
		branch     *string
		transcript *string
		bellOff    bool
		want       []tmux.Command
		wantRing   bool
	}{
		{name: "session start idle", ev: hookevent.SessionStart, p: Payload{Source: "startup"}, want: []tmux.Command{set("idle"), agents, remember}},
		{name: "session start with branch", ev: hookevent.SessionStart, p: Payload{Source: "resume"}, branch: strp("main"), want: []tmux.Command{set("idle"), agents, remember, branchSet("main")}},
		{name: "session start outside repository clears branch", ev: hookevent.SessionStart, branch: strp(""), want: []tmux.Command{set("idle"), agents, remember, branchUnset}},
		{name: "session start branch escaped for drawing", ev: hookevent.SessionStart, branch: strp("feat/#[fg=red]"), want: []tmux.Command{set("idle"), agents, remember, branchSet("feat/##[fg=red]")}},
		{name: "compaction keeps state, refreshes branch", ev: hookevent.SessionStart, p: Payload{Source: "compact"}, branch: strp("dev"), want: []tmux.Command{branchSet("dev")}},
		{name: "compaction without branch does nothing", ev: hookevent.SessionStart, p: Payload{Source: "compact"}, want: nil},
		{name: "session start keeps its transcript", ev: hookevent.SessionStart, p: Payload{Source: "startup"}, transcript: strp(path), branch: strp("main"), want: []tmux.Command{set("idle"), agents, remember, keep, branchSet("main")}},
		{name: "a resumed session keeps its transcript", ev: hookevent.SessionStart, p: Payload{Source: "resume"}, transcript: strp(path), want: []tmux.Command{set("idle"), agents, remember, keep}},
		{name: "compaction keeps its transcript too", ev: hookevent.SessionStart, p: Payload{Source: "compact"}, transcript: strp(path), want: []tmux.Command{keep}},
		{name: "a session whose transcript is not kept leaves none", ev: hookevent.SessionStart, p: Payload{Source: "clear"}, transcript: strp(""), want: []tmux.Command{set("idle"), agents, remember, drop}},
		{name: "a transcript on another event is not kept", ev: hookevent.UserPromptSubmit, transcript: strp(path), want: []tmux.Command{set("busy"), remember}},
		{name: "a transcript on stop is not kept", ev: hookevent.Stop, transcript: strp(path), bellOff: true, want: []tmux.Command{set("idle")}},
		{name: "a transcript on session end is left", ev: hookevent.SessionEnd, transcript: strp(""), want: []tmux.Command{
			{"set-option", "-p", "-u", "-t", pane, "@lt_state"},
			{"set-option", "-p", "-u", "-t", pane, "@lt_subagents"},
			{"set-option", "-p", "-u", "-t", pane, "@lt_running"},
			agents,
		}},
		{name: "prompt busy", ev: hookevent.UserPromptSubmit, want: []tmux.Command{set("busy"), remember}},
		{name: "prompt ignores branch", ev: hookevent.UserPromptSubmit, branch: strp("main"), want: []tmux.Command{set("busy"), remember}},
		{name: "ask user waits and rings", ev: hookevent.PreToolUse, p: Payload{ToolName: "AskUserQuestion"}, want: []tmux.Command{set("waiting"), tty}, wantRing: true},
		{name: "ask user bell disabled", ev: hookevent.PreToolUse, p: Payload{ToolName: "AskUserQuestion"}, bellOff: true, want: []tmux.Command{set("waiting")}},
		{name: "other pre tool use ignored", ev: hookevent.PreToolUse, p: Payload{ToolName: "Bash"}, want: nil},
		{name: "pre tool use unknown payload trusts matcher", ev: hookevent.PreToolUse, unknown: true, want: []tmux.Command{set("waiting"), tty}, wantRing: true},
		{name: "permission request", ev: hookevent.PermissionRequest, p: Payload{ToolName: "Bash"}, want: []tmux.Command{set("waiting"), tty}, wantRing: true},
		{name: "permission request from subagent", ev: hookevent.PermissionRequest, p: Payload{ToolName: "Bash", AgentID: "a1"}, want: []tmux.Command{set("waiting"), tty}, wantRing: true},
		{name: "permission notification", ev: hookevent.Notification, p: Payload{NotificationType: "permission_prompt"}, want: []tmux.Command{set("waiting"), tty}, wantRing: true},
		{name: "idle notification ignored", ev: hookevent.Notification, p: Payload{NotificationType: "idle_prompt"}, want: nil},
		{name: "notification unknown payload trusts matcher", ev: hookevent.Notification, unknown: true, bellOff: true, want: []tmux.Command{set("waiting")}},
		{name: "post tool use write signals", ev: hookevent.PostToolUse, p: Payload{ToolName: "Write"}, want: []tmux.Command{set("busy"), signal}},
		{name: "post tool use edit signals", ev: hookevent.PostToolUse, p: Payload{ToolName: "Edit"}, want: []tmux.Command{set("busy"), signal}},
		{name: "post tool use multiedit signals", ev: hookevent.PostToolUse, p: Payload{ToolName: "MultiEdit"}, want: []tmux.Command{set("busy"), signal}},
		{name: "post tool use notebook signals", ev: hookevent.PostToolUse, p: Payload{ToolName: "NotebookEdit"}, want: []tmux.Command{set("busy"), signal}},
		{name: "post tool use read only busy", ev: hookevent.PostToolUse, p: Payload{ToolName: "Read"}, want: []tmux.Command{set("busy")}},
		{name: "post tool use near miss name", ev: hookevent.PostToolUse, p: Payload{ToolName: "Edits"}, want: []tmux.Command{set("busy")}},
		{name: "post tool use empty tool name", ev: hookevent.PostToolUse, p: Payload{}, want: []tmux.Command{set("busy")}},
		{name: "post tool use unknown payload signals", ev: hookevent.PostToolUse, unknown: true, want: []tmux.Command{set("busy"), signal}},
		{name: "subagent write only signals", ev: hookevent.PostToolUse, p: Payload{ToolName: "Write", AgentID: "a1"}, want: []tmux.Command{signal}},
		{name: "subagent read does nothing", ev: hookevent.PostToolUse, p: Payload{ToolName: "Grep", AgentID: "a1"}, want: nil},
		{
			name: "subagent start", ev: hookevent.SubagentStart, p: Payload{AgentID: "ag-1", AgentType: "Explore"},
			want: []tmux.Command{
				{"set-option", "-p", "-t", pane, "-F", "@lt_subagents", "#{e|+:#{@lt_subagents},1}"},
				{"set-option", "-p", "-t", pane, "-F", "@lt_running", "#{?#{e|<:#{n:@lt_running},512},#{@lt_running}ag-1=Explore#,,#{@lt_running}}"},
				agents,
			},
		},
		{
			name: "subagent stop", ev: hookevent.SubagentStop, p: Payload{AgentID: "ag-1", AgentType: "Explore"},
			want: []tmux.Command{
				{"set-option", "-p", "-t", pane, "-F", "@lt_subagents", "#{?#{e|>:#{@lt_subagents},0},#{e|-:#{@lt_subagents},1},0}"},
				{"set-option", "-p", "-t", pane, "-F", "@lt_running", "#{s/ag-1=Explore,//:@lt_running}"},
				agents,
			},
		},
		{
			name: "a subagent the agent did not name is only counted", ev: hookevent.SubagentStart,
			want: []tmux.Command{{"set-option", "-p", "-t", pane, "-F", "@lt_subagents", "#{e|+:#{@lt_subagents},1}"}, agents},
		},
		{name: "a teammate out of work refreshes the sidebar", ev: hookevent.TeammateIdle, want: []tmux.Command{agents}},
		{name: "a task created refreshes the sidebar", ev: hookevent.TaskCreated, want: []tmux.Command{agents}},
		{name: "a task completed refreshes the sidebar", ev: hookevent.TaskCompleted, want: []tmux.Command{agents}},
		{name: "a team event with no payload still refreshes", ev: hookevent.TeammateIdle, unknown: true, want: []tmux.Command{agents}},
		{name: "stop idle bell branch", ev: hookevent.Stop, branch: strp("main"), want: []tmux.Command{set("idle"), branchSet("main"), tty}, wantRing: true},
		{name: "stop branch lookup failed", ev: hookevent.Stop, bellOff: true, want: []tmux.Command{set("idle")}},
		{name: "session end unsets", ev: hookevent.SessionEnd, p: Payload{Reason: "prompt_input_exit"}, want: []tmux.Command{
			{"set-option", "-p", "-u", "-t", pane, "@lt_state"},
			{"set-option", "-p", "-u", "-t", pane, "@lt_subagents"},
			{"set-option", "-p", "-u", "-t", pane, "@lt_running"},
			agents,
		}},
		{name: "unhandled event", ev: hookevent.Event("PreCompact"), want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ring := Commands(tc.ev, tc.p, !tc.unknown, pane, tc.branch, tc.transcript, !tc.bellOff)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Commands =\n%q\nwant\n%q", got, tc.want)
			}
			if ring != tc.wantRing {
				t.Fatalf("ring = %v; want %v", ring, tc.wantRing)
			}
		})
	}
}

// Every registered event must produce tmux work for a payload that matches
// its registration; otherwise the settings would register a hook that does nothing.
func TestCommandsCoverRegistrations(t *testing.T) {
	for _, r := range hookevent.Registrations() {
		t.Run(string(r.Event)+"/"+r.Matcher, func(t *testing.T) {
			p := Payload{ToolName: "Write"}
			switch r.Event {
			case hookevent.PreToolUse:
				p.ToolName = r.Matcher
			case hookevent.Notification:
				p.NotificationType = r.Matcher
			}
			cmds, _ := Commands(r.Event, p, true, "%1", nil, nil, true)
			if len(cmds) == 0 {
				t.Fatalf("no commands for %+v", r)
			}
		})
	}
}

func TestFileEditMatcherIsExactList(t *testing.T) {
	// The matcher is used as an exact-name list; a regular expression
	// character would change how Claude Code evaluates it.
	if strings.ContainsFunc(hookevent.MatchFileEdits, func(r rune) bool {
		return r != '|' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
	}) {
		t.Fatalf("MatchFileEdits %q is not a plain name list", hookevent.MatchFileEdits)
	}
}

func TestValidID(t *testing.T) {
	cases := []struct {
		in    string
		sigil byte
		want  bool
	}{
		{"%0", '%', true},
		{"%123", '%', true},
		{"$4", '$', true},
		{"@12", '@', true},
		{"%", '%', false},
		{"", '%', false},
		{"%1a", '%', false},
		{"1", '%', false},
		{"$1", '%', false},
		{"%-1", '%', false},
		{"%1;", '%', false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := validID(tc.in, tc.sigil); got != tc.want {
				t.Fatalf("validID(%q, %q) = %v", tc.in, tc.sigil, got)
			}
		})
	}
}

func TestLastLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/dev/ttys001\n", "/dev/ttys001"},
		{"noise\n/dev/pts/3\n", "/dev/pts/3"},
		{"/dev/pts/3", "/dev/pts/3"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := lastLine(tc.in); got != tc.want {
				t.Fatalf("lastLine(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
