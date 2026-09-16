package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// agentsProgramTimeout bounds one full-screen program run in a test, so a
// program that never quits fails the test instead of hanging it.
const agentsProgramTimeout = 60 * time.Second

// agentsBuffer is an output buffer the test reads while a program writes it.
type agentsBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *agentsBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *agentsBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// agentsTerm is a command bound to a pipe the test types into and to
// captured output. A pipe, unlike a plain reader, lets a finished program
// stop reading, so input typed for the next program is not lost.
type agentsTerm struct {
	cmd    *cobra.Command
	keys   *os.File
	out    *agentsBuffer
	errOut *agentsBuffer
}

func newAgentsTerm(t *testing.T) *agentsTerm {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), agentsProgramTimeout)
	t.Cleanup(cancel)
	term := &agentsTerm{cmd: &cobra.Command{}, keys: w, out: &agentsBuffer{}, errOut: &agentsBuffer{}}
	term.cmd.SetContext(ctx)
	term.cmd.SetIn(r)
	term.cmd.SetOut(term.out)
	term.cmd.SetErr(term.errOut)
	return term
}

func (a *agentsTerm) send(t *testing.T, keys string) {
	t.Helper()
	if _, err := a.keys.WriteString(keys); err != nil {
		t.Fatal(err)
	}
}

// start runs fn in the background and returns a function that waits for its
// result.
func (a *agentsTerm) start(t *testing.T, fn func(cmd *cobra.Command) error) func() error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn(a.cmd) }()
	return func() error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(agentsProgramTimeout + 5*time.Second):
			t.Fatalf("program did not finish; output:\n%s", a.out.String())
			return nil
		}
	}
}

// agentsUI is a terminal description for programs that need no tmux.
func agentsUI(t *testing.T) rootUI {
	t.Helper()
	root := t.TempDir()
	env := map[string]string{"LYNA_TMUX_HOME": root, "TERM": "xterm-256color", "LANG": "en_US.UTF-8"}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	return rootUI{
		host:   app.Host{Getenv: func(k string) string { return env[k] }, Environ: environ, Home: root},
		config: config.Default(),
		term:   Terminal{Interactive: true, Width: 100, Height: 30},
	}
}

// agentsFake is a backend with a fixed snapshot that records every effect.
type agentsFake struct {
	mu        sync.Mutex
	snap      agent.Snapshot
	cached    int
	refreshes int
	jump      *app.Attach
	job       app.Attach
	err       error
	jumped    []string
	attached  []string
	killed    []string
}

func (f *agentsFake) Cached() (agent.Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cached++
	return f.snap, true
}

func (f *agentsFake) Refresh(context.Context) (agent.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	return f.snap, nil
}

func (f *agentsFake) Preview(context.Context, agent.Agent, int) (string, error) {
	return "fake pane text", nil
}

func (f *agentsFake) Jump(_ context.Context, a agent.Agent) (*app.Attach, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jumped = append(f.jumped, a.Record.Key())
	return f.jump, f.err
}

func (f *agentsFake) AttachJob(a agent.Agent) (app.Attach, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, a.Record.Key())
	return f.job, f.err
}

func (f *agentsFake) Kill(a agent.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, a.Record.Key())
	return f.err
}

func (f *agentsFake) calls() (cached int, jumped, attached, killed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cached, slices.Clone(f.jumped), slices.Clone(f.attached), slices.Clone(f.killed)
}

func agentsSample(source agent.Source) agent.Agent {
	a := agent.Agent{
		Record: agent.Record{PID: 4242, CWD: "/src/api", Kind: agent.KindInteractive, StartedAt: time.Now().UnixMilli(), Name: "fake-agent", Status: "waiting"},
		Source: source, Status: agent.StatusWaiting,
	}
	switch source {
	case agent.SourceBackground:
		a.Record = agent.Record{ID: "job-7", CWD: "/src/api", Kind: agent.KindBackground, StartedAt: time.Now().UnixMilli(), Name: "fake-job", State: agent.JobBlocked}
	case agent.SourceManaged, agent.SourcePane:
		a.Location = &agent.Location{Session: "api", WindowIndex: 1, WindowName: "claude", PaneID: "%7"}
	case agent.SourceExternal:
	}
	return a
}

