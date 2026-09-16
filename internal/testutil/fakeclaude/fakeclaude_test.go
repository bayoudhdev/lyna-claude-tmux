package fakeclaude

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type run struct {
	code   int
	stdout string
	stderr string
	record string
}

// start runs Main in-process with a private record file.
func start(t *testing.T, args []string, env []string, stdin io.Reader, signal <-chan struct{}) run {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record.jsonl")
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	var stdout, stderr bytes.Buffer
	code := Main(Process{
		Args:    append([]string{"/fake/bin/claude"}, args...),
		Environ: append([]string{EnvRecord + "=" + record, "HOME=" + dir}, env...),
		Dir:     dir,
		Stdin:   stdin,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Signal:  signal,
	})
	return run{code: code, stdout: stdout.String(), stderr: stderr.String(), record: record}
}

func TestMainReadOnlyCommands(t *testing.T) {
	agentsFile := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`[{"pid":42,"cwd":"/w","kind":"interactive","startedAt":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		args       []string
		env        []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version default", args: []string{"--version"}, wantStdout: DefaultVersion + "\n"},
		{name: "version short flag", args: []string{"-v"}, wantStdout: DefaultVersion + "\n"},
		{name: "version override", args: []string{"--version"}, env: []string{EnvVersion + "=2.0.1 (Claude Code)"}, wantStdout: "2.0.1 (Claude Code)\n"},
		{name: "agents without file", args: []string{"agents", "--json"}, wantStdout: "[]\n"},
		{name: "agents from file", args: []string{"agents", "--json"}, env: []string{EnvAgents + "=" + agentsFile}, wantStdout: `[{"pid":42,"cwd":"/w","kind":"interactive","startedAt":1}]`},
		{name: "agents missing file", args: []string{"agents", "--json"}, env: []string{EnvAgents + "=/nonexistent/agents.json"}, wantCode: 1, wantStderr: "fakeclaude: agents:"},
		{name: "agents without json", args: []string{"agents"}, wantCode: 2, wantStderr: "only `agents --json`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := start(t, tc.args, tc.env, nil, nil)
			if r.code != tc.wantCode || r.stdout != tc.wantStdout || !strings.Contains(r.stderr, tc.wantStderr) {
				t.Fatalf("Main = %d, stdout %q, stderr %q; want %d, %q, stderr containing %q", r.code, r.stdout, r.stderr, tc.wantCode, tc.wantStdout, tc.wantStderr)
			}
			invs := ReadRecords(t, r.record)
			if len(invs) != 1 || !slices.Equal(invs[0].Args, tc.args) || invs[0].Program != "/fake/bin/claude" || invs[0].PID != os.Getpid() {
				t.Fatalf("records = %+v", invs)
			}
		})
	}
}

func TestMainRecordsEnvAndSettings(t *testing.T) {
	settingsDir := t.TempDir()
	settingsFile := filepath.Join(settingsDir, "0123456789abcdef.json")
	if err := os.WriteFile(settingsFile, []byte(`{"sandbox":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "broken.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"LYNA_TMUX_MANAGED=1", "LYNA_TMUX_SESSION=api", "CLAUDE_CODE_NO_FLICKER=1", "CLAUDE_CONFIG_DIR=/cfg",
		"TMUX=/tmp/s,1,0", "TMUX_PANE=%3", "SECRET_TOKEN=nope", "PATH=/usr/bin", EnvExit + "=1",
	}
	cases := []struct {
		name         string
		args         []string
		wantPath     string
		wantSettings string
		wantError    string
	}{
		{name: "no settings", args: []string{"--name=api"}},
		{name: "equals form file", args: []string{"--settings=" + settingsFile}, wantPath: settingsFile, wantSettings: `{"sandbox":{"enabled":true}}`},
		{name: "separate form file", args: []string{"--settings", settingsFile, "prompt"}, wantPath: settingsFile, wantSettings: `{"sandbox":{"enabled":true}}`},
		{name: "inline json", args: []string{"--settings", `{"model":"opus"}`}, wantSettings: `{"model":"opus"}`},
		{name: "missing file", args: []string{"--settings=/nonexistent/s.json"}, wantPath: "/nonexistent/s.json", wantError: "no such file"},
		{name: "invalid json file", args: []string{"--settings=" + filepath.Join(settingsDir, "broken.json")}, wantPath: filepath.Join(settingsDir, "broken.json"), wantError: "not valid JSON"},
		{name: "after double dash is not a flag", args: []string{"--", "--settings=" + settingsFile}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := start(t, tc.args, env, nil, nil)
			if r.code != 0 {
				t.Fatalf("Main = %d, stderr %q", r.code, r.stderr)
			}
			invs := ReadRecords(t, r.record)
			if len(invs) != 1 {
				t.Fatalf("got %d records", len(invs))
			}
			inv := invs[0]
			wantEnv := map[string]string{
				"LYNA_TMUX_MANAGED": "1", "LYNA_TMUX_SESSION": "api", "CLAUDE_CODE_NO_FLICKER": "1",
				"CLAUDE_CONFIG_DIR": "/cfg", "TMUX": "/tmp/s,1,0", "TMUX_PANE": "%3",
			}
			if len(inv.Env) != len(wantEnv) {
				t.Fatalf("env = %v, want %v", inv.Env, wantEnv)
			}
			for k, v := range wantEnv {
				if inv.Env[k] != v {
					t.Fatalf("env = %v, want %v", inv.Env, wantEnv)
				}
			}
			if inv.SettingsPath != tc.wantPath || string(inv.Settings) != tc.wantSettings || !strings.Contains(inv.SettingsError, tc.wantError) || (tc.wantError == "") != (inv.SettingsError == "") {
				t.Fatalf("settings = path %q, doc %s, error %q", inv.SettingsPath, inv.Settings, inv.SettingsError)
			}
			if inv.Cwd == "" {
				t.Fatal("cwd not recorded")
			}
		})
	}
}

