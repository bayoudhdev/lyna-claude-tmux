package cli

import (
	"errors"
	"slices"
	"testing"
)

func TestTeammateArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		rest    []string
		wantErr bool
	}{
		{
			name: "the line the launcher execs",
			args: []string{"--claude", "/opt/claude", "--", "--agent-name", "review-api"},
			want: "/opt/claude", rest: []string{"--agent-name", "review-api"},
		},
		{
			name: "the agent joined to its flag",
			args: []string{"--claude=/opt/claude", "--", "--agent-name=review-api"},
			want: "/opt/claude", rest: []string{"--agent-name=review-api"},
		},
		{
			name: "a teammate with no arguments of its own",
			args: []string{"--claude", "/opt/claude", "--"},
			want: "/opt/claude", rest: []string{},
		},
		{
			name: "nothing after the agent",
			args: []string{"--claude", "/opt/claude"},
			want: "/opt/claude",
		},
		{
			name: "a flag of ours after the separator belongs to the agent",
			args: []string{"--claude", "/opt/claude", "--", "--claude", "/opt/other", "--"},
			want: "/opt/claude", rest: []string{"--claude", "/opt/other", "--"},
		},
		{
			name: "a path holding a space",
			args: []string{"--claude", "/my tools/claude", "--", "--agent-id", "a@t"},
			want: "/my tools/claude", rest: []string{"--agent-id", "a@t"},
		},
		{name: "no agent named", args: []string{"--", "--agent-name", "a"}, wantErr: true},
		{name: "no arguments at all", args: nil, wantErr: true},
		{name: "the flag with nothing after it", args: []string{"--claude"}, wantErr: true},
		{name: "a flag this command does not have", args: []string{"--socket", "x", "--claude", "/c"}, wantErr: true},
		{name: "an argument that is not a flag", args: []string{"/opt/claude"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, rest, err := teammateArgs(tc.args)
			if tc.wantErr {
				if !errors.Is(err, errTeammateArgs) {
					t.Fatalf("teammateArgs(%q) = %q, %q, %v", tc.args, path, rest, err)
				}
				return
			}
			if err != nil || path != tc.want || !slices.Equal(rest, tc.rest) {
				t.Fatalf("teammateArgs(%q) = %q, %q, %v; want %q, %q", tc.args, path, rest, err, tc.want, tc.rest)
			}
		})
	}
}

// TestTeammateCommand runs the command the launcher runs, outside a workspace,
// and checks the one thing that has to hold everywhere: the process becomes
// the agent it was told to run, with the arguments it was given.
func TestTeammateCommand(t *testing.T) {
	e := newCLIEnv(t)
	code, stdout, stderr := e.run(t, "teammate", "--claude", "/opt/claude", "--",
		"--agent-id", "review-api@session-1", "--agent-name", "review-api")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	want := []string{"/opt/claude", "/opt/claude", "--agent-id", "review-api@session-1", "--agent-name", "review-api"}
	if len(e.execs) != 1 || !slices.Equal(e.execs[0], want) {
		t.Fatalf("execs %q, want one %q", e.execs, want)
	}
}

func TestTeammateCommandRefuses(t *testing.T) {
	e := newCLIEnv(t)
	code, _, stderr := e.run(t, "teammate", "--", "--agent-name", "review-api")
	if code == 0 || !containsFolded(stderr, "--claude") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(e.execs) != 0 {
		t.Fatalf("a teammate was started without an agent: %q", e.execs)
	}
}
