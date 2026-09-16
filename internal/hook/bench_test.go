package hook

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// nopExecutor answers every tmux call with success, so benchmarks measure the
// handler rather than a process spawn.
type nopExecutor struct{ stdout []byte }

func (e nopExecutor) Exec(context.Context, string, []string) (tmux.Result, error) {
	return tmux.Result{Stdout: e.stdout}, nil
}

type nopTTY struct{}

func (nopTTY) Write(p []byte) (int, error) { return len(p), nil }
func (nopTTY) Close() error                { return nil }

func BenchmarkRun(b *testing.B) {
	getenv := env(nil)
	cases := []struct {
		name    string
		event   string
		payload string
	}{
		// The most frequent hook: a file edit returns the agent to busy and
		// signals the changes pane.
		{name: "PostToolUse", event: "PostToolUse", payload: `{"session_id":"abc123","transcript_path":"/t.jsonl","cwd":"/u/project","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"/u/project/main.go","content":"` + strings.Repeat("x", 4096) + `"},"tool_response":{"success":true}}`},
		// Tool calls the handler ignores decode the payload and stop.
		{name: "PreToolUseIgnored", event: "PreToolUse", payload: `{"session_id":"abc123","cwd":"/u/project","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/u/project/main.go"}}`},
		{name: "Stop", event: "Stop", payload: `{"session_id":"abc123","cwd":"/u/project","hook_event_name":"Stop","stop_hook_active":false}`},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			deps := Deps{
				Tmux: func(s tmux.Socket) *tmux.Client {
					return tmux.New(tmux.Options{Bin: "tmux", Socket: s, Executor: nopExecutor{stdout: []byte("/dev/pts/1\n")}})
				},
				Getwd:   func() (string, error) { return "/u/project", nil },
				OpenTTY: func(string) (io.WriteCloser, error) { return nopTTY{}, nil },
			}
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				Run(ctx, Input{Event: bc.event, Stdin: strings.NewReader(bc.payload), Getenv: getenv}, deps)
			}
		})
	}
}

func BenchmarkBellForward(b *testing.B) {
	deps := Deps{
		Tmux: func(s tmux.Socket) *tmux.Client {
			return tmux.New(tmux.Options{Bin: "tmux", Socket: s, Executor: bellForwardExecutor{}})
		},
		OpenTTY: func(string) (io.WriteCloser, error) { return nopTTY{}, nil },
	}
	ctx := context.Background()
	in := BellForwardInput{Session: "$4", Getenv: env(nil)}
	b.ReportAllocs()
	for b.Loop() {
		BellForward(ctx, in, deps)
	}
}

// bellForwardExecutor answers the two display-message calls of a forwarded bell.
type bellForwardExecutor struct{}

func (bellForwardExecutor) Exec(_ context.Context, _ string, args []string) (tmux.Result, error) {
	if strings.HasSuffix(args[len(args)-1], "#{pane_tty}") {
		return reply("work", "/dev/pts/1"), nil
	}
	return reply("claude-1a2b3c4d", "@7", ""), nil
}