func TestAgentsActionsAdapter(t *testing.T) {
	d := Deps{LookPath: func(name string) (string, error) {
		if name == "missing" {
			return "", os.ErrNotExist
		}
		return "/resolved/" + filepath.Base(name), nil
	}}
	failure := errors.New("backend failed")
	cases := []struct {
		name     string
		fake     *agentsFake
		action   func(x agentsActions, a agent.Agent) (tea.ExecCommand, error)
		wantArgs []string
		wantEnv  []string
		wantErr  error
	}{
		{
			name: "jump inside tmux hands nothing over",
			fake: &agentsFake{},
			action: func(x agentsActions, a agent.Agent) (tea.ExecCommand, error) {
				return x.Jump(context.Background(), a)
			},
		},
		{
			name: "jump outside tmux attaches",
			fake: &agentsFake{jump: &app.Attach{Argv: []string{"tmux", "-L", "lyna-tmux", "attach-session", "-t", "=api:"}, Env: []string{"TERM=xterm"}}},
			action: func(x agentsActions, a agent.Agent) (tea.ExecCommand, error) {
				return x.Jump(context.Background(), a)
			},
			wantArgs: []string{"/resolved/tmux", "-L", "lyna-tmux", "attach-session", "-t", "=api:"},
			wantEnv:  []string{"TERM=xterm"},
		},
		{
			name: "jump failure",
			fake: &agentsFake{err: failure},
			action: func(x agentsActions, a agent.Agent) (tea.ExecCommand, error) {
				return x.Jump(context.Background(), a)
			},
			wantErr: failure,
		},
		{
			name: "attach job runs claude attach",
			fake: &agentsFake{job: app.Attach{Argv: []string{"/bin/claude", "attach", "job-7"}, Env: []string{"A=1"}}},
			action: func(x agentsActions, a agent.Agent) (tea.ExecCommand, error) {
				return x.Attach(context.Background(), a)
			},
			wantArgs: []string{"/resolved/claude", "attach", "job-7"},
			wantEnv:  []string{"A=1"},
		},
		{
			name: "attach program not found",
			fake: &agentsFake{job: app.Attach{Argv: []string{"missing"}}},
			action: func(x agentsActions, a agent.Agent) (tea.ExecCommand, error) {
				return x.Attach(context.Background(), a)
			},
			wantErr: os.ErrNotExist,
		},
		{
			name: "kill reaches the backend",
			fake: &agentsFake{err: failure},
			action: func(x agentsActions, a agent.Agent) (tea.ExecCommand, error) {
				return nil, x.Kill(context.Background(), a)
			},
			wantErr: failure,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := agentsSample(agent.SourcePane)
			exe, err := tc.action(agentsActions{d: d, b: tc.fake}, a)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v, want %v", err, tc.wantErr)
			}
			if tc.wantArgs == nil {
				if exe != nil {
					t.Fatalf("handover %#v, want none", exe)
				}
				return
			}
			c, ok := exe.(rootExecCmd)
			if !ok || c.Path != tc.wantArgs[0] || !slices.Equal(c.Args[1:], tc.wantArgs[1:]) || !slices.Equal(c.Env, tc.wantEnv) {
				t.Fatalf("handover %#v", exe)
			}
			var in bytes.Buffer
			var out bytes.Buffer
			exe.SetStdin(&in)
			exe.SetStdout(&out)
			exe.SetStderr(&out)
			if c.Stdin != &in || c.Stdout != &out || c.Stderr != &out {
				t.Fatal("handover streams not set")
			}
		})
	}
	if _, err := (Deps{}).rootExec(app.Attach{}); err == nil {
		t.Fatal("empty attach command accepted")
	}
}

