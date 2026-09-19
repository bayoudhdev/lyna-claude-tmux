package fakeclaude

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
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

func TestParseTeammates(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    []teammate
		wantErr string
	}{
		{name: "a name and a type", spec: "review-api:api-developer", want: []teammate{{"review-api", "api-developer"}}},
		{name: "a name with no type", spec: "tests", want: []teammate{{"tests", ""}}},
		{name: "an empty type", spec: "tests:", want: []teammate{{"tests", ""}}},
		{name: "several, spaced, with empty entries", spec: " a : Explore ,, b ", want: []teammate{{"a", "Explore"}, {"b", ""}}},
		{name: "nothing named", spec: " , ", wantErr: "no teammate named"},
		{name: "a name that starts with a dash", spec: "-x", wantErr: "not one Claude Code accepts"},
		{name: "a name with a slash", spec: "a/b:t", wantErr: "not one Claude Code accepts"},
		{name: "a name too long", spec: strings.Repeat("a", 65), wantErr: "not one Claude Code accepts"},
		{name: "the name of the lead", spec: "Team-Lead", wantErr: "reserved"},
		{name: "the name of the main conversation", spec: "main", wantErr: "reserved"},
		{name: "a name given twice", spec: "a,A:Explore", wantErr: "given twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTeammates(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseTeammates(%q) = %v, %v; want an error with %q", tc.spec, got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("parseTeammates(%q) = %v, %v; want %v", tc.spec, got, err, tc.want)
			}
		})
	}
}

func FuzzParseTeammates(f *testing.F) {
	for _, s := range []string{"a:Explore,b", "a:", " , ", "team-lead", "a,A", "x:y:z"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		got, err := parseTeammates(spec)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, m := range got {
			key := strings.ToLower(m.name)
			if !teammateName.MatchString(m.name) || seen[key] || !strings.Contains(spec, m.name) {
				t.Fatalf("parseTeammates(%q) opened %q, which Claude Code would not", spec, m.name)
			}
			seen[key] = true
		}
	})
}

func TestTeamName(t *testing.T) {
	cases := []struct{ id, want string }{
		{id: DefaultSessionID, want: "session-00000000"},
		{id: "8f3c1d2a-77b1-4d0e-9a55-5c6d2f1e0b34", want: "session-8f3c1d2a"},
		{id: "12345678", want: "session-12345678"},
		{id: "abc", want: "session-abc"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			// The directory the fake writes is the one lyna-tmux reads.
			if got := teamName(tc.id); got != tc.want || got != team.Name(tc.id) {
				t.Fatalf("teamName(%q) = %q, want %q (team.Name %q)", tc.id, got, tc.want, team.Name(tc.id))
			}
		})
	}
}

// shellEcho prints what sh reads word as.
func shellEcho(t *testing.T, word string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf %s "+word).Output()
	if err != nil {
		t.Fatalf("sh -c printf %s: %v", word, err)
	}
	return string(out)
}

func TestShellWord(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "plain", want: "plain"},
		{in: "review-api@session-8f3c1d2a", want: "review-api@session-8f3c1d2a"},
		{in: "/tmp/a/b.json", want: "/tmp/a/b.json"},
		{in: "%3", want: "%3"},
		{in: "", want: "''"},
		{in: "a b", want: "'a b'"},
		{in: "it's", want: `'it'\''s'`},
		{in: "$HOME", want: "'$HOME'"},
		{in: "a;b&&c|d", want: "'a;b&&c|d'"},
		{in: "line\nbreak", want: "'line\nbreak'"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := shellWord(tc.in); got != tc.want {
				t.Fatalf("shellWord(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got := shellEcho(t, shellWord(tc.in)); got != tc.in {
				t.Fatalf("sh reads %q as %q", shellWord(tc.in), got)
			}
		})
	}
}

func FuzzShellWord(f *testing.F) {
	for _, s := range []string{"plain", "", "a b", "it's", "$(id)", "`id`", "a\nb", "--flag=v"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// No argument can carry a NUL, and a word that starts with a dash
		// would be read by printf as an option rather than printed.
		if strings.ContainsRune(s, 0) || strings.HasPrefix(s, "-") {
			return
		}
		if got := shellEcho(t, shellWord(s)); got != s {
			t.Fatalf("sh reads %q as %q", shellWord(s), got)
		}
	})
}

func TestFlagValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{name: "both spellings in order", args: []string{"--plugin-dir=/a", "--model", "opus", "--plugin-dir", "/b"}, want: []string{"/a", "/b"}},
		{name: "none", args: []string{"--model=opus"}},
		{name: "a flag at the end has no value", args: []string{"--plugin-dir"}},
		{name: "nothing after the double dash", args: []string{"--plugin-dir=/a", "--", "--plugin-dir=/b"}, want: []string{"/a"}},
		{name: "a value is not read as a flag", args: []string{"--plugin-dir", "--plugin-dir", "/c"}, want: []string{"--plugin-dir"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagValues(tc.args, "--plugin-dir"); !slices.Equal(got, tc.want) {
				t.Fatalf("flagValues(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestLookPath(t *testing.T) {
	bin := t.TempDir()
	noExec := t.TempDir()
	for dir, mode := range map[string]os.FileMode{bin: 0o700, noExec: 0o600} {
		if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(bin, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, program, path, want string
	}{
		{name: "the first executable on PATH", program: "tmux", path: noExec + ":" + bin, want: filepath.Join(bin, "tmux")},
		{name: "a relative directory is passed over", program: "tmux", path: "." + string(filepath.ListSeparator) + "rel"},
		{name: "a file that cannot run", program: "tmux", path: noExec},
		{name: "a directory of that name", program: "dir", path: bin},
		{name: "an empty PATH", program: "tmux", path: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lookPath(tc.program, tc.path)
			if got != tc.want || (tc.want == "") != (err != nil) {
				t.Fatalf("lookPath(%q, %q) = %q, %v; want %q", tc.program, tc.path, got, err, tc.want)
			}
		})
	}
}

// readTeam reads the team file a lead wrote under configDir, through the
// parser lyna-tmux reads it with.
func readTeam(t *testing.T, configDir string) team.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "teams", team.Name(DefaultSessionID), "config.json"))
	if err != nil {
		t.Fatalf("team file: %v", err)
	}
	cfg, err := team.ParseConfig(data)
	if err != nil {
		t.Fatalf("team file: %v\n%s", err, data)
	}
	return cfg
}

// checkLead checks the team file names the lead the way Claude Code does.
func checkLead(t *testing.T, cfg team.Config, dir string) {
	t.Helper()
	lead := cfg.Members[0]
	if cfg.Name != team.Name(DefaultSessionID) || cfg.LeadSessionID != DefaultSessionID || !lead.Lead ||
		lead.Name != "team-lead" || lead.Pane != team.LeadPane || lead.Backend != team.BackendInProcess || lead.Dir != dir {
		t.Fatalf("team %+v, lead %+v", cfg, lead)
	}
}

func TestOpenTeamInProcess(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"teammateMode":"in-process"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inTmux := []string{"TMUX=/tmp/tmux-1/lt-test-none,1,0", "TMUX_PANE=%0", "PATH=/nonexistent"}
	cases := []struct {
		name string
		args []string
		env  []string
	}{
		{name: "the setting keeps them in process", args: []string{"--settings=" + settings}, env: inTmux},
		{name: "the flag keeps them in process", args: []string{"--teammate-mode", "in-process"}, env: inTmux},
		{name: "a lead outside tmux", args: []string{"--teammate-mode=tmux"}},
		{name: "a lead in tmux with no pane of its own", env: []string{"TMUX=/tmp/tmux-1/lt-test-none,1,0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := t.TempDir()
			env := append([]string{EnvTeammates + "=alpha:Explore,beta", EnvExit + "=1", "CLAUDE_CONFIG_DIR=" + config}, tc.env...)
			r := start(t, tc.args, env, nil, nil)
			if r.code != 0 {
				t.Fatalf("Main = %d, stderr %q", r.code, r.stderr)
			}
			if runs := ReadTmuxRuns(t, r.record); len(runs) != 0 {
				t.Fatalf("a team kept in process ran tmux: %+v", runs)
			}
			spawns := ReadTeammates(t, r.record)
			if len(spawns) != 2 || spawns[0].Name != "alpha" || spawns[0].AgentType != "Explore" || spawns[1].Name != "beta" {
				t.Fatalf("spawns %+v", spawns)
			}
			cfg := readTeam(t, config)
			checkLead(t, cfg, ReadRecords(t, r.record)[0].Cwd)
			mates := cfg.Teammates()
			for i, s := range spawns {
				m := mates[i]
				if _, ok := m.TmuxPane(); ok || s.Backend != backendInProcess || s.Pane != "" ||
					m.Name != s.Name || m.AgentType != s.AgentType || m.Backend != team.BackendInProcess || m.Pane != backendInProcess {
					t.Fatalf("teammate %d: spawn %+v, member %+v", i, s, m)
				}
			}
		})
	}
}

