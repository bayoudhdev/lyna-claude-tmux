package hook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

const testSocket = "/tmp/tmux-501/default"

var fixedNow = time.Date(2026, 9, 15, 12, 30, 0, 0, time.UTC)

// fakeTmux records tmux invocations and replies with a canned result.
type fakeTmux struct {
	mu     sync.Mutex
	calls  [][]string
	result tmux.Result
	err    error
}

func (f *fakeTmux) Exec(_ context.Context, _ string, args []string) (tmux.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	return f.result, f.err
}

// fakeTTY records bells.
type fakeTTY struct {
	mu      sync.Mutex
	opened  []string
	written bytes.Buffer
	openErr error
}

type ttyWriter struct {
	t *fakeTTY
}

func (w ttyWriter) Write(p []byte) (int, error) {
	w.t.mu.Lock()
	defer w.t.mu.Unlock()
	return w.t.written.Write(p)
}

func (ttyWriter) Close() error { return nil }

func (f *fakeTTY) open(path string) (io.WriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.opened = append(f.opened, path)
	return ttyWriter{f}, nil
}

type harness struct {
	tmux    *fakeTmux
	tty     *fakeTTY
	logPath string
	deps    Deps
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{tmux: &fakeTmux{}, tty: &fakeTTY{}, logPath: filepath.Join(t.TempDir(), "state", "lyna-tmux.log")}
	h.deps = Deps{
		Tmux: func(s tmux.Socket) *tmux.Client {
			return tmux.New(tmux.Options{Bin: "tmux", Socket: s, Executor: h.tmux})
		},
		Now:     func() time.Time { return fixedNow },
		LogPath: h.logPath,
		ReadFile: func(string, int64) ([]byte, error) {
			return nil, fs.ErrNotExist
		},
		Getwd:   func() (string, error) { return "/work", nil },
		OpenTTY: h.tty.open,
	}
	return h
}

