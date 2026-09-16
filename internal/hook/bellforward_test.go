package hook

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// scriptTmux replies to successive tmux calls with successive results.
type scriptTmux struct {
	mu      sync.Mutex
	calls   [][]string
	replies []tmux.Result
}

func (s *scriptTmux) Exec(_ context.Context, _ string, args []string) (tmux.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string(nil), args...))
	if len(s.replies) == 0 {
		return tmux.Result{ExitCode: 1, Stderr: []byte("unexpected call")}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func reply(fields ...string) tmux.Result {
	return tmux.Result{Stdout: []byte(strings.Join(fields, fieldSep) + "\n")}
}

func TestBellForward(t *testing.T) {
	resolve := []string{"-S", testSocket, "display-message", "-p", "-t", "$4", "#{session_name}\x1f#{@lt_origin}\x1f#{@claude_origin}"}
	origin := func(target string) []string {
		return []string{"-S", testSocket, "display-message", "-p", "-t", target, "#{session_name}\x1f#{pane_tty}"}
	}
	cases := []struct {
		name     string
		session  string
		prefix   string
		env      map[string]string
		replies  []tmux.Result
		want     [][]string
		wantBell []string
		wantLog  string
	}{
		{
			name: "forwards to origin window", session: "$4",
			replies:  []tmux.Result{reply("claude-1a2b3c4d", "@7", ""), reply("work", "/dev/ttys012")},
			want:     [][]string{resolve, origin("@7")},
			wantBell: []string{"/dev/ttys012"},
		},
		{
			name: "falls back to upstream origin option", session: "$4",
			replies:  []tmux.Result{reply("claude-1a2b3c4d", "", "@2"), reply("main", "/dev/pts/9")},
			want:     [][]string{resolve, origin("@2")},
			wantBell: []string{"/dev/pts/9"},
		},
		{
			name: "own origin option wins", session: "$4",
			replies:  []tmux.Result{reply("claude-1a2b3c4d", "@3", "@2"), reply("main", "/dev/pts/9")},
			want:     [][]string{resolve, origin("@3")},
			wantBell: []string{"/dev/pts/9"},
		},
		{
			name: "pane origin accepted", session: "$4",
			replies:  []tmux.Result{reply("claude-1a2b3c4d", "%15", ""), reply("main", "/dev/pts/2")},
			want:     [][]string{resolve, origin("%15")},
			wantBell: []string{"/dev/pts/2"},
		},
		{
			name: "custom prefix", session: "$4", prefix: "ai-",
			replies:  []tmux.Result{reply("ai-1a2b3c4d", "@1", ""), reply("main", "/dev/pts/2")},
			want:     [][]string{resolve, origin("@1")},
			wantBell: []string{"/dev/pts/2"},
		},
		{
			name: "session without prefix does not forward", session: "$4",
			replies: []tmux.Result{reply("work", "@1", "")},
			want:    [][]string{resolve},
		},
		{
			name: "default prefix not used when custom prefix set", session: "$4", prefix: "ai-",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "@1", "")},
			want:    [][]string{resolve},
		},
		{
			name: "no origin recorded", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "", "")},
			want:    [][]string{resolve},
		},
		{
			name: "origin in another popup session is not forwarded", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "@9", ""), reply("claude-99999999", "/dev/pts/3")},
			want:    [][]string{resolve, origin("@9")},
		},
		{
			name: "origin window gone is quiet", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "@9", ""), {ExitCode: 1, Stderr: []byte("can't find window: @9")}},
			want:    [][]string{resolve, origin("@9")},
		},
		{
			name: "origin expands to nothing when gone", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "@9", ""), reply("", "")},
			want:    [][]string{resolve, origin("@9")},
		},
		{
			name: "origin not an ID", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "=work:1", "")},
			want:    [][]string{resolve},
			wantLog: `bell-forward: origin "=work:1" is not a window or pane ID`,
		},
		{
			name: "session lookup failure logged", session: "$4",
			replies: []tmux.Result{{ExitCode: 1, Stderr: []byte("can't find session: $4")}},
			want:    [][]string{resolve},
			wantLog: "bell-forward: tmux display-message: can't find session: $4",
		},
		{
			name: "origin lookup failure logged", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "@9", ""), {ExitCode: 1, Stderr: []byte("server exited unexpectedly")}},
			want:    [][]string{resolve, origin("@9")},
			wantLog: "bell-forward: tmux display-message: server exited unexpectedly",
		},
		{
			name: "unexpected reply shape", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d")},
			want:    [][]string{resolve},
		},
		{
			name: "unexpected origin reply shape", session: "$4",
			replies: []tmux.Result{reply("claude-1a2b3c4d", "@1", ""), reply("/dev/pts/1")},
			want:    [][]string{resolve, origin("@1")},
		},
		{name: "session name instead of ID", session: "claude-1a2b3c4d", wantLog: `bell-forward: invalid session ID "claude-1a2b3c4d"`},
		{name: "shell text instead of ID", session: "$(id)", wantLog: `bell-forward: invalid session ID "$(id)"`},
		{name: "empty session", session: "", wantLog: `bell-forward: invalid session ID ""`},
		{name: "outside tmux", session: "$4", env: map[string]string{"TMUX": "<unset>"}, wantLog: "bell-forward: not running inside tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			script := &scriptTmux{replies: tc.replies}
			h.deps.Tmux = func(s tmux.Socket) *tmux.Client {
				return tmux.New(tmux.Options{Socket: s, Executor: script})
			}
			status := BellForward(context.Background(), BellForwardInput{Session: tc.session, Prefix: tc.prefix, Getenv: env(tc.env)}, h.deps)
			if status != 0 {
				t.Fatalf("status %d", status)
			}
			if !reflect.DeepEqual(script.calls, tc.want) {
				t.Fatalf("tmux calls\n%q\nwant\n%q", script.calls, tc.want)
			}
			if !reflect.DeepEqual(h.tty.opened, tc.wantBell) {
				t.Fatalf("bell terminals %q; want %q", h.tty.opened, tc.wantBell)
			}
			logged := h.log(t)
			if tc.wantLog == "" && logged != "" || !strings.Contains(logged, tc.wantLog) {
				t.Fatalf("log %q; want %q", logged, tc.wantLog)
			}
		})
	}
}

