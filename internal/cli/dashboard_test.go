package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

func TestRootWithoutTerminalListsWorkspaces(t *testing.T) {
	e := newCLIEnv(t)
	e.term.Interactive = false
	cases := []struct {
		name   string
		setup  func(t *testing.T)
		outHas []string
	}{
		{name: "no workspaces", outHas: []string{"No workspaces. Start one with: lmux create"}},
		{name: "workspaces", setup: func(t *testing.T) { t.Helper(); e.start(t, "api", "/src/api") }, outHas: []string{"NAME", "api", "duo", "/src/api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			code, stdout, stderr := e.run(t)
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			_, lsOut, _ := e.run(t, "ls")
			if stdout != lsOut {
				t.Fatalf("root output differs from ls:\n%s\nls:\n%s", stdout, lsOut)
			}
			if len(e.execs) != 0 {
				t.Fatalf("attached without a terminal: %q", e.execs)
			}
		})
	}
}

func TestRootOnTerminalReportsServerErrors(t *testing.T) {
	e := newCLIEnv(t)
	e.host.TmuxBin = filepath.Join(e.host.Home, "missing-tmux")
	code, stdout, stderr := agentsCLI(t, e, strings.NewReader("q"))
	if code != 1 || !containsFolded(stderr, "not installed") || stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestRootOnTerminalRunsDashboard runs lyna-tmux without a command on a
// terminal against the real server and a fake claude, and follows the keys.
func TestRootOnTerminalRunsDashboard(t *testing.T) {
	cases := []struct {
		name string
		keys func(t *testing.T, term *agentsTerm)
	}{
		{
			name: "quit after the sessions are drawn",
			keys: func(t *testing.T, term *agentsTerm) {
				t.Helper()
				rootWaitScreen(t, term, "root-api")
				rootWaitScreen(t, term, "root-agent")
				term.send(t, "q")
			},
		},
		{
			name: "agents picker lists the claude agents",
			keys: func(t *testing.T, term *agentsTerm) {
				t.Helper()
				rootWaitScreen(t, term, "root-api")
				term.send(t, "a")
				rootWaitScreen(t, term, "ctrl+x")
				term.send(t, "q")
			},
		},
		{
			name: "new workspace form opens",
			keys: func(t *testing.T, term *agentsTerm) {
				t.Helper()
				rootWaitScreen(t, term, "root-api")
				term.send(t, "n")
				rootWaitScreen(t, term, "Directory")
				term.send(t, "\x03")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			e.withFakeClaude(t)
			agentsUnsetenv(e, "TMUX")
			e.start(t, "root-api", e.host.Home)
			pid, err := agentsServer(e).Display(tmuxtest.Context(t), "=root-api:", "#{pane_pid}")
			if err != nil {
				t.Fatal(err)
			}
			n, _ := strconv.Atoi(pid)
			agentsRecords(t, e, agent.Record{PID: n, CWD: e.host.Home, Kind: agent.KindInteractive, StartedAt: time.Now().UnixMilli(), Name: "root-agent", Status: "idle"})
			term := newAgentsTerm(t)
			wait := term.runCLI(t, e)
			tc.keys(t, term)
			if code := wait(); code != 0 {
				t.Fatalf("exit %d: %s", code, term.errOut.String())
			}
			if len(e.execs) != 0 {
				t.Fatalf("process replaced: %q", e.execs)
			}
		})
	}
}

// rootWaitScreen waits until the program output shows text.
func rootWaitScreen(t *testing.T, term *agentsTerm, text string) {
	t.Helper()
	tmuxtest.WaitFor(t, "screen to show "+text, func() bool { return strings.Contains(ansi.Strip(term.out.String()), text) })
}

// dashFakeSessions lists fixed sessions.
type dashFakeSessions struct{ sessions []tmux.Session }

func (f dashFakeSessions) Sessions(context.Context) ([]tmux.Session, error) { return f.sessions, nil }

// dashFakeActions records the dashboard's effects.
type dashFakeActions struct {
	mu       sync.Mutex
	attached []string
	ran      atomic.Bool
}

func (f *dashFakeActions) Attach(_ context.Context, s tmux.Session) (tea.ExecCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, s.Name)
	return rootTestExec{ran: &f.ran}, nil
}

func (f *dashFakeActions) Kill(context.Context, tmux.Session) error { return nil }

func (f *dashFakeActions) Create(context.Context, tui.CreateRequest) (tea.ExecCommand, error) {
	return nil, errors.New("not used")
}

// rootTestExec is a terminal handover that only records it ran.
type rootTestExec struct{ ran *atomic.Bool }

