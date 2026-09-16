package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/statusline"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// glueEnv runs commands the way Claude Code and tmux start them: with stdin,
// an explicit environment and a fixed clock, against an isolated home.
type glueEnv struct {
	root     string
	env      map[string]string
	home     string
	exe      string
	tmuxBin  string
	term     Terminal
	now      time.Time
	lookPath func(string) (string, error)
	looked   []string
}

func newGlueEnv(t *testing.T) *glueEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &glueEnv{
		root:    root,
		env:     map[string]string{session.EnvHome: filepath.Join(root, "lt"), "HOME": root, "PATH": os.Getenv("PATH")},
		home:    root,
		exe:     filepath.Join(root, "bin", "lyna-tmux"),
		tmuxBin: filepath.Join(root, "no-tmux"),
		now:     time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	}
}

func (e *glueEnv) configFile() string {
	return filepath.Join(e.env[session.EnvHome], "config", "config.toml")
}

func (e *glueEnv) logFile() string {
	return filepath.Join(e.env[session.EnvHome], "state", "lyna-tmux.log")
}

func (e *glueEnv) writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.configFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.configFile(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *glueEnv) run(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	env := make(map[string]string, len(e.env))
	var environ []string
	for k, v := range e.env {
		env[k] = v
		environ = append(environ, k+"="+v)
	}
	host := app.Host{Getenv: func(k string) string { return env[k] }, Environ: environ, Home: e.home, Exe: e.exe, TmuxBin: e.tmuxBin}
	var out, errOut bytes.Buffer
	d := Deps{
		Host: func() (app.Host, error) { return host, nil },
		LookPath: func(name string) (string, error) {
			e.looked = append(e.looked, name)
			return e.lookPath(name)
		},
		Getwd:    func() (string, error) { return e.root, nil },
		Terminal: func() Terminal { return e.term },
		Now:      func() time.Time { return e.now },
	}
	code = run(t.Context(), NewRootWith(Streams{In: strings.NewReader(stdin), Out: &out, Err: &errOut}, d), args)
	return code, out.String(), errOut.String()
}

// glueServer is an isolated tmux server reached the way hooks reach it: through
// its socket path in $TMUX.
type glueServer struct {
	*tmuxtest.Server
	socket string
}

func startGlueServer(t *testing.T) *glueServer {
	t.Helper()
	srv := tmuxtest.Start(t)
	socket, err := srv.Client.Display(tmuxtest.Context(t), "", "#{socket_path}")
	if err != nil {
		t.Fatal(err)
	}
	return &glueServer{Server: srv, socket: socket}
}

// session creates a detached session and returns its pane, window and session IDs.
func (s *glueServer) session(t *testing.T, name string) (pane, window, id string) {
	t.Helper()
	out, err := s.Client.Run(tmuxtest.Context(t), "new-session", "-d", "-s", name, "-x", "80", "-y", "24",
		"-P", "-F", "#{pane_id} #{window_id} #{session_id}", "sleep 3600")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(out)
	if len(f) != 3 {
		t.Fatalf("new-session printed %q", out)
	}
	return f[0], f[1], f[2]
}

func TestGlueHookArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantEvent  string
		wantPlugin bool
	}{
		{name: "event", args: []string{"Stop"}, wantEvent: "Stop"},
		{name: "plugin first", args: []string{"--plugin", "Stop"}, wantEvent: "Stop", wantPlugin: true},
		{name: "plugin after event", args: []string{"Stop", "--plugin"}, wantEvent: "Stop", wantPlugin: true},
		{name: "extra arguments ignored", args: []string{"Stop", "extra", "--x"}, wantEvent: "Stop"},
		{name: "unknown flag is the event", args: []string{"--x", "Stop"}, wantEvent: "--x"},
		{name: "plugin only", args: []string{"--plugin"}, wantPlugin: true},
		{name: "empty event kept", args: []string{"", "Stop"}},
		{name: "nothing", args: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, plugin := hookArgs(tc.args)
			if event != tc.wantEvent || plugin != tc.wantPlugin {
				t.Fatalf("hookArgs(%q) = %q, %v; want %q, %v", tc.args, event, plugin, tc.wantEvent, tc.wantPlugin)
			}
		})
	}
}