func TestOpenTeamRefuses(t *testing.T) {
	emptyPath := t.TempDir()
	cases := []struct {
		name       string
		args       []string
		env        []string
		noConfig   bool
		wantCode   int
		wantStderr string
		// wantFile expects the team file with its lead only.
		wantFile bool
	}{
		{name: "a teammate Claude Code would refuse", env: []string{EnvTeammates + "=-x"}, wantCode: 2, wantStderr: EnvTeammates},
		{name: "no configuration directory", env: []string{EnvTeammates + "=a"}, noConfig: true, wantCode: 2, wantStderr: "CLAUDE_CONFIG_DIR"},
		{name: "a relative configuration directory", env: []string{EnvTeammates + "=a", "CLAUDE_CONFIG_DIR=claude"}, noConfig: true, wantCode: 2, wantStderr: "CLAUDE_CONFIG_DIR"},
		{name: "a teammate opens no team", args: []string{"--agent-id", "b@session-1", "--agent-name", "b"}, env: []string{EnvTeammates + "=a"}},
		{name: "no teammates asked for"},
		{
			name: "tmux missing from PATH", env: []string{EnvTeammates + "=a", "TMUX=/tmp/tmux-1/lt-test-none,1,0", "TMUX_PANE=%0", "PATH=" + emptyPath},
			wantCode: 1, wantStderr: "tmux is not on PATH", wantFile: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := t.TempDir()
			env := append([]string{EnvExit + "=1"}, tc.env...)
			if !tc.noConfig {
				env = append(env, "CLAUDE_CONFIG_DIR="+config)
			}
			r := start(t, tc.args, env, nil, nil)
			if r.code != tc.wantCode || !strings.Contains(r.stderr, tc.wantStderr) {
				t.Fatalf("Main = %d, stderr %q; want %d with %q", r.code, r.stderr, tc.wantCode, tc.wantStderr)
			}
			if spawns, runs := ReadTeammates(t, r.record), ReadTmuxRuns(t, r.record); len(spawns)+len(runs) != 0 {
				t.Fatalf("opened %+v with %+v", spawns, runs)
			}
			if tc.wantFile {
				if cfg := readTeam(t, config); len(cfg.Members) != 1 {
					t.Fatalf("team file %+v, want the lead alone", cfg)
				}
				return
			}
			if _, err := os.Stat(filepath.Join(config, "teams")); !os.IsNotExist(err) {
				t.Fatalf("a team directory was written: %v", err)
			}
		})
	}
}

// teamServer is a real tmux server with a pane for a lead to run in.
type teamServer struct {
	srv *tmuxtest.Server
	// socket is the path $TMUX names, and lead the pane the lead runs in.
	socket, lead string
}

func startTeamServer(t *testing.T) teamServer {
	t.Helper()
	srv := tmuxtest.Start(t)
	s := teamServer{srv: srv, socket: tmuxtest.SocketPath(srv.Name)}
	s.lead = s.run(t, "display-message", "-p", "-t", "=base:", "#{pane_id}")
	return s
}

