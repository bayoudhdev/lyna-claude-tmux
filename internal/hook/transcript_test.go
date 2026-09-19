package hook

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// transcriptUnset is the command that leaves a pane with no transcript.
var transcriptUnset = []string{"set-option", "-p", "-u", "-t", "%3", "@lt_transcript"}

// sessionStartBatch is the batch a session start in pane %3 sends, outside any
// repository, with the transcript command in its place.
func sessionStartBatch(transcript ...string) []string {
	out := []string{"-S", testSocket, "-u", "set-option", "-p", "-t", "%3", "@lt_state", "idle", ";"}
	out = append(out, agentsSignal...)
	out = append(out, rememberLayout...)
	if len(transcript) > 0 {
		out = append(append(out, ";"), transcript...)
	}
	return append(out, ";", "set-option", "-u", "-t", "%3", "@lt_branch")
}

// TestRunTranscript covers which events keep the transcript a payload names,
// and what a session start that names none leaves on the pane.
func TestRunTranscript(t *testing.T) {
	const path = "/home/u/.claude/projects/-work-api/5f0c2a1e-7b3d-4c8e-9a10-2b3c4d5e6f70.jsonl"
	const spaced = "/home/u/My Projects #2/.claude/projects/-work-api/s.jsonl"
	keep := func(p string) []string { return []string{"set-option", "-p", "-t", "%3", "@lt_transcript", p} }
	cases := []struct {
		name    string
		event   string
		stdin   string
		want    [][]string
		wantLog string
	}{
		{
			name: "a session start keeps the transcript it names", event: "SessionStart",
			stdin: `{"session_id":"5f0c2a1e","transcript_path":"` + path + `","source":"startup"}`,
			want:  [][]string{sessionStartBatch(keep(path)...)},
		},
		{
			name: "a transcript under a directory with spaces and a hash", event: "SessionStart",
			stdin: `{"transcript_path":"` + spaced + `","source":"clear"}`,
			want:  [][]string{sessionStartBatch(keep(spaced)...)},
		},
		{
			name: "a session start that names no transcript leaves none", event: "SessionStart",
			stdin: `{"source":"startup"}`,
			want:  [][]string{sessionStartBatch(transcriptUnset...)},
		},
		{
			name: "a transcript of the wrong type names none", event: "SessionStart",
			stdin: `{"transcript_path":7,"source":"startup"}`,
			want:  [][]string{sessionStartBatch(transcriptUnset...)},
		},
		{
			name: "a payload that cannot be read leaves the option alone", event: "SessionStart",
			stdin:   `{"transcript_path":"` + path + `"`,
			want:    [][]string{append(argv("set-option -p -t %3 @lt_state idle ;"), append(agentsSignal, append(rememberLayout, ";", "set-option", "-u", "-t", "%3", "@lt_branch")...)...)},
			wantLog: "hook SessionStart: hook: decode payload",
		},
		{
			name: "a prompt does not keep the transcript it names", event: "UserPromptSubmit",
			stdin: `{"transcript_path":"` + path + `","prompt":"go"}`,
			want:  [][]string{append(argv("set-option -p -t %3 @lt_state busy"), rememberLayout...)},
		},
		{
			name: "a subagent does not keep the transcript it names", event: "SubagentStop",
			stdin: `{"transcript_path":"` + path + `","agent_transcript_path":"/t/sub.jsonl"}`,
			want: [][]string{append([]string{
				"-S", testSocket, "-u",
				"set-option", "-p", "-t", "%3", "-F", "@lt_subagents", "#{?#{e|>:#{@lt_subagents},0},#{e|-:#{@lt_subagents},1},0}", ";",
			}, agentsSignal...)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			Run(context.Background(), Input{Event: tc.event, Stdin: strings.NewReader(tc.stdin), Getenv: env(nil)}, h.deps)
			if !slices.EqualFunc(h.tmux.calls, tc.want, slices.Equal) {
				t.Fatalf("tmux calls\n%q\nwant\n%q", h.tmux.calls, tc.want)
			}
			logged := h.log(t)
			if tc.wantLog == "" && logged != "" || !strings.Contains(logged, tc.wantLog) {
				t.Fatalf("log %q; want %q", logged, tc.wantLog)
			}
		})
	}
}