// TestAgentsPickProgram runs the picker as a real program on scripted keys
// and checks the backend is what it draws and acts on.
func TestAgentsPickProgram(t *testing.T) {
	cases := []struct {
		name  string
		agent agent.Agent
		popup bool
		keys  func(t *testing.T, term *agentsTerm, f *agentsFake)
		check func(t *testing.T, f *agentsFake, out string)
	}{
		{
			name:  "quit draws the cached agents",
			agent: agentsSample(agent.SourceManaged),
			keys:  func(t *testing.T, term *agentsTerm, _ *agentsFake) { t.Helper(); term.send(t, "q") },
			check: func(t *testing.T, f *agentsFake, out string) {
				t.Helper()
				if cached, jumped, _, _ := f.calls(); cached != 1 || len(jumped) != 0 || !strings.Contains(out, "fake-agent") {
					t.Fatalf("cached %d, jumped %q, output:\n%s", cached, jumped, out)
				}
			},
		},
		{
			name:  "enter jumps and quits",
			agent: agentsSample(agent.SourceManaged),
			keys:  func(t *testing.T, term *agentsTerm, _ *agentsFake) { t.Helper(); term.send(t, "\r") },
			check: func(t *testing.T, f *agentsFake, _ string) {
				t.Helper()
				if _, jumped, _, _ := f.calls(); !slices.Equal(jumped, []string{"pid:4242"}) {
					t.Fatalf("jumped %q", jumped)
				}
			},
		},
		{
			name:  "enter on a background job runs the attach command",
			agent: agentsSample(agent.SourceBackground),
			keys:  func(t *testing.T, term *agentsTerm, _ *agentsFake) { t.Helper(); term.send(t, "\r") },
			check: func(t *testing.T, f *agentsFake, _ string) {
				t.Helper()
				if _, _, attached, _ := f.calls(); !slices.Equal(attached, []string{"job:job-7"}) {
					t.Fatalf("attached %q", attached)
				}
			},
		},
		{
			name:  "a popup picker names the quit key close",
			agent: agentsSample(agent.SourceManaged),
			popup: true,
			keys:  func(t *testing.T, term *agentsTerm, _ *agentsFake) { t.Helper(); term.send(t, "q") },
			check: func(t *testing.T, _ *agentsFake, out string) {
				t.Helper()
				if frame := ansi.Strip(out); !strings.Contains(frame, "q close") || strings.Contains(frame, "q quit") {
					t.Fatalf("popup footer:\n%s", frame)
				}
			},
		},
		{
			name:  "kill after confirmation",
			agent: agentsSample(agent.SourceExternal),
			keys: func(t *testing.T, term *agentsTerm, f *agentsFake) {
				t.Helper()
				term.send(t, "\x18y")
				tmuxtest.WaitFor(t, "kill", func() bool { _, _, _, killed := f.calls(); return len(killed) > 0 })
				term.send(t, "q")
			},
			check: func(t *testing.T, f *agentsFake, _ string) {
				t.Helper()
				if _, _, _, killed := f.calls(); !slices.Equal(killed, []string{"pid:4242"}) {
					t.Fatalf("killed %q", killed)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := newAgentsTerm(t)
			marker := filepath.Join(t.TempDir(), "attached")
			f := &agentsFake{
				snap: agent.Snapshot{TakenAt: time.Now(), Agents: []agent.Agent{tc.agent}},
				job:  app.Attach{Argv: []string{"/usr/bin/touch", marker}},
			}
			d := Deps{LookPath: func(name string) (string, error) { return name, nil }, Now: time.Now}
			wait := term.start(t, func(cmd *cobra.Command) error { return d.agentsPick(cmd, agentsUI(t), f, tc.popup) })
			tc.keys(t, term, f)
			if err := wait(); err != nil {
				t.Fatalf("picker: %v\n%s", err, term.errOut.String())
			}
			tc.check(t, f, term.out.String())
			if tc.agent.Source == agent.SourceBackground {
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("attach command did not run: %v", err)
				}
			}
		})
	}
}

// agentsCLI runs the command line like cliEnv.run, with typed input for
// full-screen commands and a deadline.
func agentsCLI(t *testing.T, e *cliEnv, stdin io.Reader, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut agentsBuffer
	code = agentsRunRoot(t, e, Streams{In: stdin, Out: &out, Err: &errOut}, args)
	return code, out.String(), errOut.String()
}

// runCLI starts the command line on the terminal's pipe and buffers and
// returns a function that waits for its exit code.
func (a *agentsTerm) runCLI(t *testing.T, e *cliEnv, args ...string) func() int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- agentsRunRoot(t, e, Streams{In: a.cmd.InOrStdin(), Out: a.out, Err: a.errOut}, args) }()
	return func() int {
		t.Helper()
		select {
		case code := <-done:
			return code
		case <-time.After(agentsProgramTimeout + 5*time.Second):
			t.Fatalf("command did not finish; output:\n%s", a.out.String())
			return -1
		}
	}
}

