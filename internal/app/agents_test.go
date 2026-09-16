package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/procx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// agentsEnv is a test host whose claude is the fake, listing the records of
// a file the test writes.
type agentsEnv struct {
	*testHost
	agents string
}

func newAgentsEnv(t *testing.T) *agentsEnv {
	t.Helper()
	h := newTestHost(t)
	fake := fakeclaude.Build(t)
	e := &agentsEnv{testHost: h, agents: filepath.Join(h.root, "agents.json")}
	delete(h.env, "TMUX")
	h.env[fakeclaude.EnvAgents] = e.agents
	h.refreshEnviron()
	h.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return fake, nil
		}
		return "", os.ErrNotExist
	}
	return e
}

func (e *agentsEnv) setenv(k, v string) {
	if v == "" {
		delete(e.env, k)
	} else {
		e.env[k] = v
	}
	e.refreshEnviron()
}

func (e *agentsEnv) writeRecords(t *testing.T, records ...agent.Record) {
	t.Helper()
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.agents, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// agentsDisplay reads one format on a server.
func agentsDisplay(t *testing.T, c *tmux.Client, target, format string) string {
	t.Helper()
	out, err := c.Display(tmuxtest.Context(t), target, format)
	if err != nil {
		t.Fatalf("display %s on %s: %v", format, target, err)
	}
	return out
}

// agentsNewWindow adds a window running argv to a session and returns its
// pane id and root process id.
func agentsNewWindow(t *testing.T, c *tmux.Client, session, name string, argv ...string) (paneID string, pid int) {
	t.Helper()
	args := append([]string{"-d", "-P", "-F", "#{pane_id} #{pane_pid}", "-t", tmux.ExactSession(session), "-n", name}, argv...)
	out, err := c.Run(tmuxtest.Context(t), "new-window", args...)
	if err != nil {
		t.Fatal(err)
	}
	id, pidText, _ := strings.Cut(strings.TrimSpace(out), " ")
	pid, err = strconv.Atoi(pidText)
	if err != nil {
		t.Fatalf("new-window printed %q", out)
	}
	return id, pid
}

func agentsByName(t *testing.T, snap agent.Snapshot) map[string]agent.Agent {
	t.Helper()
	out := map[string]agent.Agent{}
	for _, a := range snap.Agents {
		out[a.Record.Name] = a
	}
	return out
}

func TestAgentsViewRefresh(t *testing.T) {
	e := newAgentsEnv(t)
	s := openServer(t, e.testHost)
	startWorkspace(t, s, "api")
	ctx := tmuxtest.Context(t)
	managedFields := strings.Fields(agentsDisplay(t, s.Client, "=api:", "#{pane_id} #{pane_pid} #{window_index}"))
	managedID, managedWindow := managedFields[0], "api:"+managedFields[2]
	managedPID, _ := strconv.Atoi(managedFields[1])

	// A Claude process started below a shell in a plain pane is found
	// through its ancestry.
	childFile := filepath.Join(e.root, "child.pid")
	shellID, _ := agentsNewWindow(t, s.Client, "api", "tests", "/bin/sh", "-c", "sleep 3600 & echo $! > "+childFile+"; wait")
	shellWindow := "api:" + agentsDisplay(t, s.Client, shellID, "#{window_index}")
	var childPID int
	tmuxtest.WaitFor(t, "child pid", func() bool {
		// The file holds one line written by a single echo, so its newline is
		// the last byte written: a read that sees it saw the whole pid.
		data, err := os.ReadFile(childFile)
		if err != nil || !strings.HasSuffix(string(data), "\n") {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	})

	other := tmuxtest.Start(t)
	otherPane := agentsDisplay(t, other.Client, "=base:", "#{socket_path} #{pid} #{pane_id} #{pane_pid}")
	otherFields := strings.Fields(otherPane)
	otherPID, _ := strconv.Atoi(otherFields[3])

	started := time.Now().Add(-time.Hour).UnixMilli()
	e.writeRecords(t,
		agent.Record{PID: managedPID, CWD: e.root, Kind: agent.KindInteractive, StartedAt: started, Name: "managed", Status: "waiting"},
		agent.Record{PID: childPID, CWD: e.root, Kind: agent.KindInteractive, StartedAt: started, Name: "nested", Status: "busy"},
		agent.Record{PID: otherPID, CWD: e.root, Kind: agent.KindInteractive, StartedAt: started, Name: "other", Status: "idle"},
		agent.Record{ID: "job-1", CWD: e.root, Kind: agent.KindBackground, StartedAt: started, Name: "job", State: agent.JobWorking},
	)
	now := time.UnixMilli(time.Now().UnixMilli())
	type where struct {
		source agent.Source
		pane   string
		window string
	}
	cases := []struct {
		name string
		tmux string
		want map[string]where
	}{
		{
			name: "outside tmux locates agents on the lyna-tmux server",
			want: map[string]where{
				"managed": {agent.SourceManaged, managedID, managedWindow},
				"nested":  {agent.SourcePane, shellID, shellWindow},
				"other":   {source: agent.SourceExternal},
				"job":     {source: agent.SourceBackground},
			},
		},
		{
			name: "inside tmux locates agents on the server of TMUX",
			tmux: otherFields[0] + "," + otherFields[1] + ",0",
			want: map[string]where{
				"managed": {source: agent.SourceExternal},
				"nested":  {source: agent.SourceExternal},
				"other":   {agent.SourcePane, otherFields[2], "base:0"},
				"job":     {source: agent.SourceBackground},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.setenv("TMUX", tc.tmux)
			v := NewAgentsView(e.Host, s, func() time.Time { return now })
			if v.Inside() != (tc.tmux != "") {
				t.Fatalf("Inside() = %v", v.Inside())
			}
			snap, err := v.Refresh(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !snap.TakenAt.Equal(now) || len(snap.Agents) != len(tc.want) {
				t.Fatalf("snapshot at %v with %d agents", snap.TakenAt, len(snap.Agents))
			}
			for name, a := range agentsByName(t, snap) {
				w := tc.want[name]
				got := where{source: a.Source}
				if a.Location != nil {
					got.pane, got.window = a.Location.PaneID, a.Location.Session+":"+strconv.Itoa(a.Location.WindowIndex)
				}
				if got != w {
					t.Errorf("%s: %+v, want %+v", name, got, w)
				}
			}
			assertMode(t, s.Paths.AgentsCache(), 0o600)
			cached, ok := v.Cached()
			if !ok || !cached.TakenAt.Equal(now) || len(cached.Agents) != len(snap.Agents) {
				t.Fatalf("cached %+v, %v", cached, ok)
			}
			for i := range cached.Agents {
				if cached.Agents[i].Record.Key() != snap.Agents[i].Record.Key() || (cached.Agents[i].Location == nil) != (snap.Agents[i].Location == nil) {
					t.Fatalf("cached agent %d = %+v, want %+v", i, cached.Agents[i], snap.Agents[i])
				}
			}
		})
	}
}

func TestAgentsViewCacheAndFailures(t *testing.T) {
	e := newAgentsEnv(t)
	s := openServer(t, e.testHost)
	e.writeRecords(t, agent.Record{PID: 999999, CWD: e.root, Kind: agent.KindInteractive, StartedAt: 1, Name: "lonely"})
	cache := s.Paths.AgentsCache()
	write := func(data []byte) func(t *testing.T) {
		return func(t *testing.T) {
			t.Helper()
			mkdir(t, filepath.Dir(cache))
			if err := os.WriteFile(cache, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name       string
		setup      func(t *testing.T)
		wantCached bool
		refreshErr string
	}{
		{name: "no cache yet", wantCached: false},
		{name: "garbage cache", setup: write([]byte("{not json")), wantCached: false},
		{name: "oversized cache", setup: write([]byte(`{"version":1,"agents":[]}` + strings.Repeat(" ", agent.MaxSnapshotSize))), wantCached: false},
		{name: "other wire version", setup: write([]byte(`{"version":2,"agents":[]}`)), wantCached: false},
		{name: "valid cache", setup: write([]byte(`{"version":1,"takenAt":5,"agents":[]}`)), wantCached: true},
		{
			name: "linked cache is neither read nor replaced",
			setup: func(t *testing.T) {
				t.Helper()
				target := filepath.Join(e.root, "planted.json")
				if err := os.WriteFile(target, []byte(`{"version":1,"agents":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
				_ = os.Remove(cache)
				if err := os.Symlink(target, cache); err != nil {
					t.Fatal(err)
				}
			},
			wantCached: false,
			refreshErr: "symbolic link",
		},
		{
			name: "claude missing",
			setup: func(t *testing.T) {
				t.Helper()
				_ = os.Remove(cache)
				e.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
			},
			refreshErr: "install Claude Code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			v := NewAgentsView(e.Host, s, nil)
			if _, ok := v.Cached(); ok != tc.wantCached {
				t.Fatalf("Cached ok = %v, want %v", ok, tc.wantCached)
			}
			snap, err := v.Refresh(tmuxtest.Context(t))
			if tc.refreshErr == "" {
				if err != nil || len(snap.Agents) != 1 || snap.Agents[0].Source != agent.SourceExternal {
					t.Fatalf("Refresh = %+v, %v", snap, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refreshErr) {
				t.Fatalf("Refresh error %v, want %q", err, tc.refreshErr)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(e.root, "planted.json")); err != nil {
		t.Fatalf("the link target was touched: %v", err)
	}
}

func TestAgentsViewJumpInside(t *testing.T) {
	n := tmuxtest.StartNested(t, "/dev/null", t.TempDir(), 100, 30)
	e := newAgentsEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	if _, err := n.Inner.Client.Run(ctx, "new-session", "-d", "-s", "api", "-x", "100", "-y", "29", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	target, pid := agentsNewWindow(t, n.Inner.Client, "api", "tests", "sleep 3600")
	mainPane := agentsDisplay(t, n.Inner.Client, "=main:", "#{pane_id}")
	socket := agentsDisplay(t, n.Inner.Client, "=main:", "#{socket_path},#{pid},0")
	e.setenv("TMUX", socket)
	e.setenv("TMUX_PANE", mainPane)
	e.writeRecords(t, agent.Record{PID: pid, CWD: e.root, Kind: agent.KindInteractive, StartedAt: 1, Name: "tests"})

	v := NewAgentsView(e.Host, s, nil)
	snap, err := v.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || snap.Agents[0].Location == nil || snap.Agents[0].Location.PaneID != target {
		t.Fatalf("agents %+v", snap.Agents)
	}
	a := snap.Agents[0]

	// A second client, attached later to another session, is the one tmux
	// would pick without the calling pane: the jump must move the client of
	// TMUX_PANE, not the most recently active one.
	if _, err := n.Inner.Client.Run(ctx, "new-session", "-d", "-s", "other", "-x", "100", "-y", "29", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	attach := tmux.ShellJoin(n.Inner.Bin, "-L", n.Inner.Name, "attach-session", "-t", "=other")
	if _, err := n.Outer.Client.Run(ctx, "new-session", "-d", "-s", "term2", "-x", "100", "-y", "30", attach); err != nil {
		t.Fatal(err)
	}
	otherPane := agentsDisplay(t, n.Inner.Client, "=other:", "#{pane_id}")
	clientView := func() []string {
		out, err := n.Inner.Client.Run(ctx, "list-clients", "-F", "#{client_session}:#{window_index}:#{pane_id}")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Fields(out)
		slices.Sort(lines)
		return lines
	}
	tmuxtest.WaitFor(t, "second client", func() bool { return len(clientView()) == 2 })
	if got, want := clientView(), []string{"main:0:" + mainPane, "other:0:" + otherPane}; !slices.Equal(got, want) {
		t.Fatalf("clients start at %q, want %q", got, want)
	}
	att, err := v.Jump(ctx, a)
	if err != nil || att != nil {
		t.Fatalf("Jump = %+v, %v", att, err)
	}
	if got, want := clientView(), []string{"api:1:" + target, "other:0:" + otherPane}; !slices.Equal(got, want) {
		t.Fatalf("clients show %q after the jump, want %q", got, want)
	}
	// The pane is the active one of its window, not only the client's view.
	if got := agentsDisplay(t, n.Inner.Client, "=api:1", "#{pane_id}"); got != target {
		t.Fatalf("active pane %q", got)
	}

	if _, err := v.Preview(ctx, a, 10); err != nil {
		t.Fatalf("Preview on the server of TMUX: %v", err)
	}

	if _, err := n.Inner.Client.Run(ctx, "kill-pane", "-t", target); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Jump(ctx, a); !errors.Is(err, ErrAgentNoPane) {
		t.Fatalf("jump to a closed pane: %v", err)
	}
}

func TestAgentsViewJumpOutsideAndPreview(t *testing.T) {
	e := newAgentsEnv(t)
	s := openServer(t, e.testHost)
	startWorkspace(t, s, "api")
	ctx := tmuxtest.Context(t)
	target, pid := agentsNewWindow(t, s.Client, "api", "tests", "/bin/sh", "-c", "printf 'preview-marker\\n'; exec sleep 3600")
	e.writeRecords(t, agent.Record{PID: pid, CWD: e.root, Kind: agent.KindInteractive, StartedAt: 1, Name: "tests"})
	v := NewAgentsView(e.Host, s, nil)
	snap, err := v.Refresh(ctx)
	if err != nil || len(snap.Agents) != 1 || snap.Agents[0].Location == nil {
		t.Fatalf("Refresh = %+v, %v", snap, err)
	}
	a := snap.Agents[0]
	tmuxtest.WaitFor(t, "pane output", func() bool {
		out, err := v.Preview(ctx, a, 5)
		return err == nil && strings.Contains(out, "preview-marker")
	})
	targetWindow := agentsDisplay(t, s.Client, target, "#{window_index}")
	if got := agentsDisplay(t, s.Client, "=api:", "#{window_index}"); got == targetWindow {
		t.Fatalf("the agent's window %s is current before the jump", got)
	}
	att, err := v.Jump(ctx, a)
	if err != nil || att == nil {
		t.Fatalf("Jump = %+v, %v", att, err)
	}
	if !slices.Contains(att.Argv, "attach-session") || att.Argv[len(att.Argv)-1] != "=api:" {
		t.Fatalf("attach argv %q", att.Argv)
	}
	if got := agentsDisplay(t, s.Client, "=api:", "#{window_index}:#{pane_id}"); got != targetWindow+":"+target {
		t.Fatalf("session shows %q, want %s:%s", got, targetWindow, target)
	}

	bad := []struct {
		name string
		loc  *agent.Location
		want string
	}{
		{name: "no pane", loc: nil, want: ErrAgentNoPane.Error()},
		{name: "injected target", loc: &agent.Location{PaneID: "%1;kill-server"}, want: "invalid pane id"},
		{name: "session target instead of pane", loc: &agent.Location{PaneID: "=api:"}, want: "invalid pane id"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			a := agent.Agent{Record: agent.Record{PID: 5, Name: "x"}, Location: tc.loc}
			if _, err := v.Jump(ctx, a); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Jump error %v, want %q", err, tc.want)
			}
			if _, err := v.Preview(ctx, a, 5); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Preview error %v, want %q", err, tc.want)
			}
		})
	}
	if ok, err := s.Client.HasSession(ctx, "api"); !ok || err != nil {
		t.Fatalf("server damaged by a refused target: %v %v", ok, err)
	}
}

func TestAgentsViewAttachJob(t *testing.T) {
	e := newAgentsEnv(t)
	s := openServer(t, e.testHost)
	fake, _ := e.LookPath("claude")
	e.setenv("TMUX", "/tmp/user-tmux,1,0")
	e.setenv("TMUX_PANE", "%3")
	e.setenv("CLAUDE_CODE_MESSAGING_TOKEN", "secret")
	job := func(id string) agent.Agent {
		return agent.Agent{Record: agent.Record{ID: id, Kind: agent.KindBackground}, Source: agent.SourceBackground}
	}
	cases := []struct {
		name    string
		agent   agent.Agent
		lookup  func(string) (string, error)
		want    []string
		wantErr string
	}{
		{name: "background job", agent: job("job-1.a_b"), want: []string{fake, "attach", "job-1.a_b"}},
		{name: "option-like id", agent: job("-rf"), wantErr: "invalid background job id"},
		{name: "id with a space", agent: job("a b"), wantErr: "invalid background job id"},
		{name: "overlong id", agent: job(strings.Repeat("a", 129)), wantErr: "invalid background job id"},
		{name: "interactive agent", agent: agent.Agent{Record: agent.Record{PID: 9, ID: "x"}, Source: agent.SourcePane}, wantErr: ErrAgentNotJob.Error()},
		{name: "job without id", agent: job(""), wantErr: ErrAgentNotJob.Error()},
		{name: "claude missing", agent: job("job-1"), lookup: func(string) (string, error) { return "", os.ErrNotExist }, wantErr: claude.ErrNotFound.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := e.Host
			if tc.lookup != nil {
				h.LookPath = tc.lookup
			}
			att, err := NewAgentsView(h, s, nil).AttachJob(tc.agent)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(att.Argv, tc.want) {
				t.Fatalf("AttachJob = %q, %v", att.Argv, err)
			}
			if !slices.Contains(att.Env, "TMUX=/tmp/user-tmux,1,0") || !slices.Contains(att.Env, "TMUX_PANE=%3") {
				t.Fatalf("attach env lacks the terminal's tmux: %q", att.Env)
			}
			for _, kv := range att.Env {
				if strings.HasPrefix(kv, "CLAUDE_CODE_MESSAGING_TOKEN=") {
					t.Fatalf("attach env keeps %q", kv)
				}
			}
		})
	}
}

func TestAgentsKillGuards(t *testing.T) {
	start := time.UnixMilli(1_700_000_000_000).Add(250 * time.Microsecond)
	claudeProc := procx.Proc{PID: 42, Start: start, Comm: "claude"}
	cases := []struct {
		name     string
		record   agent.Record
		proc     procx.Proc
		readErr  error
		killErr  error
		wantErr  error
		wantKill bool
	}{
		{name: "verified claude", record: agent.Record{PID: 42, StartedAt: start.UnixMilli() + 900}, proc: claudeProc, wantKill: true},
		{name: "stamped in the same millisecond", record: agent.Record{PID: 42, StartedAt: start.UnixMilli()}, proc: claudeProc, wantKill: true},
		{name: "signal failure", record: agent.Record{PID: 42, StartedAt: start.UnixMilli() + 1}, proc: claudeProc, killErr: procx.ErrIdentityChanged, wantErr: procx.ErrIdentityChanged, wantKill: true},
		{name: "pid reused after the listing", record: agent.Record{PID: 42, StartedAt: start.UnixMilli() - 1}, proc: claudeProc, wantErr: procx.ErrIdentityChanged},
		{name: "not claude", record: agent.Record{PID: 42, StartedAt: start.UnixMilli() + 900}, proc: procx.Proc{PID: 42, Start: start, Comm: "sleep"}, wantErr: procx.ErrNotClaude},
		{name: "already exited", record: agent.Record{PID: 42, StartedAt: start.UnixMilli()}, readErr: procx.ErrNotFound, wantErr: procx.ErrNotFound},
		{name: "unreadable", record: agent.Record{PID: 42, StartedAt: start.UnixMilli()}, readErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "no start time", record: agent.Record{PID: 42}, proc: claudeProc, wantErr: ErrAgentIdentity},
		{name: "background job without process", record: agent.Record{ID: "job", StartedAt: 5}, wantErr: procx.ErrInvalidPID},
		{name: "init", record: agent.Record{PID: 1, StartedAt: 5}, wantErr: procx.ErrInvalidPID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			killed := false
			err := agentsKill(tc.record,
				func(pid int) (procx.Proc, error) {
					if pid != tc.record.PID {
						t.Fatalf("read pid %d", pid)
					}
					return tc.proc, tc.readErr
				},
				func(pid int, st time.Time, sig syscall.Signal) error {
					killed = true
					if pid != tc.record.PID || !st.Equal(tc.proc.Start) || sig != syscall.SIGTERM {
						t.Fatalf("kill(%d, %v, %v)", pid, st, sig)
					}
					return tc.killErr
				})
			if killed != tc.wantKill {
				t.Fatalf("killed = %v, want %v", killed, tc.wantKill)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestAgentsKillProcess signals real processes: a Claude process is stopped
// only when the listing proves its identity.
func TestAgentsKillProcess(t *testing.T) {
	fake := fakeclaude.Build(t)
	start := func(t *testing.T, argv ...string) (*exec.Cmd, <-chan struct{}) {
		t.Helper()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		ready := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				if sc.Text() == fakeclaude.ReadyLine {
					close(ready)
				}
			}
			_ = cmd.Wait()
			close(exited)
		}()
		t.Cleanup(func() {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			<-exited
		})
		if filepath.Base(argv[0]) == "claude" {
			select {
			case <-ready:
			case <-time.After(10 * time.Second):
				t.Fatal("fake claude never became ready")
			}
		}
		return cmd, exited
	}
	cases := []struct {
		name      string
		argv      []string
		startedAt func(p procx.Proc) int64
		wantErr   error
	}{
		{name: "listed claude stops", argv: []string{fake}, startedAt: func(p procx.Proc) int64 { return p.Start.UnixMilli() + 200 }},
		{name: "process started after the listing", argv: []string{fake}, startedAt: func(p procx.Proc) int64 { return p.Start.UnixMilli() - 1000 }, wantErr: procx.ErrIdentityChanged},
		{name: "not a claude process", argv: []string{"/bin/sleep", "3600"}, startedAt: func(p procx.Proc) int64 { return p.Start.UnixMilli() + 200 }, wantErr: procx.ErrNotClaude},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, done := start(t, tc.argv...)
			p, err := procx.Read(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			a := agent.Agent{Record: agent.Record{PID: cmd.Process.Pid, Kind: agent.KindInteractive, StartedAt: tc.startedAt(p)}}
			err = (&AgentsView{}).Kill(a)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Kill error %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
					t.Fatalf("refused kill still stopped the process: %v", err)
				}
				return
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("claude did not exit after SIGTERM")
			}
		})
	}
}
