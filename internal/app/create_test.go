package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// createEnv is a test host whose claude is the fake and whose lyna-tmux
// binary is a script recording the arguments of every pane that runs it.
type createEnv struct {
	*testHost
	record  string
	exeLog  string
	project string
}

func newCreateEnv(t *testing.T) *createEnv {
	t.Helper()
	h := newTestHost(t)
	fake := fakeclaude.Build(t)
	e := &createEnv{
		testHost: h,
		record:   filepath.Join(h.root, "claude.jsonl"),
		exeLog:   filepath.Join(h.root, "exe.log"),
		project:  filepath.Join(h.root, "projects", "api"),
	}
	mkdir(t, filepath.Join(e.project, ".git"))
	mkdir(t, filepath.Join(e.project, "src"))
	mkdir(t, filepath.Dir(h.Exe))
	script := "#!/bin/sh\n{ printf '%s|' \"$@\"; echo; } >> '" + e.exeLog + "'\nexec sleep 3600\n"
	if err := os.WriteFile(h.Exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	h.env[fakeclaude.EnvRecord] = e.record
	h.env["CLAUDE_CONFIG_DIR"] = filepath.Join(h.root, "claude-home")
	h.env["SHELL"] = "/bin/sh"
	h.refreshEnviron()
	h.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return fake, nil
		}
		return exec.LookPath(name)
	}
	return e
}

// invocations waits for at least n recorded claude starts.
func (e *createEnv) invocations(t *testing.T, n int) []fakeclaude.Invocation {
	t.Helper()
	var got []fakeclaude.Invocation
	tmuxtest.WaitFor(t, "claude started", func() bool {
		if _, err := os.Stat(e.record); err != nil {
			return false
		}
		got = got[:0]
		for _, r := range fakeclaude.ReadRecords(t, e.record) {
			if r.Kind == "invocation" {
				got = append(got, r)
			}
		}
		return len(got) >= n
	})
	return got
}

func (e *createEnv) exeCalls(t *testing.T, n int) []string {
	t.Helper()
	var lines []string
	tmuxtest.WaitFor(t, "lyna-tmux pane started", func() bool {
		data, err := os.ReadFile(e.exeLog)
		if err != nil {
			return false
		}
		// Only complete lines count: the file exists before a line is written.
		text := string(data)
		last := strings.LastIndex(text, "\n")
		if last < 0 {
			return false
		}
		lines = strings.Split(text[:last], "\n")
		return len(lines) >= n
	})
	return lines
}

