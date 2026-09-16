package hook

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// liveServer wraps an isolated tmux server with helpers to run the handler
// against it exactly as a Claude process inside one of its panes would.
type liveServer struct {
	*tmuxtest.Server
	socket  string
	logPath string
}

func startLive(t *testing.T) *liveServer {
	t.Helper()
	srv := tmuxtest.Start(t)
	socket, err := srv.Client.Display(tmuxtest.Context(t), "", "#{socket_path}")
	if err != nil {
		t.Fatal(err)
	}
	return &liveServer{Server: srv, socket: socket, logPath: filepath.Join(t.TempDir(), "state", "lyna-tmux.log")}
}

// newSession creates a detached session and returns its first pane, window and session IDs.
func (s *liveServer) newSession(t *testing.T, name string) (pane, window, sessionID string) {
	t.Helper()
	out, err := s.Client.Run(tmuxtest.Context(t), "new-session", "-d", "-s", name, "-x", "80", "-y", "24",
		"-P", "-F", "#{pane_id}"+fieldSep+"#{window_id}"+fieldSep+"#{session_id}", "sleep 3600")
	if err != nil {
		t.Fatalf("new-session %q: %v", name, err)
	}
	f := strings.Split(strings.TrimSpace(out), fieldSep)
	if len(f) != 3 {
		t.Fatalf("new-session output %q", out)
	}
	return f[0], f[1], f[2]
}

func (s *liveServer) deps() Deps {
	return Deps{
		Tmux:    func(sock tmux.Socket) *tmux.Client { return tmux.New(tmux.Options{Bin: s.Bin, Socket: sock}) },
		LogPath: s.logPath,
	}
}

func (s *liveServer) env(pane string, extra map[string]string) func(string) string {
	vars := map[string]string{"TMUX": s.socket + ",1,0", "TMUX_PANE": pane}
	for k, v := range extra {
		if v == "<unset>" {
			delete(vars, k)
			continue
		}
		vars[k] = v
	}
	return func(k string) string { return vars[k] }
}

func (s *liveServer) show(t *testing.T, scope, target, option string) string {
	t.Helper()
	v, err := s.Client.ShowOption(tmuxtest.Context(t), scope, target, option)
	if err != nil {
		t.Fatalf("show %s %s: %v", target, option, err)
	}
	return v
}