// hookSettings writes a settings file whose hooks append a marker line with
// the event and payload to marker.
func hookSettings(t *testing.T, dir, marker string, extra map[string]any) string {
	t.Helper()
	cmd := func(tag string) map[string]any {
		return map[string]any{"type": "command", "command": "{ printf '" + tag + " '; cat; echo; } >> '" + marker + "'"}
	}
	hooks := map[string]any{
		"SessionStart":     []any{map[string]any{"hooks": []any{cmd("SessionStart")}}},
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{cmd("UserPromptSubmit")}}},
		"PreToolUse": []any{
			map[string]any{"matcher": "AskUserQuestion", "hooks": []any{cmd("PreToolUse-ask")}},
			map[string]any{"matcher": "Bash", "hooks": []any{cmd("PreToolUse-bash")}},
		},
		"Notification": []any{map[string]any{"matcher": "permission_prompt", "hooks": []any{cmd("Notification"), map[string]any{"type": "http", "url": "http://127.0.0.1:9"}}}},
		"Stop":         []any{map[string]any{"hooks": []any{cmd("Stop"), map[string]any{"type": "command", "command": "echo out; echo err >&2; exit 3"}}}},
	}
	for k, v := range extra {
		hooks[k] = v
	}
	data, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMainRunsHooksInOrder(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	settings := hookSettings(t, dir, marker, map[string]any{
		"SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "exec tail -f /dev/null", "timeout": 0.05}}}},
	})
	r := start(t, []string{"--settings=" + settings, "--permission-mode=plan"},
		[]string{EnvHooks + "=SessionStart, UserPromptSubmit,PreToolUse,Notification,,Stop,SessionEnd", EnvExit + "=1", EnvSessionID + "=s-1"}, nil, nil)
	if r.code != 0 {
		t.Fatalf("Main = %d, stderr %q", r.code, r.stderr)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		tag, payload, _ := strings.Cut(line, " ")
		tags = append(tags, tag)
		var in map[string]any
		if err := json.Unmarshal([]byte(payload), &in); err != nil {
			t.Fatalf("hook %s stdin is not JSON: %v", tag, err)
		}
		if in["session_id"] != "s-1" || in["permission_mode"] != "plan" || in["cwd"] == "" || in["transcript_path"] == "" {
			t.Fatalf("hook %s payload %v", tag, in)
		}
	}
	if want := []string{"SessionStart", "UserPromptSubmit", "PreToolUse-ask", "Notification", "Stop"}; !slices.Equal(tags, want) {
		t.Fatalf("hooks ran %v, want %v", tags, want)
	}
	runs := ReadHookRuns(t, r.record)
	if len(runs) != 7 {
		t.Fatalf("got %d hook runs: %+v", len(runs), runs)
	}
	stop := runs[5]
	if stop.Event != "Stop" || stop.ExitCode != 3 || stop.Stdout != "out\n" || stop.Stderr != "err\n" {
		t.Fatalf("failing Stop hook recorded as %+v", stop)
	}
	end := runs[6]
	if end.Event != "SessionEnd" || end.ExitCode == 0 {
		t.Fatalf("timed out SessionEnd hook recorded as %+v", end)
	}
	if len(ReadRecords(t, r.record)) != 1 {
		t.Fatal("hook lines were read as invocations")
	}
}