func TestGlueStatus(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		wantCode int
	}{
		{name: "success", code: 0, wantCode: 0},
		{name: "failure keeps the code", code: 3, wantCode: 3},
		{name: "one", code: 1, wantCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := glueStatus(tc.code)
			if got := exitCode(err); got != tc.wantCode {
				t.Fatalf("exit code %d, want %d (err %v)", got, tc.wantCode, err)
			}
		})
	}
}

// TestGlueHookCLI drives `lyna-tmux hook` exactly as Claude Code does, with the
// payload on stdin and TMUX/TMUX_PANE pointing at a real pane, and reads the
// resulting state back from tmux.
func TestGlueHookCLI(t *testing.T) {
	srv := startGlueServer(t)
	pane, _, _ := srv.session(t, "agent")
	e := newGlueEnv(t)

	// Record every tmux invocation so the test can prove the hook never runs
	// tmux -V or loads a configuration file (what opening the server does).
	tmuxLog := filepath.Join(e.root, "tmux.argv")
	wrapper := filepath.Join(e.root, "tmux-wrapper")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + tmux.ShellQuote(tmuxLog) + "\nexec " + tmux.ShellQuote(srv.Bin) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	e.tmuxBin = wrapper
	e.env["TMUX"] = srv.socket + ",1,0"
	e.env["TMUX_PANE"] = pane
	homeFile := filepath.Join(e.root, "not-a-dir")
	if err := os.WriteFile(homeFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ltHome := e.env[session.EnvHome]

	state := func(t *testing.T) string {
		t.Helper()
		v, err := srv.Client.ShowOption(tmuxtest.Context(t), "-p", pane, tmux.OptState)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	steps := []struct {
		name    string
		args    []string
		stdin   string
		env     map[string]string // overrides for this step only
		noHome  bool
		config  string
		want    string
		wantLog string
	}{
		{name: "session start is idle", args: []string{"hook", "SessionStart"}, stdin: `{"source":"startup","cwd":"/"}`, want: "idle"},
		{name: "prompt is busy", args: []string{"hook", "UserPromptSubmit"}, stdin: `{"prompt":"hi"}`, want: "busy"},
		{name: "other tool keeps busy", args: []string{"hook", "PreToolUse"}, stdin: `{"tool_name":"Read"}`, want: "busy"},
		{name: "question waits", args: []string{"hook", "PreToolUse"}, stdin: `{"tool_name":"AskUserQuestion"}`, want: "waiting"},
		{name: "tool use is busy again", args: []string{"hook", "PostToolUse"}, stdin: `{"tool_name":"Edit"}`, want: "busy"},
		{name: "permission prompt waits", args: []string{"hook", "Notification"}, stdin: `{"notification_type":"permission_prompt"}`, want: "waiting"},
		{name: "stop is idle", args: []string{"hook", "Stop"}, stdin: `{}`, want: "idle"},
		{name: "plugin skipped in managed pane", args: []string{"hook", "--plugin", "UserPromptSubmit"}, env: map[string]string{session.EnvManaged: "1"}, want: "idle"},
		{name: "plugin runs outside managed pane", args: []string{"hook", "--plugin", "UserPromptSubmit"}, stdin: `{}`, want: "busy"},
		{name: "plugin flag after the event", args: []string{"hook", "Stop", "--plugin"}, env: map[string]string{session.EnvManaged: "1"}, want: "busy"},
		{name: "extra arguments", args: []string{"hook", "Stop", "extra", "--unknown"}, stdin: `{}`, want: "idle"},
		{name: "outside tmux", args: []string{"hook", "UserPromptSubmit"}, env: map[string]string{"TMUX": ""}, want: "idle"},
		{name: "unknown event", args: []string{"hook", "NoSuchEvent"}, want: "idle", wantLog: `unknown event "NoSuchEvent"`},
		{name: "missing event", args: []string{"hook"}, want: "idle", wantLog: `unknown event ""`},
		{name: "unknown flag", args: []string{"hook", "--verbose"}, want: "idle", wantLog: `unknown event "--verbose"`},
		{name: "help flag", args: []string{"hook", "--help"}, want: "idle", wantLog: `unknown event "--help"`},
		{name: "malformed payload falls back to the matcher", args: []string{"hook", "PreToolUse"}, stdin: "not json", want: "waiting"},
		{name: "config error", config: "[ui]\ntheme = \"nope\"\n", args: []string{"hook", "UserPromptSubmit"}, stdin: `{}`, want: "busy"},
		{name: "relative LYNA_TMUX_HOME and no home", noHome: true, env: map[string]string{session.EnvHome: "relative/home", "HOME": ""}, args: []string{"hook", "Stop"}, want: "idle"},
		{name: "LYNA_TMUX_HOME is a file", env: map[string]string{session.EnvHome: homeFile}, args: []string{"hook", "NoSuchEvent"}, want: "idle"},
		{name: "session end clears the state", args: []string{"hook", "SessionEnd"}, stdin: `{"reason":"exit"}`, want: ""},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			saved := map[string]string{}
			for k, v := range st.env {
				saved[k] = e.env[k]
				e.env[k] = v
			}
			if st.noHome {
				e.home = ""
			}
			t.Cleanup(func() {
				for k, v := range saved {
					e.env[k] = v
				}
				e.home = e.root
			})
			if st.config != "" {
				e.writeConfig(t, st.config)
			}
			logBefore, _ := os.ReadFile(filepath.Join(ltHome, "state", "lyna-tmux.log"))

			code, stdout, stderr := e.run(t, st.stdin, st.args...)
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q; a hook must exit 0 silently", code, stdout, stderr)
			}
			if got := state(t); got != st.want {
				t.Fatalf("@lt_state %q, want %q", got, st.want)
			}
			if st.wantLog != "" {
				logAfter, _ := os.ReadFile(filepath.Join(ltHome, "state", "lyna-tmux.log"))
				if added := strings.TrimPrefix(string(logAfter), string(logBefore)); !strings.Contains(added, st.wantLog) {
					t.Fatalf("log gained %q, want %q", added, st.wantLog)
				}
			}
		})
	}

	argv, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("the hook never ran tmux through the host tmux binary: %v", err)
	}
	for line := range strings.Lines(string(argv)) {
		if f := strings.Fields(line); len(f) > 0 && (f[0] == "-V" || f[0] == "-f") {
			t.Fatalf("hook opened the server (tmux %s)", strings.TrimSpace(line))
		}
	}
	if _, err := os.Stat(filepath.Join(ltHome, "state", "tmux.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hook wrote the generated tmux configuration: %v", err)
	}
}