func TestBellForwardLogsBellFailureAndPanics(t *testing.T) {
	h := newHarness(t)
	script := &scriptTmux{replies: []tmux.Result{reply("claude-1a2b3c4d", "@7", ""), reply("work", "/dev/ttys012")}}
	h.deps.Tmux = func(s tmux.Socket) *tmux.Client { return tmux.New(tmux.Options{Socket: s, Executor: script}) }
	h.tty.openErr = errNotTTY
	BellForward(context.Background(), BellForwardInput{Session: "$4", Getenv: env(nil)}, h.deps)
	if logged := h.log(t); !strings.Contains(logged, "bell-forward: bell: not a terminal device") {
		t.Fatalf("log %q", logged)
	}

	h = newHarness(t)
	h.deps.Now = func() time.Time { return fixedNow }
	h.deps.Tmux = func(tmux.Socket) *tmux.Client { panic("boom") }
	if status := BellForward(context.Background(), BellForwardInput{Session: "$4", Getenv: env(nil)}, h.deps); status != 0 {
		t.Fatalf("status %d", status)
	}
	if logged := h.log(t); !strings.Contains(logged, "bell-forward: internal error: boom") {
		t.Fatalf("log %q", logged)
	}
	// A nil environment means outside tmux.
	h = newHarness(t)
	BellForward(context.Background(), BellForwardInput{Session: "$1"}, h.deps)
	if logged := h.log(t); !strings.Contains(logged, "not running inside tmux") {
		t.Fatalf("log %q", logged)
	}
}