func agentsRunRoot(t *testing.T, e *cliEnv, s Streams, args []string) int {
	d := Deps{
		Host: func() (app.Host, error) { return e.host, nil },
		Exec: func(path string, argv, env []string) error {
			e.execs = append(e.execs, append([]string{path}, argv...))
			e.envs = append(e.envs, env)
			return nil
		},
		LookPath: func(name string) (string, error) { return name, nil },
		Getwd:    func() (string, error) { return e.cwd, nil },
		Terminal: func() Terminal { return e.term },
		Now:      time.Now,
	}
	ctx, cancel := context.WithTimeout(t.Context(), agentsProgramTimeout)
	defer cancel()
	return run(ctx, NewRootWith(s, d), args)
}

// agentsUnsetenv removes a variable from the test host.
func agentsUnsetenv(e *cliEnv, key string) {
	delete(e.env, key)
	e.host.Environ = e.host.Environ[:0:0]
	for k, v := range e.env {
		e.host.Environ = append(e.host.Environ, k+"="+v)
	}
}

// agentsRecords makes the fake claude list records.
func agentsRecords(t *testing.T, e *cliEnv, records ...agent.Record) {
	t.Helper()
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.host.Home, "agents.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	e.setenv(fakeclaude.EnvAgents, path)
}

func agentsServer(e *cliEnv) *tmux.Client {
	return tmux.New(tmux.Options{Bin: e.host.TmuxBin, Socket: tmux.Socket{Name: e.env["LYNA_TMUX_SOCKET_NAME"]}})
}

func TestAgentsCLI(t *testing.T) {
	e := newCLIEnv(t)
	e.withFakeClaude(t)
	agentsUnsetenv(e, "TMUX")
	e.start(t, "api", e.host.Home)
	pane, err := agentsServer(e).Display(tmuxtest.Context(t), "=api:", "#{pane_id} #{pane_pid} #{window_index}")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(pane)
	pid, _ := strconv.Atoi(fields[1])
	started := time.Now().Add(-90 * time.Minute).UnixMilli()
	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		outLacks []string
		errHas   []string
		check    func(t *testing.T, stdout string)
	}{
		{
			name: "no agents", args: []string{"agents"},
			setup:  func(t *testing.T) { t.Helper(); e.term.Interactive = false; agentsRecords(t, e) },
			outHas: []string{"No Claude agents running. Start one with: lyna-tmux create"},
		},
		{
			name: "table without a terminal is sanitized",
			setup: func(t *testing.T) {
				t.Helper()
				agentsRecords(t, e,
					agent.Record{PID: pid, CWD: e.host.Home, Kind: agent.KindInteractive, StartedAt: started, Name: "api\x1b]0;evil\x07", Status: "waiting"},
					agent.Record{ID: "job-1", CWD: e.host.Home, Kind: agent.KindBackground, StartedAt: started, State: agent.JobWorking},
					agent.Record{PID: 999999, CWD: filepath.Join(e.host.Home, "src", "web"), Kind: agent.KindInteractive, StartedAt: started, Status: "idle"},
				)
			},
			args:     []string{"agents"},
			outHas:   []string{"STATUS", "WHERE", "waiting", "api:" + fields[2], fields[0], "managed", strconv.Itoa(pid), "1h", "job working", "background", "~/src/web", "external", "999999"},
			outLacks: []string{"\x1b", "\x07"},
		},
		{
			name: "json snapshot", args: []string{"agents", "--json"},
			check: func(t *testing.T, stdout string) {
				t.Helper()
				var got struct {
					Version int   `json:"version"`
					TakenAt int64 `json:"takenAt"`
					Agents  []struct {
						Record   agent.Record `json:"record"`
						Location *struct {
							Session string `json:"session"`
							PaneID  string `json:"paneId"`
						} `json:"location"`
						Source string `json:"source"`
						Status string `json:"status"`
					} `json:"agents"`
				}
				if err := json.Unmarshal([]byte(stdout), &got); err != nil {
					t.Fatalf("not JSON: %v\n%s", err, stdout)
				}
				if got.Version != agent.SnapshotVersion || got.TakenAt == 0 || len(got.Agents) != 3 {
					t.Fatalf("snapshot %+v", got)
				}
				first := got.Agents[0]
				if first.Record.PID != pid || first.Location == nil || first.Location.Session != "api" || first.Location.PaneID != fields[0] || first.Source != "managed" || first.Status != "waiting" {
					t.Fatalf("first agent %+v", first)
				}
				if _, err := os.Stat(filepath.Join(e.host.Home, "cache", "agents.json")); err != nil {
					t.Fatalf("snapshot not cached: %v", err)
				}
			},
		},
		{name: "extra argument", args: []string{"agents", "api"}, wantCode: 1, errHas: []string{`unknown command "api"`}},
		{name: "popup and json exclude each other", args: []string{"agents", "--popup", "--json"}, wantCode: 1, errHas: []string{"popup", "json", "none of the others"}},
		{name: "popup outside tmux", args: []string{"agents", "--popup"}, wantCode: 1, errHas: []string{"$TMUX is not set"}},
		{
			name: "popup without a terminal", args: []string{"agents", "--popup"}, wantCode: 1, errHas: []string{"needs the terminal of a tmux popup"},
			setup: func(t *testing.T) { t.Helper(); e.setenv("TMUX", "/tmp/somewhere,1,0") },
			check: func(*testing.T, string) { agentsUnsetenv(e, "TMUX") },
		},
		{name: "help hides the popup flag", args: []string{"agents", "--help"}, outHas: []string{"--json", "claude attach"}, outLacks: []string{"--popup"}},
		{
			name: "claude missing", args: []string{"agents", "--json"}, wantCode: 1, errHas: []string{"install Claude Code"},
			setup: func(t *testing.T) {
				t.Helper()
				e.host.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
			},
		},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := e.run(t, st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			for _, s := range st.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range st.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%q", s, stdout)
				}
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t, stdout)
			}
		})
	}
}