// TestRunNeverKeepsARefusedTranscript sends a session start naming a path
// that breaks one of the rules: the path reaches no tmux argument, the pane is
// left with no transcript, and the log says why.
func TestRunNeverKeepsARefusedTranscript(t *testing.T) {
	for _, tc := range refusedTranscripts {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			stdin := mustJSON(map[string]string{"transcript_path": tc.path, "source": "startup"})
			Run(context.Background(), Input{Event: "SessionStart", Stdin: strings.NewReader(stdin), Getenv: env(nil)}, h.deps)
			if len(h.tmux.calls) != 1 {
				t.Fatalf("tmux calls %q", h.tmux.calls)
			}
			for _, arg := range h.tmux.calls[0] {
				if strings.Contains(arg, "jsonl") || strings.Contains(arg, tc.path) {
					t.Fatalf("the refused path reached tmux: %q", h.tmux.calls[0])
				}
			}
			if !slices.Equal(h.tmux.calls[0], sessionStartBatch(transcriptUnset...)) {
				t.Fatalf("tmux call\n%q\nwant\n%q", h.tmux.calls[0], sessionStartBatch(transcriptUnset...))
			}
			if logged := h.log(t); !strings.Contains(logged, "hook SessionStart: transcript path not kept") {
				t.Fatalf("log %q", logged)
			}
		})
	}
}

// refusedTranscripts are paths a pane never keeps, one per rule.
var refusedTranscripts = []struct {
	name, path string
}{
	{name: "relative", path: "projects/-work-api/s.jsonl"},
	{name: "a parent step", path: "/home/u/.claude/projects/../../../etc/s.jsonl"},
	{name: "a doubled slash", path: "/home/u/.claude//projects/s.jsonl"},
	{name: "a current directory step", path: "/home/u/./.claude/projects/s.jsonl"},
	{name: "a trailing slash", path: "/home/u/.claude/projects/s.jsonl/"},
	{name: "not a transcript", path: "/home/u/.claude/settings.json"},
	{name: "an extension alone", path: "/home/u/.claude/projects/.jsonl"},
	{name: "an extension in the middle", path: "/home/u/.claude/projects/s.jsonl.bak"},
	{name: "an escape sequence", path: "/home/u/\x1b]52;c;aGk=\x07/s.jsonl"},
	{name: "a newline", path: "/home/u/a\n/s.jsonl"},
	{name: "a tab", path: "/home/u/a\t/s.jsonl"},
	{name: "a bidirectional override", path: "/home/u/\u202egnp/s.jsonl"},
	{name: "longer than the bound", path: "/" + strings.Repeat("d", maxTranscript) + "/s.jsonl"},
}

// TestTranscriptRules checks every rule of a kept transcript path on its own,
// the bound at its exact edge and a path that is not UTF-8, which no JSON
// payload can carry.
func TestTranscriptRules(t *testing.T) {
	atBound := "/" + strings.Repeat("d", maxTranscript-len("//s.jsonl")) + "/s.jsonl"
	cases := []struct {
		name, path string
		kept       bool
	}{
		{name: "a transcript", path: "/home/u/.claude/projects/-work-api/5f0c2a1e.jsonl", kept: true},
		{name: "a subagent transcript", path: "/home/u/.claude/projects/-work-api/5f0c2a1e/subagents/agent-a1.jsonl", kept: true},
		{name: "spaces, a hash and an accent", path: "/home/José/My Projects #2/s.jsonl", kept: true},
		{name: "exactly at the bound", path: atBound, kept: true},
		{name: "one past the bound", path: "/d" + atBound},
		{name: "nothing", path: ""},
		{name: "not UTF-8", path: "/home/u/\xff/s.jsonl"},
	}
	for _, r := range refusedTranscripts {
		cases = append(cases, struct {
			name, path string
			kept       bool
		}{name: r.name, path: r.path})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			got := h.deps.withDefaults().transcript("hook SessionStart", tc.path)
			if want := map[bool]string{true: tc.path}[tc.kept]; got != want {
				t.Fatalf("transcript(%q) = %q, want %q", tc.path, got, want)
			}
			if tc.kept || tc.path == "" {
				if logged := h.log(t); logged != "" {
					t.Fatalf("a path kept or absent was logged: %q", logged)
				}
			}
		})
	}
	if len(atBound) != maxTranscript {
		t.Fatalf("the path at the bound is %d bytes", len(atBound))
	}
}

// TestSessionStartTranscriptOrder pins where the transcript goes in the batch:
// with the state of the session, before the branch, which a lookup that
// failed leaves out.
func TestSessionStartTranscriptOrder(t *testing.T) {
	path := "/t/s.jsonl"
	cmds, _ := Commands("SessionStart", Payload{Source: "startup"}, true, "%3", nil, &path, true)
	var names []string
	for _, c := range cmds {
		names = append(names, strings.Join(c[:min(len(c), 5)], " "))
	}
	want := []string{
		"set-option -p -t %3 @lt_state",
		"run-shell -C -t %3 wait-for -S 'lt-agents-#{session_id}'",
		strings.Join(tmux.RememberLayout("%3")[0][:5], " "),
		"set-option -p -t %3 @lt_transcript",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("batch %q, want %q", names, want)
	}
}