func TestGlueStatuslineCLI(t *testing.T) {
	e := newGlueEnv(t)
	e.env["NO_COLOR"] = "1"
	e.env["COLUMNS"] = "200"
	e.env[session.EnvSandbox] = "strict"
	resets := strconv.FormatInt(e.now.Add(90*time.Minute).Unix(), 10)
	payload := `{"model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":` + resets + `}}}`
	getenv := func(k string) string { return e.env[k] }
	render := func(data string, now time.Time, getenv func(string) string) string {
		return statusline.Render([]byte(data), getenv, now) + "\n"
	}
	// The expectations below only prove the wiring if the rendered line
	// depends on the clock and the environment.
	if render(payload, e.now, getenv) == render(payload, e.now.Add(-time.Hour), getenv) ||
		render(payload, e.now, getenv) == render(payload, e.now, func(string) string { return "" }) {
		t.Fatal("status line does not depend on the clock or environment; the test proves nothing")
	}
	cases := []struct {
		name     string
		stdin    string
		args     []string
		wantCode int
		wantOut  string
		errHas   string
	}{
		{name: "renders stdin with the host environment and clock", stdin: payload, args: []string{"statusline"}, wantOut: render(payload, e.now, getenv)},
		{name: "empty stdin prints a minimal line", args: []string{"statusline"}, wantOut: render("", e.now, getenv)},
		{name: "malformed stdin prints a minimal line", stdin: "{", args: []string{"statusline"}, wantOut: render("{", e.now, getenv)},
		{name: "rejects arguments", args: []string{"statusline", "extra"}, wantCode: 1, errHas: `unknown command "extra"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := e.run(t, tc.stdin, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %s)", code, tc.wantCode, stderr)
			}
			if tc.wantCode == 0 && stdout != tc.wantOut {
				t.Fatalf("stdout %q, want %q", stdout, tc.wantOut)
			}
			if tc.errHas != "" && !containsFolded(stderr, tc.errHas) {
				t.Fatalf("stderr %q, want %q", stderr, tc.errHas)
			}
		})
	}
}

// TestGlueBellForwardCLI runs bell-forward as the alert-bell hook does and
// checks the bell flag tmux raises on the origin window.
func TestGlueBellForwardCLI(t *testing.T) {
	srv := startGlueServer(t)
	e := newGlueEnv(t)
	e.tmuxBin = srv.Bin
	e.env["TMUX"] = srv.socket + ",1,0"
	bellFlag := func(t *testing.T, window string) string {
		t.Helper()
		v, err := srv.Client.Display(tmuxtest.Context(t), window, "#{window_bell_flag}")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name     string
		config   string
		popup    string
		wantBell bool
	}{
		{name: "default prefix", popup: "claude-1a2b3c4d", wantBell: true},
		{name: "configured prefix", config: "[popup]\nsession_prefix = \"ai-\"\n", popup: "ai-3c4d5e6f", wantBell: true},
		{name: "broken config falls back to the default prefix", config: "[popup]\nsession_prefix = 3\n", popup: "claude-5e6f7081", wantBell: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.RemoveAll(filepath.Dir(e.configFile())); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				e.writeConfig(t, tc.config)
			}
			_, origin, _ := srv.session(t, "work-"+strconv.Itoa(i))
			_, _, popupID := srv.session(t, tc.popup)
			if _, err := srv.Client.Run(tmuxtest.Context(t), "set-option", "-t", popupID, tmux.OptOrigin, origin); err != nil {
				t.Fatal(err)
			}
			if bellFlag(t, origin) != "0" {
				t.Fatal("bell flag set before forwarding")
			}
			code, stdout, stderr := e.run(t, "", "bell-forward", popupID)
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if data, _ := os.ReadFile(e.logFile()); len(data) != 0 {
				t.Fatalf("bell-forward logged %q", data)
			}
			if tc.wantBell {
				tmuxtest.WaitFor(t, "bell flag on the origin window", func() bool { return bellFlag(t, origin) == "1" })
			}
		})
	}

	errCases := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		errHas   string
	}{
		// A hook exits 0 whatever tmux hands it: tmux shows a failing hook to
		// the user, and the reason belongs in the log instead.
		{name: "a missing session id is logged", args: []string{"bell-forward"}},
		{name: "extra arguments are ignored", args: []string{"bell-forward", "$1", "$2"}},
		{name: "an unknown flag is logged", args: []string{"bell-forward", "--nope"}},
		{name: "invalid session id is logged", args: []string{"bell-forward", "name"}},
		{name: "outside tmux", args: []string{"bell-forward", "$1"}, env: map[string]string{"TMUX": ""}},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				old := e.env[k]
				e.env[k] = v
				t.Cleanup(func() { e.env[k] = old })
			}
			code, stdout, stderr := e.run(t, "", tc.args...)
			if code != tc.wantCode || stdout != "" {
				t.Fatalf("exit %d, want %d, stdout %q, stderr %q", code, tc.wantCode, stdout, stderr)
			}
			if tc.errHas != "" && !containsFolded(stderr, tc.errHas) {
				t.Fatalf("stderr %q, want %q", stderr, tc.errHas)
			}
			if tc.errHas == "" && stderr != "" {
				t.Fatalf("stderr %q", stderr)
			}
		})
	}
	data, _ := os.ReadFile(e.logFile())
	for _, want := range []string{`invalid session ID "name"`, `invalid session ID ""`, `invalid session ID "--nope"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("log %q lacks %s", data, want)
		}
	}
}