func (s teamServer) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := s.srv.Client.Run(tmuxtest.Context(t), args[0], args[1:]...)
	if err != nil {
		t.Fatalf("%q: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// teamLead is a lead run in-process in a pane of a real server, with every
// path it is given holding a character the shell would read.
type teamLead struct {
	dir, config, record, settings, marker string
}

func newTeamLead(t *testing.T) teamLead {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := teamLead{
		dir: filepath.Join(root, "lead's project"), config: filepath.Join(root, "claude home"),
		record: filepath.Join(root, "record.jsonl"), settings: filepath.Join(root, "set tings", "settings.json"),
		marker: filepath.Join(root, "marker"),
	}
	for _, d := range []string{l.dir, l.config, filepath.Dir(l.settings)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hooks := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo \"$TMUX_PANE\" >> '` + l.marker + `'"}]}]}}`
	if err := os.WriteFile(l.settings, []byte(hooks), 0o600); err != nil {
		t.Fatal(err)
	}
	return l
}

// main runs the lead to the end of its team and returns its exit status.
func (l teamLead) main(t *testing.T, s teamServer, exe string, args, env []string, stderr io.Writer) int {
	t.Helper()
	return Main(Process{
		Args: append([]string{"/lead/claude"}, args...),
		Environ: append([]string{
			EnvRecord + "=" + l.record, "HOME=" + l.dir, "PATH=" + os.Getenv("PATH"), "CLAUDE_CONFIG_DIR=" + l.config,
			EnvExit + "=1", EnvHooks + "=SessionStart", "TMUX=" + s.socket + ",1,0", "TMUX_PANE=" + s.lead,
		}, env...),
		Dir: l.dir, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: stderr,
		Signal: make(chan struct{}), Executable: exe,
	})
}

// teammatesStarted waits until every spawned teammate recorded its start, and
// returns the starts by pane.
func teammatesStarted(t *testing.T, record string, n int) map[string]Invocation {
	t.Helper()
	byPane := map[string]Invocation{}
	tmuxtest.WaitFor(t, "every teammate started", func() bool {
		for _, inv := range ReadRecords(t, record) {
			if isTeammate(inv.Args) {
				byPane[inv.Env["TMUX_PANE"]] = inv
			}
		}
		return len(byPane) >= n
	})
	return byPane
}

// TestOpenTeamOnTmux runs a lead in a pane of a real server and reads back
// what it did: the commands the tmux backend runs, in its order, the panes
// they made, the team file, and every teammate started in its own pane with
// arguments lyna-tmux parses.
func TestOpenTeamOnTmux(t *testing.T) {
	fake := Build(t)
	leadArgs := func(l teamLead) []string {
		return []string{
			"--settings=" + l.settings, "--name=api", "--model=opus", "--effort=high",
			"--permission-mode=acceptEdits", "--plugin-dir=/plugins/one", "--plugin-dir", "/plugins/two",
		}
	}
	type split struct{ target, dir string }
	cases := []struct {
		name string
		// beside opens a pane left of the lead first, the way a rail sits.
		beside bool
		// launcher starts each teammate through CLAUDE_CODE_TEAMMATE_COMMAND;
		// without one the lead starts the fake itself.
		launcher bool
		spec     string
		// wantSplits are the split targets, by teammate name or "lead", and
		// their directions.
		wantSplits []split
		wantVerbs  []string
		// wantStatus expects the border status the first teammate of a window
		// of one pane turns on.
		wantStatus bool
		// wantColumn is the pane main-vertical keeps a 30% column for.
		wantColumn string
	}{
		{
			name: "a lead alone in its window, through a launcher", launcher: true, spec: "alpha:Explore,beta",
			wantSplits: []split{{"lead", "-h"}, {"alpha", "-v"}},
			wantVerbs: []string{
				"display-message", "list-panes", "split-window", "set-option", "set-option", "set-option", "select-pane", "set-option", "list-panes",
				"set-option", "set-option", "respawn-pane",
				"list-panes", "split-window", "set-option", "set-option", "set-option", "select-pane", "set-option", "list-panes",
				"select-layout", "resize-pane", "set-option", "respawn-pane",
			},
			wantStatus: true, wantColumn: "lead",
		},
		{
			name: "a lead beside another pane, with no launcher", beside: true, spec: "alpha",
			wantSplits: []split{{"lead", "-v"}},
			wantVerbs: []string{
				"display-message", "list-panes", "split-window", "set-option", "set-option", "set-option", "select-pane", "set-option", "list-panes",
				"select-layout", "resize-pane", "set-option", "respawn-pane",
			},
			wantColumn: "beside",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := startTeamServer(t)
			l := newTeamLead(t)
			names := map[string]string{"lead": s.lead}
			if tc.beside {
				names["beside"] = s.run(t, "split-window", "-b", "-h", "-d", "-l", "28", "-t", s.lead, "-P", "-F", "#{pane_id}", "sleep 3600")
			}
			var env []string
			program := fake
			if tc.launcher {
				program = filepath.Join(filepath.Dir(l.settings), "launch er.sh")
				script := "#!/bin/sh\nexport LYNA_TMUX_TEST_LAUNCHER=1\nexec '" + fake + "' \"$@\"\n"
				if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				env = append(env, "CLAUDE_CODE_TEAMMATE_COMMAND="+program)
			}
			var stderr bytes.Buffer
			if code := l.main(t, s, fake, leadArgs(l), append(env, EnvTeammates+"="+tc.spec), &stderr); code != 0 {
				t.Fatalf("Main = %d, stderr %q", code, stderr.String())
			}

			spawns := ReadTeammates(t, l.record)
			for _, sp := range spawns {
				names[sp.Name] = sp.Pane
			}
			runs := ReadTmuxRuns(t, l.record)
			var verbs []string
			var splits []split
			for _, r := range runs {
				if len(r.Args) < 3 || r.Args[0] != "-S" || r.Args[1] != s.socket || r.ExitCode != 0 {
					t.Fatalf("tmux run %+v, want one on %s that succeeded", r, s.socket)
				}
				verbs = append(verbs, r.Args[2])
				if r.Args[2] == "split-window" {
					splits = append(splits, split{r.Args[5], r.Args[6]})
				}
				if r.Args[2] == "resize-pane" && (r.Args[4] != names[tc.wantColumn] || r.Args[6] != "30%") {
					t.Fatalf("resize %q, want %s at 30%%", r.Args, tc.wantColumn)
				}
			}
			if !slices.Equal(verbs, tc.wantVerbs) {
				t.Fatalf("tmux verbs\n got %q\nwant %q", verbs, tc.wantVerbs)
			}
			for i := range splits {
				splits[i].target = map[string]string{s.lead: "lead", names["alpha"]: "alpha"}[splits[i].target]
			}
			if !slices.Equal(splits, tc.wantSplits) {
				t.Fatalf("splits %v, want %v", splits, tc.wantSplits)
			}
			if got := s.run(t, "show-options", "-w", "-v", "-t", s.lead, "pane-border-status"); (got == "top") != tc.wantStatus {
				t.Fatalf("pane-border-status %q, want top: %v", got, tc.wantStatus)
			}

			cfg := readTeam(t, l.config)
			checkLead(t, cfg, l.dir)
			started := teammatesStarted(t, l.record, len(spawns))
			wantEnv := map[string]string{"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1", "CLAUDE_CONFIG_DIR": l.config}
			for i, sp := range spawns {
				color := palette[i+1]
				wantSpawn := team.Spawn{
					AgentID: sp.Name + "@" + cfg.Name, Name: sp.Name, AgentType: sp.AgentType,
					Team: cfg.Name, Color: color, Model: "opus",
				}
				if got := team.ParseSpawn(sp.Args); got != wantSpawn || sp.Program != program || sp.Backend != backendTmux {
					t.Fatalf("spawn %+v reads as %+v, want %+v run with %s", sp, got, wantSpawn, program)
				}
				for _, flag := range [][]string{{"--settings", l.settings}, {"--effort", "high"}, {"--permission-mode", "acceptEdits"}, {"--plugin-dir", "/plugins/two"}} {
					if !slices.Contains(sp.Args, flag[1]) || sp.Args[slices.Index(sp.Args, flag[1])-1] != flag[0] {
						t.Fatalf("spawn args %q, want %s %s passed on", sp.Args, flag[0], flag[1])
					}
				}
				m, ok := cfg.ByPane(sp.Pane)
				if !ok || m.Name != sp.Name || m.AgentType != sp.AgentType || m.Color != color || m.Dir != l.dir {
					t.Fatalf("team member for %s: %+v, %v", sp.Pane, m, ok)
				}
				for option, want := range map[string]string{
					"pane-border-format": BorderFormat(color), "window-style": "bg=default,fg=" + tmuxColors[color],
					"pane-border-style": "fg=" + tmuxColors[color], "remain-on-exit": "failed",
				} {
					if got := s.run(t, "show-options", "-p", "-v", "-t", sp.Pane, option); got != want {
						t.Fatalf("%s %s = %q, want %q", sp.Pane, option, got, want)
					}
				}
				if got := s.run(t, "display-message", "-p", "-t", sp.Pane, "#{pane_title}"); got != sp.Name {
					t.Fatalf("pane title %q, want %q", got, sp.Name)
				}
				inv := started[sp.Pane]
				if !slices.Equal(inv.Args, sp.Args) || inv.Cwd != l.dir || (inv.Env["LYNA_TMUX_TEST_LAUNCHER"] == "1") != tc.launcher {
					t.Fatalf("teammate in %s started as %+v, want args %q in %s", sp.Pane, inv, sp.Args, l.dir)
				}
				for k, v := range wantEnv {
					if inv.Env[k] != v {
						t.Fatalf("teammate env %s = %q, want %q", k, inv.Env[k], v)
					}
				}
				tmuxtest.WaitFor(t, sp.Name+" ready", func() bool {
					return strings.Contains(s.run(t, "capture-pane", "-p", "-t", sp.Pane), ReadyLine)
				})
			}
			// The lead and every teammate ran the hooks of the settings the
			// teammates were handed, each in its own pane.
			data, err := os.ReadFile(l.marker)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{s.lead}
			for _, sp := range spawns {
				want = append(want, sp.Pane)
			}
			got := strings.Fields(string(data))
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("SessionStart ran in %q, want %q", got, want)
			}
		})
	}
}

// TestOpenTeamGate holds a lead at its gate: each teammate opens when the test
// signals the channel, and not before.
func TestOpenTeamGate(t *testing.T) {
	s := startTeamServer(t)
	l := newTeamLead(t)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- l.main(t, s, "/bin/sh", nil, []string{EnvTeammates + "=alpha,beta", EnvTeamGate + "=fake-gate"}, &stderr)
	}()
	opened := func(n int) {
		t.Helper()
		s.run(t, "wait-for", "-S", "fake-gate")
		tmuxtest.WaitFor(t, "teammate opened", func() bool { return len(ReadTeammates(t, l.record)) == n })
	}
	opened(1)
	select {
	case code := <-done:
		t.Fatalf("the lead finished at the gate with %d, stderr %q", code, stderr.String())
	default:
	}
	opened(2)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Main = %d, stderr %q", code, stderr.String())
		}
	case <-time.After(time.Minute):
		t.Fatal("the lead did not finish after its last teammate")
	}
	var gates []int
	for i, r := range ReadTmuxRuns(t, l.record) {
		if r.Args[2] == "wait-for" {
			gates = append(gates, i)
			if !slices.Equal(r.Args[3:], []string{"fake-gate"}) {
				t.Fatalf("gate %q", r.Args)
			}
		}
	}
	runs := ReadTmuxRuns(t, l.record)
	if len(gates) != 2 || gates[0] != 0 || runs[gates[1]-1].Args[2] != "respawn-pane" {
		t.Fatalf("gates at %v of %d runs, want one before each teammate", gates, len(runs))
	}
}

// TestReadRecordsCompleteLines reads a record file the way a test polls one
// while fakes in other panes append to it: a line still being written is not
// read yet.
func TestReadRecordsCompleteLines(t *testing.T) {
	line := `{"kind":"invocation","program":"claude","args":[],"cwd":"/w","pid":1,"env":{}}` + "\n"
	cases := []struct {
		name, data string
		want       int
	}{
		{name: "an empty file", data: ""},
		{name: "one line", data: line, want: 1},
		{name: "a line still being written", data: line + `{"kind":"invoca`, want: 1},
		{name: "only a line still being written", data: `{"kind":"invoca`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "record.jsonl")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := ReadRecords(t, path); len(got) != tc.want {
				t.Fatalf("ReadRecords = %+v, want %d", got, tc.want)
			}
		})
	}
}