func TestHookInputPerEvent(t *testing.T) {
	cases := []struct {
		event   string
		subject string // "" when the event has no matcher
		fields  []string
	}{
		{event: "SessionStart", subject: "startup", fields: []string{"source", "model"}},
		{event: "UserPromptSubmit", fields: []string{"prompt"}},
		{event: "PreToolUse", subject: "Bash", fields: []string{"tool_name", "tool_input", "tool_use_id"}},
		{event: "PermissionRequest", subject: "Bash", fields: []string{"tool_name", "tool_input"}},
		{event: "PostToolUse", subject: "Bash", fields: []string{"tool_name", "tool_input", "tool_response"}},
		{event: "Notification", subject: "permission_prompt", fields: []string{"message", "notification_type"}},
		{event: "SubagentStart", subject: "Explore", fields: []string{"agent_id", "agent_type"}},
		{event: "SubagentStop", subject: "Explore", fields: []string{"agent_id", "agent_type", "stop_hook_active", "agent_transcript_path"}},
		{event: "Stop", fields: []string{"stop_hook_active", "last_assistant_message"}},
		{event: "SessionEnd", subject: "other", fields: []string{"reason"}},
	}
	p := Process{Environ: []string{EnvTool + "=Bash", "CLAUDE_CONFIG_DIR=/cfg"}, Dir: "/work"}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			data, subject := p.hookInput(tc.event, Invocation{})
			var in map[string]any
			if err := json.Unmarshal(data, &in); err != nil {
				t.Fatal(err)
			}
			if in["hook_event_name"] != tc.event || in["cwd"] != "/work" || in["session_id"] != DefaultSessionID || in["permission_mode"] != "default" {
				t.Fatalf("common fields: %v", in)
			}
			if !strings.HasPrefix(in["transcript_path"].(string), "/cfg/projects/") {
				t.Fatalf("transcript_path = %v", in["transcript_path"])
			}
			for _, f := range tc.fields {
				if _, ok := in[f]; !ok {
					t.Errorf("missing field %s in %v", f, in)
				}
			}
			switch {
			case tc.subject == "" && subject != nil:
				t.Fatalf("subject = %q, want none", *subject)
			case tc.subject != "" && (subject == nil || *subject != tc.subject):
				t.Fatalf("subject = %v, want %q", subject, tc.subject)
			}
		})
	}
}