func (s *liveServer) bellFlag(t *testing.T, window string) string {
	t.Helper()
	v, err := s.Client.Display(tmuxtest.Context(t), window, "#{window_bell_flag}")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (s *liveServer) readLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(s.logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

type hookStep struct {
	event   string
	payload string
}

func TestIntegrationStateMachine(t *testing.T) {
	srv := startLive(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/feat/#1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := `"cwd":"` + repo + `"`

	cases := []struct {
		name          string
		plugin        bool
		env           map[string]string
		steps         []hookStep
		wantState     string
		wantSubagents string
		wantBranch    string
		wantBell      bool
		wantLog       string
	}{
		{
			name:       "session start is idle with branch",
			steps:      []hookStep{{"SessionStart", `{` + cwd + `,"source":"startup"}`}},
			wantState:  "idle",
			wantBranch: "feat/##1",
		},
		{
			name:      "prompt makes busy",
			steps:     []hookStep{{"SessionStart", `{"cwd":"/","source":"startup"}`}, {"UserPromptSubmit", `{"prompt":"go"}`}},
			wantState: "busy",
		},
		{
			name:      "compaction keeps busy",
			steps:     []hookStep{{"UserPromptSubmit", `{}`}, {"SessionStart", `{"cwd":"/","source":"compact"}`}},
			wantState: "busy",
		},
		{
			name:      "question waits and rings",
			steps:     []hookStep{{"UserPromptSubmit", `{}`}, {"PreToolUse", `{"tool_name":"AskUserQuestion"}`}},
			wantState: "waiting",
			wantBell:  true,
		},
		{
			name:      "permission request waits and rings",
			steps:     []hookStep{{"UserPromptSubmit", `{}`}, {"PermissionRequest", `{"tool_name":"Bash"}`}},
			wantState: "waiting",
			wantBell:  true,
		},
		{
			name:      "permission notification waits and rings",
			steps:     []hookStep{{"Notification", `{"notification_type":"permission_prompt"}`}},
			wantState: "waiting",
			wantBell:  true,
		},
		{
			name:      "tool result returns to busy",
			steps:     []hookStep{{"PermissionRequest", `{"tool_name":"Bash"}`}, {"PostToolUse", `{"tool_name":"Bash"}`}},
			wantState: "busy",
			wantBell:  true,
		},
		{
			name:       "stop is idle, rings and refreshes branch",
			steps:      []hookStep{{"UserPromptSubmit", `{}`}, {"Stop", `{` + cwd + `}`}},
			wantState:  "idle",
			wantBranch: "feat/##1",
			wantBell:   true,
		},
		{
			name:  "bell disabled",
			env:   map[string]string{"LYNA_TMUX_BELL": "0"},
			steps: []hookStep{{"Stop", `{"cwd":"/"}`}},
			// The root directory is outside any repository.
			wantState: "idle",
		},
		{
			name:          "subagents never drop below zero",
			steps:         []hookStep{{"SubagentStart", `{}`}, {"SubagentStart", `{}`}, {"SubagentStop", `{}`}, {"SubagentStop", `{}`}, {"SubagentStop", `{}`}},
			wantSubagents: "0",
		},
		{
			name:  "session end unsets state and subagents",
			steps: []hookStep{{"UserPromptSubmit", `{}`}, {"SubagentStart", `{}`}, {"SessionEnd", `{"reason":"other"}`}},
		},
		{
			name:   "plugin hook inside managed pane is a no-op",
			plugin: true,
			env:    map[string]string{"LYNA_TMUX_MANAGED": "1"},
			steps:  []hookStep{{"UserPromptSubmit", `{}`}, {"Stop", `{}`}},
		},
		{
			name:      "plugin hook outside managed pane runs",
			plugin:    true,
			steps:     []hookStep{{"UserPromptSubmit", `{}`}},
			wantState: "busy",
		},
		{
			name:  "outside tmux is a no-op",
			env:   map[string]string{"TMUX": "<unset>"},
			steps: []hookStep{{"UserPromptSubmit", `{}`}},
		},
		{
			name:      "malformed stdin still updates and logs",
			steps:     []hookStep{{"UserPromptSubmit", `{"prompt":`}},
			wantState: "busy",
			wantLog:   "hook UserPromptSubmit: hook: decode payload",
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane, window, _ := srv.newSession(t, "sm-"+strconv.Itoa(i))
			deps := srv.deps()
			deps.LogPath = filepath.Join(t.TempDir(), "lyna-tmux.log")
			for _, st := range tc.steps {
				status := Run(tmuxtest.Context(t), Input{
					Event: st.event, Plugin: tc.plugin, Stdin: strings.NewReader(st.payload),
					Getenv: srv.env(pane, tc.env),
				}, deps)
				if status != 0 {
					t.Fatalf("%s: status %d", st.event, status)
				}
			}
			if got := srv.show(t, "-p", pane, tmux.OptState); got != tc.wantState {
				t.Fatalf("@lt_state = %q; want %q", got, tc.wantState)
			}
			if got := srv.show(t, "-p", pane, tmux.OptSubagents); got != tc.wantSubagents {
				t.Fatalf("@lt_subagents = %q; want %q", got, tc.wantSubagents)
			}
			if got := srv.show(t, "", "=sm-"+strconv.Itoa(i)+":", tmux.OptBranch); got != tc.wantBranch {
				t.Fatalf("@lt_branch = %q; want %q", got, tc.wantBranch)
			}
			if tc.wantBell {
				tmuxtest.WaitFor(t, "window bell flag", func() bool { return srv.bellFlag(t, window) == "1" })
			} else if got := srv.bellFlag(t, window); got != "0" {
				t.Fatalf("window_bell_flag = %q; want 0", got)
			}
			logData, _ := os.ReadFile(deps.LogPath)
			if tc.wantLog == "" && len(logData) != 0 || !strings.Contains(string(logData), tc.wantLog) {
				t.Fatalf("log %q; want %q", logData, tc.wantLog)
			}
		})
	}
}

// Claude Code runs SessionEnd while it exits, which is often after its pane was
// closed; the real tmux wording for that must be recognized as teardown.
func TestIntegrationSessionEndAfterPaneClosed(t *testing.T) {
	srv := startLive(t)
	srv.newSession(t, "keep")
	pane, _, _ := srv.newSession(t, "closing")
	if _, err := srv.Client.Run(tmuxtest.Context(t), "kill-pane", "-t", pane); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		event   string
		payload string
		wantLog string
	}{
		{event: "SessionEnd", payload: `{"reason":"logout"}`},
		{event: "UserPromptSubmit", payload: `{}`, wantLog: "hook UserPromptSubmit: tmux set-option: no such pane: " + pane},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			deps := srv.deps()
			deps.LogPath = filepath.Join(t.TempDir(), "log")
			Run(tmuxtest.Context(t), Input{Event: tc.event, Stdin: strings.NewReader(tc.payload), Getenv: srv.env(pane, nil)}, deps)
			data, _ := os.ReadFile(deps.LogPath)
			if tc.wantLog == "" && len(data) != 0 || !strings.Contains(string(data), tc.wantLog) {
				t.Fatalf("log %q; want %q", data, tc.wantLog)
			}
		})
	}
}

