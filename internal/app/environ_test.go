package app

import (
	"reflect"
	"testing"
)

func TestServerEnviron(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "empty", in: nil, want: []string{}},
		{name: "user environment kept in order", in: []string{"PATH=/bin", "HOME=/h", "CLAUDE_CODE_USE_BEDROCK=1", "LANG=C.UTF-8"}, want: []string{"PATH=/bin", "HOME=/h", "CLAUDE_CODE_USE_BEDROCK=1", "LANG=C.UTF-8"}},
		{name: "outer tmux removed", in: []string{"TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=%1", "TERM=xterm"}, want: []string{"TERM=xterm"}},
		{name: "managed pane markers removed", in: []string{"LYNA_TMUX_MANAGED=1", "LYNA_TMUX_SOCKET=/s", "LYNA_TMUX_SESSION=api", "LYNA_TMUX_SANDBOX=strict", "LYNA_TMUX_HOME=/keep"}, want: []string{"LYNA_TMUX_HOME=/keep"}},
		{
			name: "parent Claude Code session removed",
			in: []string{
				"CLAUDECODE=1", "CLAUDE_PID=42", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_EXECPATH=/x", "CLAUDE_CODE_SESSION_ID=s",
				"CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_SESSION_ATTENDED=1", "CLAUDE_CODE_MESSAGING_SOCKET=/sock",
				"CLAUDE_CODE_MESSAGING_TOKEN=secret", "CLAUDE_CONFIG_DIR=/cfg",
			},
			want: []string{"CLAUDE_CONFIG_DIR=/cfg"},
		},
		{name: "malformed entries dropped", in: []string{"NOEQUALS", "=value", "A="}, want: []string{"A="}},
		{name: "prefix match is not enough", in: []string{"TMUX_PANE_X=1", "CLAUDECODE_X=1"}, want: []string{"TMUX_PANE_X=1", "CLAUDECODE_X=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServerEnviron(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ServerEnviron() = %q, want %q", got, tc.want)
			}
		})
	}
}