func roles(t *testing.T, s *Server, name string) []string {
	t.Helper()
	out, err := s.Client.Run(tmuxtest.Context(t), "list-panes", "-t", tmux.ExactSession(name), "-F", "#{@lt_role}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(out)
}

func TestCreateWorkspace(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)

	res, err := s.Create(ctx, e.Host, CreateRequest{Dir: filepath.Join(e.project, "src")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "api" || res.Existing || res.Project != e.project || len(res.Warnings) != 0 {
		t.Fatalf("result %+v", res)
	}

	t.Run("layout and session options", func(t *testing.T) {
		if got := roles(t, s, "api"); !slices.Equal(got, []string{"claude", "shell"}) {
			t.Fatalf("roles %q", got)
		}
		sessions, err := s.Sessions(ctx)
		if err != nil || len(sessions) != 1 {
			t.Fatalf("sessions %+v, %v", sessions, err)
		}
		x := sessions[0]
		if !x.Managed || x.Project != e.project || x.Sandbox != "standard" || x.Layout != "duo" {
			t.Fatalf("session %+v", x)
		}
		iso, err := s.Client.ShowOption(ctx, "", tmux.ExactSession("api"), tmux.OptIsolation)
		if err != nil || iso != "bash" {
			t.Fatalf("session isolation %q, %v", iso, err)
		}
		loaded, err := s.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash)
		if err != nil || loaded != s.Fingerprint {
			t.Fatalf("loaded configuration %q, %v", loaded, err)
		}
	})

	inv := e.invocations(t, 1)[0]
	t.Run("claude launch", func(t *testing.T) {
		if inv.Cwd != e.project {
			t.Fatalf("cwd %q", inv.Cwd)
		}
		if !slices.Contains(inv.Args, "--name=api") || inv.SettingsPath == "" || !slices.Contains(inv.Args, "--settings="+inv.SettingsPath) {
			t.Fatalf("args %q", inv.Args)
		}
		socket, err := s.Client.Display(ctx, "", "#{socket_path}")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"LYNA_TMUX_MANAGED": "1", "LYNA_TMUX_SESSION": "api", "LYNA_TMUX_SANDBOX": "standard",
			"LYNA_TMUX_SOCKET": socket, "CLAUDE_CODE_TMUX_TRUECOLOR": "1",
			"LYNA_TMUX_BELL": "1", "LYNA_TMUX_THEME": "lyna", "LYNA_TMUX_ICONS": "auto", "LYNA_TMUX_COLOR": "auto",
		}
		for k, v := range want {
			if inv.Env[k] != v {
				t.Fatalf("env %s = %q, want %q (all %v)", k, inv.Env[k], v, inv.Env)
			}
		}
		if _, leaked := inv.Env["CLAUDE_CODE_MESSAGING_TOKEN"]; leaked {
			t.Fatal("parent messaging token reached the Claude pane")
		}
	})

	t.Run("settings file", func(t *testing.T) {
		assertMode(t, inv.SettingsPath, 0o600)
		if filepath.Dir(inv.SettingsPath) != s.Paths.SettingsDir() {
			t.Fatalf("settings in %s", inv.SettingsPath)
		}
		var doc struct {
			Hooks      map[string]json.RawMessage `json:"hooks"`
			StatusLine json.RawMessage            `json:"statusLine"`
			Sandbox    struct {
				Enabled bool `json:"enabled"`
			} `json:"sandbox"`
			NotifyChannel string `json:"preferredNotifChannel"`
		}
		if err := json.Unmarshal(inv.Settings, &doc); err != nil {
			t.Fatal(err)
		}
		if !doc.Sandbox.Enabled || len(doc.Hooks) == 0 || doc.StatusLine == nil {
			t.Fatalf("settings %s", inv.Settings)
		}
		if !strings.Contains(string(doc.Hooks["Stop"]), e.Exe) {
			t.Fatalf("Stop hook does not run %s: %s", e.Exe, doc.Hooks["Stop"])
		}
		// The agent runs in a pane, where the terminal it can see is tmux. The
		// launch passes the terminal outside, which this environment does not
		// name, so the alert is the bell every terminal has.
		if doc.NotifyChannel != termx.NotifyBell {
			t.Fatalf("preferredNotifChannel = %q, want %q", doc.NotifyChannel, termx.NotifyBell)
		}
		opt, err := s.Client.Display(ctx, res.Built.Panes[0], "#{"+tmux.OptSettings+"}")
		if err != nil || opt != inv.SettingsPath {
			t.Fatalf("pane settings option %q, %v", opt, err)
		}
	})

	// The agent's desktop notifications and progress bar are escape sequences
	// for the terminal outside tmux. The server refuses them, so a pane running
	// the user's own programs cannot reach their terminal; the pane running the
	// agent allows them.
	t.Run("passthrough is on for the Claude pane and off everywhere else", func(t *testing.T) {
		// -A resolves what the pane actually uses: a pane with no value of its
		// own inherits the window's, which is where the server's off lives.
		for i, want := range []string{"on", "off"} {
			got, err := s.Client.ShowOption(ctx, "-pA", res.Built.Panes[i], tmux.OptPassthrough)
			if err != nil || got != want {
				t.Fatalf("pane %d (%s) allow-passthrough = %q, %v; want %q", i, roles(t, s, "api")[i], got, err, want)
			}
		}
		global, err := s.Client.ShowOption(ctx, "-wg", "", tmux.OptPassthrough)
		if err != nil || global != "off" {
			t.Fatalf("server allow-passthrough = %q, %v; want off", global, err)
		}
	})

	t.Run("same project returns the running workspace", func(t *testing.T) {
		again, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: layout.Quad})
		if err != nil || again.Name != "api" || !again.Existing {
			t.Fatalf("result %+v, %v", again, err)
		}
		if got := roles(t, s, "api"); len(got) != 2 {
			t.Fatalf("existing workspace changed: %q", got)
		}
	})
}

