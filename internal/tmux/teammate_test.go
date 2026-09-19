package tmux_test

import (
	"slices"
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

func TestRememberLayout(t *testing.T) {
	want := "set-option -w -t %4 -F @lt_wlayout " +
		"'#{?#{m:*x*,#{P:#{?#{==:#{@lt_role},teammate},x,}}},#{@lt_wlayout},#{window_layout}}'"
	if got := tmux.RememberLayout("%4").String(); got != want {
		t.Fatalf("RememberLayout:\n got %s\nwant %s", got, want)
	}
}

func TestTileAgents(t *testing.T) {
	cases := []struct {
		name               string
		window, lead, rail string
		railWidth          int
		want               string
	}{
		{
			name: "a window shared by a lead and its teammates", window: "@2", lead: "%1",
			want: "select-layout -t @2 main-vertical ; resize-pane -t %1 -x 40%",
		},
		{
			name: "a window that carries the rail", window: "@2", lead: "%1", rail: "%9",
			want: "select-layout -t @2 main-vertical ; resize-pane -t %9 -x 28",
		},
		{
			name: "a rail the workspace made wider", window: "@2", lead: "%1", rail: "%9", railWidth: 44,
			want: "select-layout -t @2 main-vertical ; resize-pane -t %9 -x 44",
		},
		{
			name: "a width with no rail to give it to", window: "@2", lead: "%1", railWidth: 44,
			want: "select-layout -t @2 main-vertical ; resize-pane -t %1 -x 40%",
		},
		{
			name: "a rail named instead of identified", window: "@2", lead: "%1", rail: "agents",
			want: "select-layout -t @2 main-vertical ; resize-pane -t %1 -x 40%",
		},
		{name: "a lead named instead of identified", window: "@2", lead: "claude", want: ""},
		{name: "no lead at all", window: "@2", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.TileAgents(tc.window, tc.lead, tc.rail, tc.railWidth).String(); got != tc.want {
				t.Fatalf("TileAgents:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestBreakOutTeammate(t *testing.T) {
	const remembered = "cf3a,200x50,0,0{100x50,0,0,0,99x50,101,0,1}"
	cases := []struct {
		name       string
		pane       string
		window     string
		agent      string
		remembered string
		want       string
	}{
		{
			name: "a teammate that gets a window of its own", pane: "%9", window: "@2",
			agent: "review-api", remembered: remembered,
			want: "break-pane -d -n review-api -s %9 ; select-layout -t @2 '" + remembered + "'",
		},
		{
			name: "a teammate whose arguments carried no name", pane: "%9", window: "@2", remembered: remembered,
			want: "break-pane -d -s %9 ; select-layout -t @2 '" + remembered + "'",
		},
		{
			name: "a window whose arrangement nobody stored", pane: "%9", window: "@2", agent: "review-api",
			want: "break-pane -d -n review-api -s %9",
		},
		{
			name: "a name that would end the command", pane: "%9", window: "@2", agent: "a ; kill-server",
			want: "break-pane -d -n 'a ; kill-server' -s %9",
		},
		{name: "a pane named instead of identified", pane: "review-api", window: "@2", want: ""},
		{name: "a window named instead of identified", pane: "%9", window: "agents", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tmux.BreakOutTeammate(tc.pane, tc.window, tc.agent, tc.remembered).String()
			if got != tc.want {
				t.Fatalf("BreakOutTeammate:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestOpenRail(t *testing.T) {
	proc := tmux.PaneProcess{Argv: []string{"/opt/lmux", "agents", "--rail", "--auto"}}
	cases := []struct {
		name, anchor string
		width        int
		proc         tmux.PaneProcess
		want         tmux.Command
	}{
		{
			name: "beside the leftmost pane of a window", anchor: "%4", proc: proc,
			want: tmux.Command{
				"split-window", "-b", "-h", "-d", "-P", "-F", "#{pane_id}", "-l", "28", "-t", "%4",
				"--", "/opt/lmux", "agents", "--rail", "--auto",
			},
		},
		{
			name: "at the width the workspace asks for", anchor: "%4", width: 44, proc: proc,
			want: tmux.Command{
				"split-window", "-b", "-h", "-d", "-P", "-F", "#{pane_id}", "-l", "44", "-t", "%4",
				"--", "/opt/lmux", "agents", "--rail", "--auto",
			},
		},
		{
			name: "no narrower than the narrowest rail", anchor: "%4", width: 3, proc: proc,
			want: tmux.Command{
				"split-window", "-b", "-h", "-d", "-P", "-F", "#{pane_id}", "-l", "20", "-t", "%4",
				"--", "/opt/lmux", "agents", "--rail", "--auto",
			},
		},
		{
			name: "a rail given the environment of its pane", anchor: "%4",
			proc: tmux.PaneProcess{Argv: []string{"/opt/lmux", "agents", "--rail"}, Env: []string{"LYNA_TMUX_SESSION=api"}},
			want: tmux.Command{
				"split-window", "-b", "-h", "-d", "-P", "-F", "#{pane_id}", "-l", "28", "-t", "%4",
				"-e", "LYNA_TMUX_SESSION=api", "--", "/opt/lmux", "agents", "--rail",
			},
		},
		{name: "a pane named instead of identified", anchor: "{left}", proc: proc},
		{name: "no pane at all", proc: proc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tmux.OpenRail(tc.anchor, tc.width, tc.proc)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("OpenRail:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestAdoptRail(t *testing.T) {
	cases := []struct {
		name, pane, want string
	}{
		{name: "a pane of ours", pane: "%7", want: "set-option -p -t %7 @lt_role agents"},
		{name: "a pane named instead of identified", pane: "{top-left}"},
		{name: "no pane at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.AdoptRail(tc.pane).String(); got != tc.want {
				t.Fatalf("AdoptRail = %q, want %q", got, tc.want)
			}
		})
	}
}