func TestGlueCommandsHidden(t *testing.T) {
	e := newGlueEnv(t)
	code, stdout, stderr := e.run(t, "", "--help")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, hidden := range []string{"hook", "statusline", "bell-forward"} {
		for line := range strings.Lines(stdout) {
			if f := strings.Fields(line); len(f) > 0 && f[0] == hidden {
				t.Fatalf("%s listed in help:\n%s", hidden, stdout)
			}
		}
	}
	if !strings.Contains(stdout, "theme") {
		t.Fatalf("theme missing from help:\n%s", stdout)
	}
}

func TestPluginClaudeCLI(t *testing.T) {
	e := newGlueEnv(t)
	other := filepath.Join(e.root, "other", "lyna-tmux")
	link := filepath.Join(e.root, "path", "lyna-tmux")
	for _, p := range []string{e.exe, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.exe, link); err != nil {
		t.Fatal(err)
	}
	steps := []string{
		"/plugin marketplace add bayoudhdev/lyna-claude-tmux\n",
		"/plugin install lyna-tmux@lyna-tmux\n",
		"/reload-plugins\n",
		"claude plugin marketplace add bayoudhdev/lyna-claude-tmux\n",
		"claude plugin install lyna-tmux@lyna-tmux\n",
		"Workspaces started by lyna-tmux create already have these hooks and do not need the\nplugin",
	}
	cases := []struct {
		name     string
		args     []string
		lookPath func(string) (string, error)
		wantCode int
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{
			name: "this binary on PATH", args: []string{"plugin", "claude"},
			lookPath: func(string) (string, error) { return e.exe, nil },
			outHas:   append([]string{"which is this binary (" + e.exe + ")"}, steps...),
		},
		{
			name: "symbolic link to this binary", args: []string{"plugin", "claude"},
			lookPath: func(string) (string, error) { return link, nil },
			outHas:   append([]string{"which is this binary (" + link + ")"}, steps...),
		},
		{
			name: "not on PATH", args: []string{"plugin", "claude"},
			lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
			outHas:   steps, outLacks: []string{"which is this binary"},
			errHas: []string{"Warning: lyna-tmux is not on PATH, so the plugin hooks do nothing", e.exe},
		},
		{
			name: "another binary on PATH", args: []string{"plugin", "claude"},
			lookPath: func(string) (string, error) { return other, nil },
			outHas:   steps, outLacks: []string{"which is this binary"},
			errHas: []string{"Warning: lyna-tmux on PATH is " + other + ", not this binary (" + e.exe + ")"},
		},
		{
			name: "binary on PATH is gone", args: []string{"plugin", "claude"},
			lookPath: func(string) (string, error) { return filepath.Join(e.root, "gone"), nil },
			outLacks: []string{"which is this binary"}, errHas: []string{"not this binary"},
		},
		{
			name: "rejects arguments", args: []string{"plugin", "claude", "extra"}, wantCode: 1,
			lookPath: func(string) (string, error) { return e.exe, nil }, errHas: []string{`unknown command "extra"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.lookPath, e.looked = tc.lookPath, nil
			code, stdout, stderr := e.run(t, "", tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if len(tc.errHas) == 0 && stderr != "" {
				t.Fatalf("unexpected stderr %q", stderr)
			}
			if tc.wantCode == 0 && (len(e.looked) != 1 || e.looked[0] != hook.PluginBinary) {
				t.Fatalf("looked up %q, want the binary the hooks run", e.looked)
			}
		})
	}
}

// TestPluginClaudeMatchesMarketplace keeps the printed install commands in
// step with the marketplace and plugin manifests committed in this repository.
func TestPluginClaudeMatchesMarketplace(t *testing.T) {
	read := func(rel string, v any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	read(".claude-plugin/marketplace.json", &market)
	if len(market.Plugins) != 1 {
		t.Fatalf("%d plugins in the marketplace", len(market.Plugins))
	}
	var manifest struct {
		Repository string `json:"repository"`
	}
	read(filepath.ToSlash(filepath.Join(market.Plugins[0].Source, ".claude-plugin", "plugin.json")), &manifest)

	steps := pluginClaudeSteps("")
	cases := []struct {
		name string
		want string
	}{
		{name: "marketplace source is the repository", want: "/plugin marketplace add " + strings.TrimPrefix(manifest.Repository, "https://github.com/") + "\n"},
		{name: "plugin id names plugin and marketplace", want: "/plugin install " + market.Plugins[0].Name + "@" + market.Name + "\n"},
		{name: "binary the hooks run", want: "hooks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(steps, tc.want) {
				t.Fatalf("steps lack %q:\n%s", tc.want, steps)
			}
		})
	}
	hooks, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(hook.PluginHooksPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hooks), "command -v "+hook.PluginBinary+" ") {
		t.Fatalf("hooks.json does not look up %s on PATH, which plugin claude checks", hook.PluginBinary)
	}
}