func TestCreateWorkspaceRequests(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	other := filepath.Join(e.root, "other", "api")
	mkdir(t, other)
	third := filepath.Join(e.root, "third", "api")
	mkdir(t, third)
	file := filepath.Join(e.root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	userSettings := filepath.Join(e.root, "claude-home", "settings.json")

	type step struct {
		name      string
		setup     func(t *testing.T)
		req       CreateRequest
		wantErr   error
		errHas    string
		wantName  string
		existing  bool
		wantRoles []string
		check     func(t *testing.T, res CreateResult)
	}
	// nth waits for the n-th claude start of this test and returns it.
	nth := func(t *testing.T, n int) fakeclaude.Invocation {
		t.Helper()
		return e.invocations(t, n)[n-1]
	}
	steps := []step{
		{name: "missing directory", req: CreateRequest{Dir: filepath.Join(e.root, "nope")}, wantErr: os.ErrNotExist},
		{name: "file instead of directory", req: CreateRequest{Dir: file}, errHas: "not a directory"},
		{name: "unknown layout", req: CreateRequest{Dir: e.project, Layout: "nope"}, wantErr: layout.ErrUnknown},
		{
			name: "bypass refused under standard", req: CreateRequest{Dir: e.project, Launch: LaunchOptions{PermissionMode: "bypassPermissions"}},
			wantErr: sandbox.ErrBypassRefused,
		},
		{
			name: "process isolation needs the sandbox runtime", req: CreateRequest{Dir: e.project, Launch: LaunchOptions{Isolation: "process"}}, wantErr: ErrIsolationUnsupported,
			setup: func(t *testing.T) {
				t.Helper()
				saved := e.LookPath
				e.LookPath = func(name string) (string, error) {
					if name == sandbox.RuntimeBinary {
						return "", exec.ErrNotFound
					}
					return saved(name)
				}
				t.Cleanup(func() { e.LookPath = saved })
			},
		},
		{name: "invalid profile", req: CreateRequest{Dir: e.project, Launch: LaunchOptions{Sandbox: "loose"}}, errHas: "loose"},
		{
			name: "claude not installed", req: CreateRequest{Dir: e.project}, wantErr: claude.ErrNotFound,
			setup: func(t *testing.T) {
				t.Helper()
				saved := e.LookPath
				e.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
				t.Cleanup(func() { e.LookPath = saved })
			},
		},
		{
			name: "sandbox off from the configuration is refused", req: CreateRequest{Dir: e.project}, wantErr: ErrSandboxOffInConfig,
			setup: func(t *testing.T) {
				t.Helper()
				s.Config.Sandbox.Profile = "off"
				t.Cleanup(func() { s.Config.Sandbox.Profile = "standard" })
			},
		},
		{name: "nothing was created by failed requests", req: CreateRequest{Dir: e.project, Name: "probe", Layout: layout.Solo}, wantName: "probe", wantRoles: []string{"claude"}, check: func(t *testing.T, _ CreateResult) {
			t.Helper()
			sessions, err := s.Sessions(tmuxtest.Context(t))
			if err != nil || len(sessions) != 1 {
				t.Fatalf("sessions %+v, %v", sessions, err)
			}
			if inv := nth(t, 1); inv.Env["LYNA_TMUX_SESSION"] != "probe" {
				t.Fatalf("first launch %+v", inv)
			}
			if kerr := s.Kill(tmuxtest.Context(t), "probe"); kerr != nil {
				t.Fatal(kerr)
			}
		}},
		{
			name: "trio with continue, extra args and a user status line", wantName: "api", wantRoles: []string{"claude", "shell", "changes"},
			req: CreateRequest{Dir: e.project, Layout: layout.Trio, Launch: LaunchOptions{Continue: true, ExtraArgs: []string{"--verbose"}, Model: "opus", Effort: "high"}},
			setup: func(t *testing.T) {
				t.Helper()
				mkdir(t, filepath.Dir(userSettings))
				if err := os.WriteFile(userSettings, []byte(`{"statusLine":{"type":"command","command":"mine"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				s.Config.Claude.Bell, s.Config.UI.Theme, s.Config.UI.Icons, s.Config.UI.Color = false, "light", "nerd", "256"
				t.Cleanup(func() {
					s.Config.Claude.Bell, s.Config.UI.Theme, s.Config.UI.Icons, s.Config.UI.Color = true, "lyna", "auto", "auto"
				})
			},
			check: func(t *testing.T, _ CreateResult) {
				t.Helper()
				inv := nth(t, 2)
				n := len(inv.Args)
				if n < 2 || inv.Args[n-2] != "--continue" || inv.Args[n-1] != "--verbose" || !slices.Contains(inv.Args, "--model=opus") || !slices.Contains(inv.Args, "--effort=high") {
					t.Fatalf("args %q", inv.Args)
				}
				for k, v := range map[string]string{"LYNA_TMUX_BELL": "0", "LYNA_TMUX_THEME": "light", "LYNA_TMUX_ICONS": "nerd", "LYNA_TMUX_COLOR": "256"} {
					if inv.Env[k] != v {
						t.Fatalf("env %s = %q, want %q", k, inv.Env[k], v)
					}
				}
				if strings.Contains(string(inv.Settings), "statusLine") {
					t.Fatalf("user status line replaced: %s", inv.Settings)
				}
				calls := e.exeCalls(t, 1)
				if want := "watch|--session|api|--dir|" + e.project + "|"; calls[len(calls)-1] != want {
					t.Fatalf("changes pane ran %q, want %q", calls[len(calls)-1], want)
				}
			},
		},
		{name: "subdirectory finds the running workspace", req: CreateRequest{Dir: filepath.Join(e.project, "src")}, wantName: "api", existing: true},
		{name: "explicit name of the same project", req: CreateRequest{Dir: e.project, Name: "api"}, wantName: "api", existing: true},
		{name: "name taken by another project", req: CreateRequest{Dir: other, Name: "api"}, wantErr: ErrNameTaken},
		{name: "invalid name", req: CreateRequest{Dir: other, Name: "a:b"}, errHas: "invalid"},
		{
			name: "same directory name gets a suffix and an explicit off sandbox", wantName: "api-2", wantRoles: []string{"claude"},
			req: CreateRequest{Dir: other, Layout: layout.Solo, Launch: LaunchOptions{Sandbox: "off"}},
			check: func(t *testing.T, _ CreateResult) {
				t.Helper()
				sessions, err := s.Sessions(tmuxtest.Context(t))
				if err != nil {
					t.Fatal(err)
				}
				for _, x := range sessions {
					if x.Name == "api-2" && x.Sandbox != "off" {
						t.Fatalf("sandbox option %q", x.Sandbox)
					}
				}
				if inv := nth(t, 3); inv.Env["LYNA_TMUX_SANDBOX"] != "off" {
					t.Fatalf("pane sandbox env %q", inv.Env["LYNA_TMUX_SANDBOX"])
				}
			},
		},
		{
			// The terminal the user is looking at is read outside tmux and
			// handed to the agent, which cannot see it from inside a pane.
			name: "the notification channel follows the terminal outside", wantName: "api-3", wantRoles: []string{"claude"},
			req: CreateRequest{Dir: third, Layout: layout.Solo},
			setup: func(t *testing.T) {
				t.Helper()
				e.env["TERM_PROGRAM"] = "iTerm.app"
				t.Cleanup(func() { delete(e.env, "TERM_PROGRAM") })
				e.refreshEnviron()
			},
			check: func(t *testing.T, _ CreateResult) {
				t.Helper()
				var doc struct {
					NotifyChannel string `json:"preferredNotifChannel"`
				}
				if err := json.Unmarshal(nth(t, 4).Settings, &doc); err != nil {
					t.Fatal(err)
				}
				if doc.NotifyChannel != termx.NotifyITerm2Bell {
					t.Fatalf("preferredNotifChannel = %q, want %q", doc.NotifyChannel, termx.NotifyITerm2Bell)
				}
			},
		},
		{
			// The rail is asked for by the configuration, so the layout the
			// user chose is the layout they get, with the rail in front of it.
			name: "a workspace that always carries the rail", wantName: "rails", wantRoles: []string{"agents", "claude", "shell"},
			req: CreateRequest{Dir: other, Name: "rails", Layout: layout.Duo, Width: 240, Height: 60},
			setup: func(t *testing.T) {
				t.Helper()
				s.Config.UI.AgentsSidebar = config.SidebarAlways
				t.Cleanup(func() { s.Config.UI.AgentsSidebar = config.SidebarAuto })
			},
			check: func(t *testing.T, _ CreateResult) {
				t.Helper()
				panes, err := s.Client.ListPanes(tmuxtest.Context(t), tmux.ExactSession("rails"))
				if err != nil || len(panes) != 3 {
					t.Fatalf("panes %+v, %v", panes, err)
				}
				if panes[0].Width != layout.RailWidth {
					t.Fatalf("the rail is %d cells wide, want %d", panes[0].Width, layout.RailWidth)
				}
				// The workspace it follows is named on the command line: a rail
				// opened with the window is not one that closes with the agents.
				calls := e.exeCalls(t, 2)
				if want := "agents|--rail|--session|rails|"; calls[len(calls)-1] != want {
					t.Fatalf("the rail ran %q, want %q", calls[len(calls)-1], want)
				}
			},
		},
		{
			name: "custom layout with a command pane", wantName: "tests", wantRoles: []string{"claude", "command"},
			req: CreateRequest{Dir: other, Name: "tests", Layout: "tests"},
			setup: func(t *testing.T) {
				t.Helper()
				e.writeConfig(t, "[layouts.tests]\npanes = [{ role = \"claude\" }, { role = \"command\", split = \"down\", command = \"exec sleep 3600\" }]\n")
				s = openServer(t, e.testHost)
			},
		},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			res, err := s.Create(tmuxtest.Context(t), e.Host, st.req)
			switch {
			case st.wantErr != nil || st.errHas != "":
				if err == nil || st.wantErr != nil && !errors.Is(err, st.wantErr) || !strings.Contains(err.Error(), st.errHas) {
					t.Fatalf("err %v, want %v %q", err, st.wantErr, st.errHas)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if res.Name != st.wantName || res.Existing != st.existing || len(res.Warnings) != 0 {
				t.Fatalf("result %+v", res)
			}
			if st.wantRoles != nil {
				if got := roles(t, s, res.Name); !slices.Equal(got, st.wantRoles) {
					t.Fatalf("roles %q, want %q", got, st.wantRoles)
				}
			}
			if st.check != nil {
				st.check(t, res)
			}
		})
	}
}

func TestCreatePrunesSettings(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	dir := s.Paths.SettingsDir()
	mkdir(t, dir)
	old := time.Now().Add(-2 * settingsGrace)
	write := func(content string, mtime time.Time) string {
		t.Helper()
		path := filepath.Join(dir, claudecfg.SettingsFileName([]byte(content)))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stale := write(`{"stale":true}`, old)
	recent := write(`{"recent":true}`, time.Now())
	inUse := write(`{"inUse":true}`, old)
	startWorkspace(t, s, "holder")
	if _, err := s.Client.Run(ctx, "set-option", "-p", "-t", "=holder:", tmux.OptSettings, inUse); err != nil {
		t.Fatal(err)
	}

	res, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo})
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("create %+v, %v", res, err)
	}
	current := e.invocations(t, 1)[0].SettingsPath
	cases := []struct {
		name string
		path string
		kept bool
	}{
		{"stale file removed", stale, false},
		{"recent file kept", recent, true},
		{"file referenced by a pane kept", inUse, true},
		{"current launch kept", current, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := os.Stat(tc.path)
			if kept := err == nil; kept != tc.kept {
				t.Fatalf("%s kept %v, want %v (%v)", tc.path, kept, tc.kept, err)
			}
		})
	}
}

func TestSocketPath(t *testing.T) {
	tmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		tmpdir string
		want   string
	}{
		{"default", "", tmp},
		{"resolved symlink", link, realDir},
		{"missing directory kept", "/nonexistent/dir", "/nonexistent/dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SocketPath(func(k string) string {
				if k == "TMUX_TMPDIR" {
					return tc.tmpdir
				}
				return ""
			}, "lyna-tmux")
			want := filepath.Join(tc.want, "tmux-"+strconv.Itoa(os.Getuid()), "lyna-tmux")
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

// TestResolveName covers the name a create resolves to for a project: the
// running workspace, an explicit name, or a fresh one. A workspace whose name
// starts with the popup prefix, such as the project directory claude-api, is
// a workspace like any other and is returned instead of a second one.
func TestResolveName(t *testing.T) {
	const root = "/src/claude-api"
	const other = "/src/web"
	popup := session.PopupName(session.DefaultPopupPrefix, root)
	ws := func(name, project string) tmux.Session {
		return tmux.Session{Name: name, Project: project, Managed: true}
	}
	cases := []struct {
		name      string
		requested string
		// root overrides the project root of the case.
		root         string
		sessions     []tmux.Session
		wantName     string
		wantExisting bool
		wantErr      error
	}{
		{name: "first workspace of the project", wantName: "claude-api"},
		{name: "directory without usable characters", root: "/src/###", wantName: session.DefaultName},
		{name: "directory without usable characters twice", root: "/src/###", sessions: []tmux.Session{{Name: session.DefaultName}}, wantName: session.DefaultName + "-2"},
		{name: "running workspace is returned", sessions: []tmux.Session{ws("claude-api", root)}, wantName: "claude-api", wantExisting: true},
		{name: "renamed workspace is returned", sessions: []tmux.Session{ws("work", root)}, wantName: "work", wantExisting: true},
		{name: "popup session of the project is skipped", sessions: []tmux.Session{ws(popup, root)}, wantName: "claude-api"},
		{name: "popup session does not take the name", sessions: []tmux.Session{ws(popup, other)}, wantName: "claude-api"},
		{name: "unmanaged session of the project is not a workspace", sessions: []tmux.Session{{Name: "claude-api", Project: root}}, wantName: "claude-api-2"},
		{name: "name of another project gets a suffix", sessions: []tmux.Session{ws("claude-api", other)}, wantName: "claude-api-2"},
		{name: "requested name is free", requested: "api", wantName: "api"},
		{name: "requested name is the running workspace", requested: "work", sessions: []tmux.Session{ws("work", root)}, wantName: "work", wantExisting: true},
		{name: "requested name of another project", requested: "work", sessions: []tmux.Session{ws("work", other)}, wantErr: ErrNameTaken},
		{name: "requested name of a plain session", requested: "work", sessions: []tmux.Session{{Name: "work"}}, wantErr: ErrNameTaken},
		{name: "requested name is invalid", requested: "a:b", wantErr: session.ErrInvalidName},
	}
	s := &Server{Config: config.Default()}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := root
			if tc.root != "" {
				dir = tc.root
			}
			name, existing, err := s.resolveName(tc.requested, dir, tc.sessions)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name != tc.wantName || existing != tc.wantExisting {
				t.Fatalf("resolveName = %q, existing %v; want %q, %v", name, existing, tc.wantName, tc.wantExisting)
			}
		})
	}
}

func TestCreateIgnoresPopupSessions(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	popup := session.PopupName(s.Config.Popup.SessionPrefix, e.project)
	startWorkspace(t, s, popup)
	if _, err := s.Client.Batch(ctx,
		tmux.Command{"set-option", "-t", tmux.ExactSession(popup), tmux.OptManaged, "1"},
		tmux.Command{"set-option", "-t", tmux.ExactSession(popup), tmux.OptProject, e.project},
	); err != nil {
		t.Fatal(err)
	}
	res, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo})
	if err != nil || res.Existing || res.Name != "api" {
		t.Fatalf("result %+v, %v", res, err)
	}
}

// racingExecutor runs tmux for real and, right before the first new-session,
// lets a competitor take the session name.
type racingExecutor struct {
	env        []string
	competitor func(ctx context.Context)
	raced      bool
}

func (r *racingExecutor) Exec(ctx context.Context, bin string, args []string) (tmux.Result, error) {
	if !r.raced && slices.Contains(args, "new-session") {
		r.raced = true
		r.competitor(ctx)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := tmux.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

func TestCreateRetriesWhenTheNameIsTaken(t *testing.T) {
	cases := []struct {
		name         string
		competitor   []tmux.Command
		wantName     string
		wantExisting bool
	}{
		{
			name:       "plain session takes the name",
			competitor: []tmux.Command{{"new-session", "-d", "-s", "api", "sleep 3600"}},
			wantName:   "api-2",
		},
		{
			name: "same project opened concurrently",
			competitor: []tmux.Command{
				{"new-session", "-d", "-s", "api", "sleep 3600"},
				{"set-option", "-t", "=api:", tmux.OptManaged, "1"},
				{"set-option", "-t", "=api:", tmux.OptProject, "PROJECT"},
			},
			wantName: "api", wantExisting: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCreateEnv(t)
			s := openServer(t, e.testHost)
			ctx := tmuxtest.Context(t)
			startWorkspace(t, s, "holder")
			direct := tmux.New(tmux.Options{Bin: e.TmuxBin, Socket: tmux.Socket{Name: s.SocketName}, Env: ServerEnviron(e.Environ)})
			exe := &racingExecutor{env: ServerEnviron(e.Environ), competitor: func(ctx context.Context) {
				cmds := make([]tmux.Command, len(tc.competitor))
				for i, c := range tc.competitor {
					cmds[i] = slices.Clone(c)
					for j, a := range cmds[i] {
						if a == "PROJECT" {
							cmds[i][j] = e.project
						}
					}
				}
				if _, err := direct.Batch(ctx, cmds...); err != nil {
					t.Errorf("competitor: %v", err)
				}
			}}
			bin := e.TmuxBin
			if bin == "" {
				bin = "tmux"
			}
			s.Client = tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: s.SocketName}, Config: s.Paths.TmuxConf(), Executor: exe})
			res, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo})
			if err != nil || res.Name != tc.wantName || res.Existing != tc.wantExisting || !exe.raced {
				t.Fatalf("result %+v, %v (raced %v)", res, err, exe.raced)
			}
		})
	}
}

// TestTrustWarning pins what a fresh workspace says about an untrusted
// project: Claude Code opens on its own trust question there and runs no
// hooks until it is answered, and the warning has to say so without guessing
// when the state file cannot be read.
func TestTrustWarning(t *testing.T) {
	project := "/projects/api"
	cases := []struct {
		name   string
		state  string
		noFile bool
		noHome bool
		want   bool
	}{
		{name: "an accepted project says nothing", state: `{"projects":{"/projects/api":{"hasTrustDialogAccepted":true}}}`},
		{name: "a refused project is reported", state: `{"projects":{"/projects/api":{"hasTrustDialogAccepted":false}}}`, want: true},
		{name: "a project Claude Code never opened is reported", state: `{"projects":{"/projects/web":{"hasTrustDialogAccepted":true}}}`, want: true},
		{name: "a parent that is trusted does not trust the project", state: `{"projects":{"/projects":{"hasTrustDialogAccepted":true}}}`, want: true},
		{name: "no state file says nothing", noFile: true},
		{name: "a state file that is not JSON says nothing", state: "{broken"},
		{name: "no home directory says nothing", state: `{"projects":{}}`, noHome: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if !tc.noFile {
				if err := os.WriteFile(filepath.Join(home, claudecfg.ConfigFileName), []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h := Host{Home: home, Getenv: func(string) string { return "" }}
			if tc.noHome {
				h.Home = ""
			}
			got := trustWarning(h, project)
			if (got != "") != tc.want {
				t.Fatalf("trustWarning = %q, want a warning: %v", got, tc.want)
			}
			if tc.want && !strings.Contains(got, project) {
				t.Fatalf("the warning does not name the project: %q", got)
			}
		})
	}
}

// TestCreateReportsAnUntrustedProject proves the warning reaches the result of
// a real create, which is what the command prints under the workspace.
func TestCreateReportsAnUntrustedProject(t *testing.T) {
	e := newCreateEnv(t)
	claudeHome := e.env["CLAUDE_CONFIG_DIR"]
	mkdir(t, claudeHome)
	state := `{"projects":{"` + e.project + `":{"hasTrustDialogAccepted":false}}}`
	if err := os.WriteFile(filepath.Join(claudeHome, claudecfg.ConfigFileName), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openServer(t, e.testHost)
	res, err := s.Create(tmuxtest.Context(t), e.Host, CreateRequest{Dir: e.project})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "not been trusted") {
		t.Fatalf("warnings %q", res.Warnings)
	}
}