// TestAgentsPopupAsTmuxRunsIt runs `agents --popup` with TMUX and TMUX_PANE
// of a pane on a server with a real attached client, as display-popup does,
// and checks where the client is after the keys.
func TestAgentsPopupAsTmuxRunsIt(t *testing.T) {
	cases := []struct {
		name       string
		keys       string
		wantJumped bool
	}{
		{name: "enter switches the client to the agent", keys: "\r", wantJumped: true},
		{name: "quit leaves the client where it was", keys: "q"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			e.withFakeClaude(t)
			n := tmuxtest.StartNested(t, "/dev/null", e.host.Home, 100, 30)
			ctx := tmuxtest.Context(t)
			inner := n.Inner.Client
			if _, err := inner.Run(ctx, "new-session", "-d", "-s", "api", "-x", "100", "-y", "29", "sleep 3600"); err != nil {
				t.Fatal(err)
			}
			out, err := inner.Run(ctx, "new-window", "-d", "-P", "-F", "#{pane_id} #{pane_pid} #{window_index}", "-t", "=api:", "sleep 3600")
			if err != nil {
				t.Fatal(err)
			}
			target := strings.Fields(out)
			pid, _ := strconv.Atoi(target[1])
			mainPane, err := inner.Display(ctx, "=main:", "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			socket, err := inner.Display(ctx, "=main:", "#{socket_path},#{pid},0")
			if err != nil {
				t.Fatal(err)
			}
			e.setenv("TMUX", socket)
			e.setenv("TMUX_PANE", mainPane)
			agentsRecords(t, e, agent.Record{PID: pid, CWD: e.host.Home, Kind: agent.KindInteractive, StartedAt: time.Now().UnixMilli(), Name: "popup-agent", Status: "waiting"})
			// The first frame draws the saved snapshot, so the key acts on the
			// agent whatever the order of the refresh and the key.
			if code, _, stderr := e.run(t, "agents", "--json"); code != 0 {
				t.Fatalf("refresh: %s", stderr)
			}
			client := func() string {
				out, err := inner.Run(ctx, "list-clients", "-F", "#{client_session}:#{window_index}:#{pane_id}")
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out)
			}
			code, stdout, stderr := agentsCLI(t, e, strings.NewReader(tc.keys), "agents", "--popup")
			if code != 0 {
				t.Fatalf("exit %d\nstderr: %s\nstdout: %q", code, stderr, stdout)
			}
			if !strings.Contains(stdout, "popup-agent") {
				t.Fatalf("picker never drew the agent:\n%q", stdout)
			}
			// The flag reaches the picker: in a popup the quit key closes an
			// overlay, and the footer says so.
			if frame := ansi.Strip(stdout); !strings.Contains(frame, "q close") {
				t.Fatalf("--popup never reached the picker:\n%s", frame)
			}
			want := "main:0:" + mainPane
			if tc.wantJumped {
				want = "api:" + target[2] + ":" + target[0]
			}
			if got := client(); got != want {
				t.Fatalf("client at %q, want %q", got, want)
			}
			if len(e.execs) != 0 {
				t.Fatalf("a popup jump replaced the process: %q", e.execs)
			}
		})
	}
}