func TestMainHookErrors(t *testing.T) {
	dir := t.TempDir()
	notHooks := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(notHooks, []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := start(t, []string{"--settings=" + notHooks}, []string{EnvHooks + "=Stop", EnvExit + "=1"}, nil, nil)
	if r.code != 1 || !strings.Contains(r.stderr, "settings hooks") {
		t.Fatalf("Main = %d, stderr %q", r.code, r.stderr)
	}
	// Hooks requested without a settings file run nothing.
	r = start(t, nil, []string{EnvHooks + "=Stop", EnvExit + "=1"}, nil, nil)
	if r.code != 0 || len(ReadHookRuns(t, r.record)) != 0 {
		t.Fatalf("Main = %d, stderr %q", r.code, r.stderr)
	}
	// An unwritable record file fails loudly.
	var stderr bytes.Buffer
	code := Main(Process{Args: []string{"claude", "--version"}, Environ: []string{EnvRecord + "=/nonexistent/dir/record"}, Stdout: io.Discard, Stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "record") {
		t.Fatalf("Main = %d, stderr %q", code, stderr.String())
	}
}

func TestMainInteractiveLifetime(t *testing.T) {
	t.Run("returns at stdin EOF", func(t *testing.T) {
		if r := start(t, nil, nil, strings.NewReader("typed keys"), make(chan struct{})); r.code != 0 {
			t.Fatalf("Main = %d", r.code)
		}
	})
	t.Run("returns on signal while stdin stays open", func(t *testing.T) {
		stdin, keepOpen := io.Pipe()
		t.Cleanup(func() { _ = keepOpen.Close() })
		signal := make(chan struct{})
		close(signal)
		if r := start(t, nil, nil, stdin, signal); r.code != 0 {
			t.Fatalf("Main = %d", r.code)
		}
	})
	t.Run("block waits for a signal before answering", func(t *testing.T) {
		signal := make(chan struct{})
		close(signal)
		r := start(t, []string{"--version"}, []string{EnvBlock + "=1"}, nil, signal)
		if r.code != 143 || r.stdout != "" || len(ReadRecords(t, r.record)) != 1 {
			t.Fatalf("Main = %d, stdout %q", r.code, r.stdout)
		}
	})
}

func TestMainRecordsConcurrently(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.jsonl")
	big := strings.Repeat("x", 32<<10)
	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Main(Process{
				Args:    []string{"claude", "--version", big},
				Environ: []string{EnvRecord + "=" + record, "LYNA_TMUX_SESSION=s" + string(rune('a'+i))},
				Stdout:  io.Discard, Stderr: io.Discard,
			})
		}()
	}
	wg.Wait()
	if got := ReadRecords(t, record); len(got) != n {
		t.Fatalf("got %d records, want %d", len(got), n)
	}
}

func TestOSProcess(t *testing.T) {
	p, stop := OSProcess()
	defer stop()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Args, os.Args) || p.Dir != wd || p.Stdin != os.Stdin || p.Stdout != os.Stdout || p.Stderr != os.Stderr || len(p.Environ) == 0 {
		t.Fatalf("OSProcess = %+v", p)
	}
	select {
	case <-p.Signal:
		t.Fatal("signal channel closed without a signal")
	default:
	}
	stop() // idempotent with the deferred call
}

func TestReadRecordsMissingFile(t *testing.T) {
	if got := ReadRecords(t, filepath.Join(t.TempDir(), "none")); got != nil {
		t.Fatalf("ReadRecords = %v, want nil", got)
	}
	if got := ReadHookRuns(t, filepath.Join(t.TempDir(), "none")); got != nil {
		t.Fatalf("ReadHookRuns = %v, want nil", got)
	}
}

func TestMatcherMatches(t *testing.T) {
	cases := []struct {
		matcher string
		value   string
		want    bool
	}{
		{matcher: "", value: "Bash", want: true},
		{matcher: "*", value: "anything", want: true},
		{matcher: "Bash", value: "Bash", want: true},
		{matcher: "Bash", value: "BashOutput"},
		{matcher: "Edit|Write", value: "Write", want: true},
		{matcher: "Edit, Write", value: "Write", want: true},
		{matcher: "code-reviewer", value: "senior-code-reviewer"},
		{matcher: "Edit.*", value: "NotebookEdit", want: true},
		{matcher: "^Edit$", value: "NotebookEdit"},
		{matcher: "mcp__memory__.*", value: "mcp__memory__read", want: true},
		{matcher: "(", value: "("},
	}
	for _, tc := range cases {
		t.Run(tc.matcher+"~"+tc.value, func(t *testing.T) {
			if got := MatcherMatches(tc.matcher, tc.value); got != tc.want {
				t.Fatalf("MatcherMatches(%q, %q) = %v, want %v", tc.matcher, tc.value, got, tc.want)
			}
		})
	}
}

func FuzzMatcherMatches(f *testing.F) {
	for _, s := range []string{"", "*", "Bash", "Edit|Write", "a, b", "^x$", "(", "mcp__.*"} {
		f.Add(s, "Bash")
	}
	f.Fuzz(func(t *testing.T, matcher, value string) {
		got := MatcherMatches(matcher, value)
		if matcher == "" || matcher == "*" {
			if !got {
				t.Fatal("match-all matcher did not match")
			}
			return
		}
		if exactMatcher.MatchString(matcher) {
			names := strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' })
			want := slices.ContainsFunc(names, func(n string) bool { return strings.TrimSpace(n) == value })
			if got != want {
				t.Fatalf("exact matcher %q on %q = %v, want %v", matcher, value, got, want)
			}
		}
	})
}
