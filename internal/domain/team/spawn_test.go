package team_test

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

func TestParseSpawn(t *testing.T) {
	// The line Claude Code appends to a teammate's launcher.
	spawned := []string{
		"--agent-id", "review-api@session-8f3c1d2a", "--agent-name", "review-api",
		"--team-name", "session-8f3c1d2a", "--agent-color", "cyan",
		"--parent-session-id", "8f3c1d2a-77b1-4d0e-9a55-5c6d2f1e0b34",
		"--agent-type", "api-developer", "--permission-mode", "acceptEdits",
		"--model", "opus", "--effort", "high",
	}
	cases := []struct {
		name string
		args []string
		want team.Spawn
	}{
		{
			name: "the arguments a teammate is started with",
			args: spawned,
			want: team.Spawn{
				AgentID: "review-api@session-8f3c1d2a", Name: "review-api", AgentType: "api-developer",
				Team: "session-8f3c1d2a", Color: "cyan", Model: "opus",
			},
		},
		{
			name: "every flag joined to its value",
			args: []string{"--agent-id=a@t", "--agent-name=a", "--agent-type=general-purpose", "--team-name=t", "--agent-color=magenta", "--model=sonnet"},
			want: team.Spawn{AgentID: "a@t", Name: "a", AgentType: "general-purpose", Team: "t", Color: "magenta", Model: "sonnet"},
		},
		{
			name: "a flag we do not read keeps its value to itself",
			args: []string{"--settings", "/tmp/s.json", "--agent-name", "kept"},
			want: team.Spawn{Name: "kept"},
		},
		{
			name: "the last spelling of a flag wins",
			args: []string{"--agent-name", "first", "--agent-name=second"},
			want: team.Spawn{Name: "second"},
		},
		{
			name: "a flag is never the value of another flag",
			args: []string{"--agent-name", "--plan-mode-required"},
			want: team.Spawn{},
		},
		{
			name: "a name behind the flag that was passed over",
			args: []string{"--agent-name", "--agent-name", "kept"},
			want: team.Spawn{Name: "kept"},
		},
		{
			name: "an empty value is read as one",
			args: []string{"--agent-type=", "--agent-name", ""},
			want: team.Spawn{},
		},
		{
			name: "a flag with nothing after it",
			args: []string{"--agent-type", "reviewer", "--agent-name"},
			want: team.Spawn{AgentType: "reviewer"},
		},
		{name: "no arguments at all", args: nil, want: team.Spawn{}},
		{
			name: "a flag of another shape is not one of ours",
			args: []string{"-agent-name", "short", "---agent-name", "long", "--agent-nameX", "wide"},
			want: team.Spawn{},
		},
		{
			name: "the team name of a session with a directory in it",
			args: []string{"--team-name", "session-../../etc", "--agent-name", "a/../b"},
			want: team.Spawn{Team: "session-../../etc", Name: "a/../b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := team.ParseSpawn(tc.args); got != tc.want {
				t.Fatalf("ParseSpawn(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func FuzzParseSpawn(f *testing.F) {
	f.Add("--agent-id a@t --agent-name a --agent-type t --team-name s --agent-color red --model opus")
	f.Add("--agent-name=a --model")
	f.Add("")
	f.Fuzz(func(t *testing.T, line string) {
		args := strings.Split(line, " ")
		got := team.ParseSpawn(args)
		// Every value read is one of the arguments, whole: nothing is built,
		// joined or cut here, so nothing can be read as more than it is.
		for _, v := range []string{got.AgentID, got.Name, got.AgentType, got.Team, got.Color, got.Model} {
			if v == "" {
				continue
			}
			found := false
			for _, a := range args {
				if a == v || strings.HasSuffix(a, "="+v) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("ParseSpawn(%q) read %q, which is not an argument", args, v)
			}
		}
	})
}