func (h *harness) log(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.logPath)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func env(overrides map[string]string) func(string) string {
	base := map[string]string{"TMUX": testSocket + ",4242,0", "TMUX_PANE": "%3"}
	for k, v := range overrides {
		if v == "<unset>" {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func argv(cmds ...string) []string {
	return append([]string{"-S", testSocket, "-u"}, strings.Fields(strings.Join(cmds, " ; "))...)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin broken") }

// agentsSignal is the command the handler adds when the agents of a session
// change: a session starting or ending, a subagent, a teammate, a task.
var agentsSignal = []string{"run-shell", "-C", "-t", "%3", "wait-for -S 'lt-agents-#{session_id}'"}

func TestRun(t *testing.T) {
	signal := []string{"run-shell", "-C", "-t", "%3", "wait-for -S 'lt-changes-#{session_id}'"}
	cases := []struct {
		name     string
		event    string
		plugin   bool
		env      map[string]string
		stdin    io.Reader
		result   tmux.Result
		execErr  error
		openErr  error
		want     [][]string // exact argv of every tmux call
		wantBell []string   // terminals the bell was written to
		wantLog  []string   // substrings of the log; nil means the log stays empty
	}{
		{
			name: "unknown event", event: "PreCompact", stdin: strings.NewReader("{}"),
			wantLog: []string{`hook: unknown event "PreCompact"`},
		},
		{
			name: "plugin inside managed pane is a no-op", event: "Stop", plugin: true,
			env: map[string]string{"LYNA_TMUX_MANAGED": "1"}, stdin: strings.NewReader("{}"),
		},
		{
			name: "plugin outside managed pane runs", event: "UserPromptSubmit", plugin: true,
			stdin: strings.NewReader(`{"prompt":"hi"}`),
			want:  [][]string{argv("set-option -p -t %3 @lt_state busy")},
		},
		{
			name: "managed settings hook runs inside managed pane", event: "UserPromptSubmit",
			env: map[string]string{"LYNA_TMUX_MANAGED": "1"}, stdin: strings.NewReader(`{}`),
			want: [][]string{argv("set-option -p -t %3 @lt_state busy")},
		},
		{
			name: "outside tmux", event: "Stop", env: map[string]string{"TMUX": "<unset>"},
			stdin: strings.NewReader("{}"),
		},
		{
			name: "no pane", event: "Stop", env: map[string]string{"TMUX_PANE": "<unset>"},
			stdin: strings.NewReader("{}"),
		},
		{
			name: "relative socket", event: "Stop", env: map[string]string{"TMUX": "default,1,0"},
			stdin: strings.NewReader("{}"),
		},
		{
			name: "invalid pane", event: "Stop", env: map[string]string{"TMUX_PANE": "%3;kill-server"},
			stdin:   strings.NewReader("{}"),
			wantLog: []string{`hook Stop: invalid TMUX_PANE "%3;kill-server"`},
		},
		{
			name: "missing stdin", event: "PostToolUse",
			want:    [][]string{append(argv("set-option -p -t %3 @lt_state busy ;"), signal...)},
			wantLog: []string{"hook PostToolUse: no payload on stdin"},
		},
		{
			name: "malformed stdin still updates state", event: "UserPromptSubmit",
			stdin:   strings.NewReader("{not json"),
			want:    [][]string{argv("set-option -p -t %3 @lt_state busy")},
			wantLog: []string{"hook UserPromptSubmit: hook: decode payload"},
		},
		{
			name: "oversized stdin", event: "PostToolUse",
			stdin:   io.MultiReader(strings.NewReader(`{"tool_name":"Read","tool_response":"`), strings.NewReader(strings.Repeat("x", MaxStdinBytes))),
			want:    [][]string{append(argv("set-option -p -t %3 @lt_state busy ;"), signal...)},
			wantLog: []string{"read payload: fsx: input exceeds size limit"},
		},
		{
			name: "unreadable stdin", event: "UserPromptSubmit", stdin: errReader{},
			want:    [][]string{argv("set-option -p -t %3 @lt_state busy")},
			wantLog: []string{"read payload: stdin broken"},
		},
		{
			name: "event needing no work skips tmux", event: "PostToolUse",
			stdin: strings.NewReader(`{"tool_name":"Glob","agent_id":"a1"}`),
		},
		{
			name: "subagent counter", event: "SubagentStart",
			stdin: strings.NewReader(`{"agent_id":"a1","agent_type":"Explore"}`),
			want:  [][]string{append([]string{"-S", testSocket, "-u", "set-option", "-p", "-t", "%3", "-F", "@lt_subagents", fmtSubagentsInc, ";"}, agentsSignal...)},
		},
		{
			name: "stop rings the pane terminal", event: "Stop",
			stdin:    strings.NewReader(`{"cwd":"/work"}`),
			result:   tmux.Result{Stdout: []byte("/dev/ttys009\n")},
			want:     [][]string{argv("set-option -p -t %3 @lt_state idle ; set-option -u -t %3 @lt_branch ; display-message -p -t %3 #{pane_tty}")},
			wantBell: []string{"/dev/ttys009"},
		},
		{
			name: "bell disabled", event: "PermissionRequest", env: map[string]string{"LYNA_TMUX_BELL": "0"},
			stdin: strings.NewReader(`{"tool_name":"Bash"}`),
			want:  [][]string{argv("set-option -p -t %3 @lt_state waiting")},
		},
		{
			name: "bell enabled by any other value", event: "PermissionRequest", env: map[string]string{"LYNA_TMUX_BELL": "off"},
			stdin:    strings.NewReader(`{"tool_name":"Bash"}`),
			result:   tmux.Result{Stdout: []byte("/dev/pts/4\n")},
			want:     [][]string{argv("set-option -p -t %3 @lt_state waiting ; display-message -p -t %3 #{pane_tty}")},
			wantBell: []string{"/dev/pts/4"},
		},
		{
			name: "bell terminal cannot be opened", event: "Notification",
			stdin:   strings.NewReader(`{"notification_type":"permission_prompt"}`),
			result:  tmux.Result{Stdout: []byte("/dev/pts/4\n")},
			openErr: errors.New("permission denied"),
			want:    [][]string{argv("set-option -p -t %3 @lt_state waiting ; display-message -p -t %3 #{pane_tty}")},
			wantLog: []string{"hook Notification: bell: permission denied"},
		},
		{
			name: "tmux failure is logged", event: "Stop", stdin: strings.NewReader(`{"cwd":"/work"}`),
			result:  tmux.Result{ExitCode: 1, Stderr: []byte("no such pane: %3")},
			want:    [][]string{argv("set-option -p -t %3 @lt_state idle ; set-option -u -t %3 @lt_branch ; display-message -p -t %3 #{pane_tty}")},
			wantLog: []string{"hook Stop: tmux set-option: no such pane: %3"},
		},
		{
			name: "tmux missing is logged", event: "UserPromptSubmit", stdin: strings.NewReader(`{}`),
			execErr: tmux.ErrNotInstalled,
			want:    [][]string{argv("set-option -p -t %3 @lt_state busy")},
			wantLog: []string{"tmux: not installed"},
		},
		{
			name: "session end after pane closed is quiet", event: "SessionEnd", stdin: strings.NewReader(`{"reason":"other"}`),
			result: tmux.Result{ExitCode: 1, Stderr: []byte("no such pane: %3")},
			want:   [][]string{append(argv("set-option -p -u -t %3 @lt_state ; set-option -p -u -t %3 @lt_subagents ;"), agentsSignal...)},
		},
		{
			name: "session end after session closed is quiet", event: "SessionEnd", stdin: strings.NewReader(`{"reason":"other"}`),
			result: tmux.Result{ExitCode: 1, Stderr: []byte("no such session: %3")},
			want:   [][]string{append(argv("set-option -p -u -t %3 @lt_state ; set-option -p -u -t %3 @lt_subagents ;"), agentsSignal...)},
		},
		{
			name: "session end with target lookup failure is quiet", event: "SessionEnd", stdin: strings.NewReader(`{"reason":"other"}`),
			result: tmux.Result{ExitCode: 1, Stderr: []byte("can't find pane: %3")},
			want:   [][]string{append(argv("set-option -p -u -t %3 @lt_state ; set-option -p -u -t %3 @lt_subagents ;"), agentsSignal...)},
		},
		{
			name: "session end when tmux cannot run is logged", event: "SessionEnd", stdin: strings.NewReader(`{"reason":"other"}`),
			execErr: tmux.ErrNotInstalled,
			want:    [][]string{append(argv("set-option -p -u -t %3 @lt_state ; set-option -p -u -t %3 @lt_subagents ;"), agentsSignal...)},
			wantLog: []string{"hook SessionEnd: tmux set-option: tmux: not installed"},
		},
		{
			name: "session end after server exit is quiet", event: "SessionEnd", stdin: strings.NewReader(`{"reason":"other"}`),
			result: tmux.Result{ExitCode: 1, Stderr: []byte("no server running on /tmp/tmux-501/default")},
			want:   [][]string{append(argv("set-option -p -u -t %3 @lt_state ; set-option -p -u -t %3 @lt_subagents ;"), agentsSignal...)},
		},
		{
			name: "session end other failure is logged", event: "SessionEnd", stdin: strings.NewReader(`{"reason":"other"}`),
			result:  tmux.Result{ExitCode: 1, Stderr: []byte("invalid option: @lt_state")},
			want:    [][]string{append(argv("set-option -p -u -t %3 @lt_state ; set-option -p -u -t %3 @lt_subagents ;"), agentsSignal...)},
			wantLog: []string{"hook SessionEnd: tmux set-option: invalid option: @lt_state"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.tmux.result = tc.result
			h.tmux.err = tc.execErr
			h.tty.openErr = tc.openErr
			status := Run(context.Background(), Input{Event: tc.event, Plugin: tc.plugin, Stdin: tc.stdin, Getenv: env(tc.env)}, h.deps)
			if status != 0 {
				t.Fatalf("status %d", status)
			}
			if !reflect.DeepEqual(h.tmux.calls, tc.want) {
				t.Fatalf("tmux calls\n%q\nwant\n%q", h.tmux.calls, tc.want)
			}
			if !reflect.DeepEqual(h.tty.opened, tc.wantBell) {
				t.Fatalf("bell terminals %q; want %q", h.tty.opened, tc.wantBell)
			}
			if want := strings.Repeat("\a", len(tc.wantBell)); h.tty.written.String() != want {
				t.Fatalf("bell bytes %q; want %q", h.tty.written.String(), want)
			}
			logged := h.log(t)
			if tc.wantLog == nil && logged != "" {
				t.Fatalf("unexpected log:\n%s", logged)
			}
			for _, want := range tc.wantLog {
				if !strings.Contains(logged, want) {
					t.Fatalf("log misses %q:\n%s", want, logged)
				}
			}
		})
	}
}