func (e rootTestExec) Run() error {
	e.ran.Store(true)
	return nil
}

func (rootTestExec) SetStdin(io.Reader)  {}
func (rootTestExec) SetStdout(io.Writer) {}
func (rootTestExec) SetStderr(io.Writer) {}

// TestDashRunProgram runs the dashboard as a real program on typed keys and
// follows it into the screens it opens.
func TestDashRunProgram(t *testing.T) {
	cases := []struct {
		name  string
		keys  func(t *testing.T, term *agentsTerm, agents *agentsFake)
		check func(t *testing.T, ui rootUI, agents *agentsFake, actions *dashFakeActions, out string)
	}{
		{
			name: "quit draws sessions and agents",
			keys: func(t *testing.T, term *agentsTerm, _ *agentsFake) {
				t.Helper()
				tmuxtest.WaitFor(t, "sessions drawn", func() bool { return strings.Contains(term.out.String(), "dash-api") })
				term.send(t, "q")
			},
			check: func(t *testing.T, _ rootUI, agents *agentsFake, actions *dashFakeActions, out string) {
				t.Helper()
				if cached, _, _, _ := agents.calls(); cached != 1 || actions.ran.Load() || !strings.Contains(out, "fake-agent") {
					t.Fatalf("cached %d, attached %v, output:\n%s", cached, actions.ran.Load(), out)
				}
			},
		},
		{
			name: "enter attaches the selected session",
			keys: func(t *testing.T, term *agentsTerm, _ *agentsFake) {
				t.Helper()
				tmuxtest.WaitFor(t, "sessions drawn", func() bool { return strings.Contains(term.out.String(), "dash-api") })
				term.send(t, "\r")
			},
			check: func(t *testing.T, _ rootUI, _ *agentsFake, actions *dashFakeActions, _ string) {
				t.Helper()
				actions.mu.Lock()
				defer actions.mu.Unlock()
				if !slices.Equal(actions.attached, []string{"dash-api"}) || !actions.ran.Load() {
					t.Fatalf("attached %q, handover ran %v", actions.attached, actions.ran.Load())
				}
			},
		},
		{
			name: "a opens the agents picker",
			keys: func(t *testing.T, term *agentsTerm, agents *agentsFake) {
				t.Helper()
				term.send(t, "a")
				tmuxtest.WaitFor(t, "picker started", func() bool { cached, _, _, _ := agents.calls(); return cached == 2 })
				term.send(t, "\r")
			},
			check: func(t *testing.T, _ rootUI, agents *agentsFake, _ *dashFakeActions, _ string) {
				t.Helper()
				if _, jumped, _, _ := agents.calls(); !slices.Equal(jumped, []string{"pid:4242"}) {
					t.Fatalf("picker jumped %q", jumped)
				}
			},
		},
		{
			name: "s opens the setup wizard",
			keys: func(t *testing.T, term *agentsTerm, _ *agentsFake) {
				t.Helper()
				term.send(t, "s")
				tmuxtest.WaitFor(t, "wizard drawn", func() bool { return strings.Contains(term.out.String(), "Color depth") })
				term.send(t, "\x03")
			},
			check: func(t *testing.T, ui rootUI, _ *agentsFake, _ *dashFakeActions, out string) {
				t.Helper()
				if !strings.Contains(out, "Setup canceled; nothing was written") {
					t.Fatalf("output:\n%s", out)
				}
				if _, err := os.Stat(filepath.Join(ui.host.Getenv("LYNA_TMUX_HOME"), "config", "config.toml")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("canceled setup wrote a file: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := newAgentsTerm(t)
			ui := agentsUI(t)
			agents := &agentsFake{snap: agent.Snapshot{TakenAt: time.Now(), Agents: []agent.Agent{agentsSample(agent.SourceManaged)}}}
			actions := &dashFakeActions{}
			b := dashBackend{
				sessions: dashFakeSessions{sessions: []tmux.Session{{ID: "$1", Name: "dash-api", Windows: 1, Managed: true, Layout: "duo", Created: time.Now()}}},
				agents:   agents,
				actions:  actions,
				layouts:  []string{"solo", "duo"},
				defaults: tui.CreateRequest{Dir: "/src", Layout: "duo", Sandbox: "standard"},
			}
			d := Deps{LookPath: func(name string) (string, error) { return name, nil }, Now: time.Now}
			wait := term.start(t, func(cmd *cobra.Command) error { return d.dashRun(cmd, ui, b) })
			tc.keys(t, term, agents)
			if err := wait(); err != nil {
				t.Fatalf("dashboard: %v\n%s", err, term.errOut.String())
			}
			tc.check(t, ui, agents, actions, term.out.String())
		})
	}
}

func TestDashActions(t *testing.T) {
	e := newCLIEnv(t)
	e.withFakeClaude(t)
	agentsUnsetenv(e, "TMUX")
	project := filepath.Join(e.host.Home, "src", "web")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := tmuxtest.Context(t)
	s, err := app.OpenServer(ctx, e.host)
	if err != nil {
		t.Fatal(err)
	}
	e.start(t, "api", "/src/api")
	d := Deps{
		Host:     func() (app.Host, error) { return e.host, nil },
		LookPath: func(name string) (string, error) { return name, nil },
		Getwd:    func() (string, error) { return filepath.Join(e.host.Home, "src"), nil },
	}
	x := dashActions{d: d, h: e.host, s: s, term: Terminal{Interactive: true, Width: 120, Height: 40}}
	attachArgs := func(t *testing.T, exe tea.ExecCommand, name string) {
		t.Helper()
		exe, _ = dashUnwrap(exe)
		c, ok := exe.(rootExecCmd)
		if !ok || c.Path != e.host.TmuxBin || !slices.Contains(c.Args, "attach-session") || c.Args[len(c.Args)-1] != "="+name+":" {
			t.Fatalf("handover %#v", exe)
		}
		for _, kv := range c.Env {
			if strings.HasPrefix(kv, "TMUX=") {
				t.Fatalf("attach environment keeps %q", kv)
			}
		}
	}
	cases := []struct {
		name    string
		run     func(t *testing.T) (tea.ExecCommand, error)
		wantErr string
		check   func(t *testing.T, exe tea.ExecCommand)
	}{
		{
			name: "attach loads the current configuration first",
			run: func(t *testing.T) (tea.ExecCommand, error) {
				t.Helper()
				if got, _ := s.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash); got == s.Fingerprint {
					t.Fatalf("configuration already recorded as loaded: %q", got)
				}
				return x.Attach(ctx, tmux.Session{Name: "api"})
			},
			check: func(t *testing.T, exe tea.ExecCommand) {
				t.Helper()
				attachArgs(t, exe, "api")
				if got, _ := s.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash); got != s.Fingerprint {
					t.Fatalf("loaded configuration %q, want %q", got, s.Fingerprint)
				}
			},
		},
		{
			name:    "attach a missing session",
			run:     func(*testing.T) (tea.ExecCommand, error) { return x.Attach(ctx, tmux.Session{Name: "nope"}) },
			wantErr: "no such workspace: nope",
		},
		{
			name: "create from a relative directory and attach",
			run: func(*testing.T) (tea.ExecCommand, error) {
				return x.Create(ctx, tui.CreateRequest{Dir: "web", Layout: "solo", Sandbox: "strict"})
			},
			check: func(t *testing.T, exe tea.ExecCommand) {
				t.Helper()
				attachArgs(t, exe, "web")
				sessions, err := s.Sessions(ctx)
				if err != nil {
					t.Fatal(err)
				}
				i := slices.IndexFunc(sessions, func(x tmux.Session) bool { return x.Name == "web" })
				if i < 0 || sessions[i].Project != project || sessions[i].Layout != "solo" || sessions[i].Sandbox != "strict" {
					t.Fatalf("sessions %+v", sessions)
				}
			},
		},
		{
			name: "create from a home directory path reuses the workspace and says so",
			run: func(*testing.T) (tea.ExecCommand, error) {
				return x.Create(ctx, tui.CreateRequest{Dir: "~/src/web", Layout: "duo", Sandbox: "standard"})
			},
			check: func(t *testing.T, exe tea.ExecCommand) {
				t.Helper()
				attachArgs(t, exe, "web")
				_, lines := dashUnwrap(exe)
				if !slices.Contains(lines, "Workspace web is already open for "+project) {
					t.Fatalf("notice %q", lines)
				}
			},
		},
		{
			name:    "create in a missing directory",
			run:     func(*testing.T) (tea.ExecCommand, error) { return x.Create(ctx, tui.CreateRequest{Dir: "nowhere"}) },
			wantErr: "no such directory",
		},
		{
			name: "container isolation opens the workspace in the container",
			run: func(t *testing.T) (tea.ExecCommand, error) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(e.host.Home, "src", "tool", ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				docker := filepath.Join(e.host.Home, "bin", "docker")
				if err := os.MkdirAll(filepath.Dir(docker), 0o700); err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/sh\ncase \"$1 $2\" in 'container ls') echo running ;; esac\nexit 0\n"
				if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				dc := x
				dc.cmd = &cobra.Command{}
				dc.cmd.SetContext(ctx)
				dc.d.LookPath = func(name string) (string, error) {
					if name == "docker" {
						return docker, nil
					}
					return name, nil
				}
				saved := s.Config.Sandbox.Isolation
				s.Config.Sandbox.Isolation = "container"
				t.Cleanup(func() { s.Config.Sandbox.Isolation = saved })
				return dc.Create(ctx, tui.CreateRequest{Dir: "tool", Layout: "solo", Sandbox: "standard"})
			},
			check: func(t *testing.T, exe tea.ExecCommand) {
				t.Helper()
				c, ok := exe.(rootExecCmd)
				if !ok || filepath.Base(c.Path) != "docker" {
					t.Fatalf("handover %#v", exe)
				}
				// The container command carries the form's request; the
				// flags it adds beyond them belong to the create command.
				i := slices.Index(c.Args, "lyna-tmux-tool")
				head := []string{"lyna-tmux-tool", "lmux", "create"}
				if i < 0 || len(c.Args)-i < len(head)+1 || !slices.Equal(c.Args[i:i+len(head)], head) || c.Args[len(c.Args)-1] != "/workspace" {
					t.Fatalf("argv %q, want it to run %q in the container for /workspace", c.Args, head)
				}
				for _, flag := range []string{"--layout=solo", "--sandbox=standard", "--isolation=container"} {
					if !slices.Contains(c.Args[i:], flag) {
						t.Fatalf("argv %q lacks %s", c.Args, flag)
					}
				}
				if !slices.Contains(c.Args, "--tty") {
					t.Fatalf("argv %q keeps no terminal", c.Args)
				}
				sessions, err := s.Sessions(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if i := slices.IndexFunc(sessions, func(x tmux.Session) bool { return x.Name == "tool" }); i >= 0 {
					t.Fatalf("workspace created on this host too: %+v", sessions[i])
				}
			},
		},
		{
			name: "kill",
			run:  func(*testing.T) (tea.ExecCommand, error) { return nil, x.Kill(ctx, tmux.Session{Name: "api"}) },
			check: func(t *testing.T, _ tea.ExecCommand) {
				t.Helper()
				if ok, err := s.Client.HasSession(ctx, "api"); ok || err != nil {
					t.Fatalf("api still running: %v %v", ok, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, err := tc.run(t)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, exe)
		})
	}
}

// dashUnwrap returns the handover under a create notice and the lines it
// shows first; a plain handover has none.
func dashUnwrap(exe tea.ExecCommand) (tea.ExecCommand, []string) {
	if n, ok := exe.(*dashNotice); ok {
		return n.ExecCommand, n.lines
	}
	return exe, nil
}

// dashCountingReader records that the notice waited for a key.
type dashCountingReader struct {
	rest  string
	reads int
}

func (r *dashCountingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.rest == "" {
		return 0, io.EOF
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

// dashRecordingExec is a handover that records the streams it was given.
type dashRecordingExec struct {
	ran bool
	in  io.Reader
	out io.Writer
}

func (e *dashRecordingExec) Run() error            { e.ran = true; return nil }
func (e *dashRecordingExec) SetStdin(r io.Reader)  { e.in = r }
func (e *dashRecordingExec) SetStdout(w io.Writer) { e.out = w }
func (e *dashRecordingExec) SetStderr(io.Writer)   {}

// TestDashCreateNotice pins what the dashboard says about a create before it
// hands the terminal to tmux, which clears whatever is on the screen.
func TestDashCreateNotice(t *testing.T) {
	cases := []struct {
		name      string
		res       app.CreateResult
		wantLines []string
	}{
		{name: "a new workspace says nothing", res: app.CreateResult{Name: "api", Project: "/src/api"}},
		{
			name: "an existing workspace", res: app.CreateResult{Name: "api", Project: "/src/api", Existing: true},
			wantLines: []string{"Workspace api is already open for /src/api"},
		},
		{
			name: "warnings", res: app.CreateResult{Name: "api", Warnings: []string{"remove old Claude settings files: denied"}},
			wantLines: []string{"Warning: remove old Claude settings files: denied"},
		},
		{
			name: "both, with terminal controls removed",
			res: app.CreateResult{
				Name: "api", Project: "/src/api\x1b]0;evil\x07", Existing: true,
				Warnings: []string{"record the loaded configuration: \x1b[31mdenied"},
			},
			wantLines: []string{"Workspace api is already open for /src/api", "Warning: record the loaded configuration: denied"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := dashCreateNotice(tc.res)
			if !slices.Equal(lines, tc.wantLines) {
				t.Fatalf("notice %q, want %q", lines, tc.wantLines)
			}
			inner := &dashRecordingExec{}
			exe := dashHold(inner, lines)
			if len(tc.wantLines) == 0 {
				if exe != tea.ExecCommand(inner) {
					t.Fatalf("handover wrapped for nothing: %#v", exe)
				}
				return
			}
			var out strings.Builder
			in := &dashCountingReader{rest: " "}
			exe.SetStdin(in)
			exe.SetStdout(&out)
			exe.SetStderr(io.Discard)
			if err := exe.Run(); err != nil {
				t.Fatal(err)
			}
			for _, l := range tc.wantLines {
				if !strings.Contains(out.String(), l) {
					t.Fatalf("output %q missing %q", out.String(), l)
				}
			}
			if !strings.Contains(out.String(), "Press any key to continue.") || in.reads == 0 {
				t.Fatalf("output %q, reads %d: the notice did not wait", out.String(), in.reads)
			}
			if !inner.ran || inner.in != io.Reader(in) || inner.out != io.Writer(&out) {
				t.Fatalf("handover ran %v with in %#v out %#v", inner.ran, inner.in, inner.out)
			}
		})
	}
}

func TestDashDefaults(t *testing.T) {
	cases := []struct {
		name    string
		layout  string
		profile string
		want    tui.CreateRequest
		wantErr error
		errHas  string
	}{
		{name: "configured values", layout: "trio", profile: "strict", want: tui.CreateRequest{Dir: "/w", Layout: "trio", Sandbox: "strict"}},
		{name: "standard", layout: "duo", profile: "standard", want: tui.CreateRequest{Dir: "/w", Layout: "duo", Sandbox: "standard"}},
		{
			name: "sandbox off in the file is refused, not replaced", layout: "auto", profile: "off",
			wantErr: app.ErrSandboxOffInConfig, errHas: "--sandbox off",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Workspace.Layout, cfg.Sandbox.Profile = tc.layout, tc.profile
			got, err := dashDefaults(cfg, "/w")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("error %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				if got != (tui.CreateRequest{}) {
					t.Fatalf("dashDefaults = %+v, want the zero request", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("dashDefaults = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestRootRefusesSandboxOffInConfig checks the dashboard refuses to open on a
// configuration that turns the sandbox off, as every other launch path does.
func TestRootRefusesSandboxOffInConfig(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		errHas   string
		outHas   string
	}{
		{name: "the dashboard", wantCode: 1, errHas: "is not honored"},
		{name: "listing is unaffected", args: []string{"ls"}, outHas: "No workspaces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			dir := filepath.Join(e.host.Home, "config")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[sandbox]\nprofile = \"off\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The typed q closes a dashboard that opened where it should have
			// been refused, so the case fails instead of hanging on it.
			code, stdout, stderr := agentsCLI(t, e, strings.NewReader("q"), tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			if tc.errHas != "" && !containsFolded(stderr, tc.errHas) {
				t.Fatalf("stderr %q, want %q", stderr, tc.errHas)
			}
			if tc.outHas != "" && !strings.Contains(stdout, tc.outHas) {
				t.Fatalf("stdout %q, want %q", stdout, tc.outHas)
			}
		})
	}
}

func TestDashDir(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{"src/api", "work"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(home, "work")
	getwd := func() (string, error) { return cwd, nil }
	cases := []struct {
		name    string
		dir     string
		getwd   func() (string, error)
		want    string
		wantErr string
	}{
		{name: "absolute", dir: filepath.Join(home, "src", "api"), want: filepath.Join(home, "src", "api")},
		{name: "home", dir: "~", want: home},
		{name: "under home with spaces around", dir: "  ~/src/api/ ", want: filepath.Join(home, "src", "api")},
		{name: "relative to the working directory", dir: "../src/api", want: filepath.Join(home, "src", "api")},
		{name: "empty", dir: "   ", wantErr: "a directory is required"},
		{name: "missing", dir: "~/nope", wantErr: "no such directory"},
		{name: "a file", dir: file, wantErr: "is not a directory"},
		{name: "unknown working directory", dir: "rel", getwd: func() (string, error) { return "", errors.New("cwd gone") }, wantErr: "cwd gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := getwd
			if tc.getwd != nil {
				g = tc.getwd
			}
			got, err := dashDir(tc.dir, home, g)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("dashDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
