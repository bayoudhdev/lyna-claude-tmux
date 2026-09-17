package tmux_test

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestAdoptTeammate(t *testing.T) {
	cases := []struct {
		name     string
		teammate tmux.Teammate
		want     string
	}{
		{
			name:     "a named teammate of a team",
			teammate: tmux.Teammate{Pane: "%7", Agent: "review-api", AgentType: "api-developer", Team: "session-8f3c1d2a"},
			want: "set-option -p -t %7 @lt_role teammate ; set-option -p -t %7 @lt_state busy ; " +
				"set-option -p -t %7 @lt_agent review-api ; set-option -p -t %7 @lt_agent_type api-developer ; " +
				"set-option -p -t %7 @lt_team session-8f3c1d2a ; set-option -p -t %7 remain-on-exit failed ; " +
				"set-option -p -t %7 allow-passthrough on ; set-option -p -u -t %7 window-style ; " +
				"set-option -p -u -t %7 pane-border-style ; set-option -p -u -t %7 pane-active-border-style ; " +
				"set-option -p -u -t %7 pane-border-format ; " +
				"run-shell -C -t %7 'wait-for -S '\\''lt-agents-#{session_id}'\\'''",
		},
		{
			name:     "a teammate whose arguments carried no name",
			teammate: tmux.Teammate{Pane: "%0"},
			want: "set-option -p -t %0 @lt_role teammate ; set-option -p -t %0 @lt_state busy ; " +
				"set-option -p -t %0 remain-on-exit failed ; set-option -p -t %0 allow-passthrough on ; " +
				"set-option -p -u -t %0 window-style ; set-option -p -u -t %0 pane-border-style ; " +
				"set-option -p -u -t %0 pane-active-border-style ; set-option -p -u -t %0 pane-border-format ; " +
				"run-shell -C -t %0 'wait-for -S '\\''lt-agents-#{session_id}'\\'''",
		},
		{name: "a pane named instead of identified", teammate: tmux.Teammate{Pane: "api", Agent: "a"}, want: ""},
		{name: "a pane selected by pattern", teammate: tmux.Teammate{Pane: "%", Agent: "a"}, want: ""},
		{name: "no pane at all", teammate: tmux.Teammate{Agent: "a"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tmux.AdoptTeammate(tc.teammate).String()
			if got != tc.want {
				t.Fatalf("AdoptTeammate:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestAdoptTeammateEscapes drives what a teammate's own name can do to the
// commands it is labeled with: the name comes from whoever asked for the
// teammate, so it is shortened, stripped of control characters and escaped
// for the border that draws it, and it is one token whatever it holds.
func TestAdoptTeammateEscapes(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		want  string
	}{
		{name: "a command of its own", agent: "a ; kill-server", want: "'a ; kill-server'"},
		{name: "a format in the name", agent: "#{pane_pid}", want: `'##{pane_pid}'`},
		{name: "a quote and a backslash", agent: `a'b\c`, want: `'a'\''b\c'`},
		{name: "a newline", agent: "a\nb", want: "ab"},
		{name: "a name longer than a border", agent: strings.Repeat("n", 40), want: strings.Repeat("n", 24) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tmux.AdoptTeammate(tmux.Teammate{Pane: "%1", Agent: tc.agent}).String()
			want := "set-option -p -t %1 @lt_agent " + tc.want + " ;"
			if !strings.Contains(got, want) {
				t.Fatalf("AdoptTeammate:\n got %s\nwant a command %s", got, want)
			}
		})
	}
}