// Subagent hooks arrive concurrently when Claude runs subagents in parallel;
// the counter must not lose updates.
func TestIntegrationSubagentCounterConcurrent(t *testing.T) {
	srv := startLive(t)
	pane, _, _ := srv.newSession(t, "counter")
	run := func(event string, n int) {
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() {
				Run(tmuxtest.Context(t), Input{Event: event, Stdin: strings.NewReader(`{"agent_id":"a"}`), Getenv: srv.env(pane, nil)}, srv.deps())
			})
		}
		wg.Wait()
	}
	run("SubagentStart", 8)
	if got := srv.show(t, "-p", pane, tmux.OptSubagents); got != "8" {
		t.Fatalf("after 8 starts @lt_subagents = %q", got)
	}
	run("SubagentStop", 11)
	if got := srv.show(t, "-p", pane, tmux.OptSubagents); got != "0" {
		t.Fatalf("after 11 stops @lt_subagents = %q", got)
	}
	if logged := srv.readLog(t); logged != "" {
		t.Fatalf("unexpected log %q", logged)
	}
}

func TestIntegrationChangesSignal(t *testing.T) {
	srv := startLive(t)
	cases := []struct {
		name    string
		session string
		payload string
	}{
		{name: "plain", session: "proj", payload: `{"tool_name":"Write"}`},
		{name: "edit from subagent", session: "sub-edit", payload: `{"tool_name":"Edit","agent_id":"a1"}`},
		{name: "space", session: "my project", payload: `{"tool_name":"Edit"}`},
		{name: "command separator", session: "a;kill-server", payload: `{"tool_name":"Write"}`},
		{name: "trailing separator", session: "semi;", payload: `{"tool_name":"Write"}`},
		{name: "percent", session: "50% done", payload: `{"tool_name":"NotebookEdit"}`},
		{name: "brace", session: "br}ace", payload: `{"tool_name":"MultiEdit"}`},
		{name: "double quote and expansions", session: `q"uote $HOME ~ {x}`, payload: `{"tool_name":"Write"}`},
		{name: "unreadable payload", session: "unknown-payload", payload: `{"tool_name":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane, _, _ := srv.newSession(t, tc.session)
			stored, err := srv.Client.Display(tmuxtest.Context(t), pane, "#{session_name}")
			if err != nil {
				t.Fatal(err)
			}
			ctx := tmuxtest.Context(t)
			done := make(chan error, 1)
			go func() {
				// wait-for latches, so the order against the hook does not matter.
				_, err := srv.Client.Run(ctx, "wait-for", tmux.ChangesChannel(stored))
				done <- err
			}()
			Run(ctx, Input{Event: "PostToolUse", Stdin: strings.NewReader(tc.payload), Getenv: srv.env(pane, nil)}, srv.deps())
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("wait-for %q: %v", tmux.ChangesChannel(stored), err)
				}
			case <-ctx.Done():
				t.Fatalf("no signal on %q", tmux.ChangesChannel(stored))
			}
		})
	}

	t.Run("single quote is skipped without error", func(t *testing.T) {
		pane, _, _ := srv.newSession(t, "it's")
		deps := srv.deps()
		deps.LogPath = filepath.Join(t.TempDir(), "log")
		Run(tmuxtest.Context(t), Input{Event: "PostToolUse", Stdin: strings.NewReader(`{"tool_name":"Write"}`), Getenv: srv.env(pane, nil)}, deps)
		if got := srv.show(t, "-p", pane, tmux.OptState); got != "busy" {
			t.Fatalf("@lt_state = %q", got)
		}
		if data, _ := os.ReadFile(deps.LogPath); len(data) != 0 {
			t.Fatalf("logged %q", data)
		}
	})

	sessions, err := srv.Client.ListSessions(tmuxtest.Context(t))
	if err != nil || len(sessions) < len(cases)+2 {
		t.Fatalf("server lost sessions: %d, %v", len(sessions), err)
	}
}

type recordingTTY struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingTTY) open(path string) (io.WriteCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, path)
	return OpenTTY(path)
}

func TestIntegrationBellForward(t *testing.T) {
	srv := startLive(t)
	ctx := tmuxtest.Context(t)
	workPane, workWindow, _ := srv.newSession(t, "work")
	otherPane, otherWindow, _ := srv.newSession(t, "other")
	_, nestedWindow, _ := srv.newSession(t, "claude-99999999")
	workTTY, err := srv.Client.Display(ctx, workPane, "#{pane_tty}")
	if err != nil {
		t.Fatal(err)
	}
	otherTTY, err := srv.Client.Display(ctx, otherPane, "#{pane_tty}")
	if err != nil {
		t.Fatal(err)
	}

	setOrigin := func(t *testing.T, sessionID, option, value string) {
		t.Helper()
		if _, err := srv.Client.Run(tmuxtest.Context(t), "set-option", "-t", sessionID, option, value); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		session string
		prefix  string
		setup   func(t *testing.T, sessionID string)
		wantTTY []string
		bellOn  string // window expected to show the bell flag
	}{
		{
			name: "own origin option", session: "claude-1a2b3c4d",
			setup:   func(t *testing.T, id string) { setOrigin(t, id, tmux.OptOrigin, workWindow) },
			wantTTY: []string{workTTY}, bellOn: workWindow,
		},
		{
			name: "upstream origin option", session: "claude-2b3c4d5e",
			setup:   func(t *testing.T, id string) { setOrigin(t, id, tmux.OptClaudeOrigin, otherWindow) },
			wantTTY: []string{otherTTY}, bellOn: otherWindow,
		},
		{
			name: "custom prefix", session: "ai-3c4d5e6f", prefix: "ai-",
			setup:   func(t *testing.T, id string) { setOrigin(t, id, tmux.OptOrigin, workWindow) },
			wantTTY: []string{workTTY},
		},
		{
			name: "origin inside a popup session is not forwarded", session: "claude-4d5e6f70",
			setup: func(t *testing.T, id string) { setOrigin(t, id, tmux.OptOrigin, nestedWindow) },
		},
		{
			name: "session without popup prefix", session: "plain-session",
			setup: func(t *testing.T, id string) { setOrigin(t, id, tmux.OptOrigin, workWindow) },
		},
		{
			name: "no origin", session: "claude-5e6f7081",
			setup: func(*testing.T, string) {},
		},
		{
			name: "origin window closed", session: "claude-6f708192",
			setup: func(t *testing.T, id string) { setOrigin(t, id, tmux.OptOrigin, "@99999") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, sessionID := srv.newSession(t, tc.session)
			tc.setup(t, sessionID)
			if tc.bellOn != "" {
				// Start from a clean flag: selecting the window of an
				// unattached session does not clear it, so use a fresh one.
				if got := srv.bellFlag(t, tc.bellOn); got != "0" {
					t.Fatalf("bell flag already set on %s", tc.bellOn)
				}
			}
			tty := &recordingTTY{}
			deps := srv.deps()
			deps.LogPath = filepath.Join(t.TempDir(), "log")
			deps.OpenTTY = tty.open
			status := BellForward(tmuxtest.Context(t), BellForwardInput{Session: sessionID, Prefix: tc.prefix, Getenv: srv.env("", nil)}, deps)
			if status != 0 {
				t.Fatalf("status %d", status)
			}
			if strings.Join(tty.paths, ",") != strings.Join(tc.wantTTY, ",") {
				t.Fatalf("bell written to %q; want %q", tty.paths, tc.wantTTY)
			}
			if tc.bellOn != "" {
				tmuxtest.WaitFor(t, "bell flag on origin window", func() bool { return srv.bellFlag(t, tc.bellOn) == "1" })
			}
			if data, _ := os.ReadFile(deps.LogPath); len(data) != 0 {
				t.Fatalf("logged %q", data)
			}
		})
	}
}
