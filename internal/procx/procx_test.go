package procx

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestIsClaude(t *testing.T) {
	cases := []struct {
		name string
		proc Proc
		want bool
	}{
		{name: "native through link on macOS", proc: Proc{Comm: "2.1.272", Exe: "/Users/u/.local/bin/claude", Args: []string{"claude", "--resume", "x"}}, want: true},
		{name: "native resolved on Linux", proc: Proc{Comm: "claude", Exe: "/home/u/.local/share/claude/versions/2.1.272", Args: []string{"claude"}}, want: true},
		{name: "versions path without arguments", proc: Proc{Comm: "2.1.272", Exe: "/opt/claude/versions/2.1.272"}, want: true},
		{name: "kernel name only", proc: Proc{Comm: "claude"}, want: true},
		{name: "argv0 versions path agreeing with the kernel name", proc: Proc{Comm: "2.1.1", Args: []string{"/home/u/.local/share/claude/versions/2.1.1"}}, want: true},
		// argv[0] is what the parent handed the process, so on its own it
		// says nothing. Each of these is another program claiming the name.
		{name: "argv0 claims a claude link", proc: Proc{Comm: "sleep", Exe: "/usr/bin/sleep", Args: []string{"/tmp/x/claude", "60"}}, want: false},
		{name: "argv0 claims a claude link with no executable path", proc: Proc{Comm: "sleep", Args: []string{"/tmp/x/claude", "60"}}, want: false},
		{name: "argv0 claims a versions path", proc: Proc{Comm: "sleep", Exe: "/usr/bin/sleep", Args: []string{"/opt/claude/versions/2.1.1"}}, want: false},
		{name: "argv0 claims a versions path the kernel name contradicts", proc: Proc{Comm: "sleep", Args: []string{"/opt/claude/versions/2.1.1"}}, want: false},
		{name: "argv0 claims a runtime", proc: Proc{Comm: "sleep", Exe: "/usr/bin/sleep", Args: []string{"node", "/x/claude-code/cli.js"}}, want: false},
		{name: "argv0 claims a runtime with no executable path", proc: Proc{Comm: "sleep", Args: []string{"node", "/x/claude-code/cli.js"}}, want: false},
		{name: "npm entry point", proc: Proc{Comm: "node", Exe: "/usr/bin/node", Args: []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "-c"}}, want: true},
		{name: "npm bin shim with runtime flags", proc: Proc{Comm: "node", Args: []string{"node", "--no-warnings", "/usr/local/bin/claude"}}, want: true},
		{name: "bun runs the entry point", proc: Proc{Comm: "bun", Exe: "/opt/bun", Args: []string{"bun", "/x/claude-code/cli.mjs"}}, want: true},
		{name: "plugin server under .claude is not claude", proc: Proc{Comm: "node", Exe: "/usr/bin/node", Args: []string{"node", "/Users/u/.claude/plugins/cache/x/bridge/mcp-server.cjs"}}, want: false},
		{name: "claude named directory is not claude", proc: Proc{Comm: "node", Args: []string{"node", "/work/claude/index.js"}}, want: false},
		{name: "runtime without script", proc: Proc{Comm: "node", Args: []string{"node", "--version"}}, want: false},
		{name: "claude as a later argument of another program", proc: Proc{Comm: "vim", Exe: "/usr/bin/vim", Args: []string{"vim", "claude"}}, want: false},
		{name: "claude-like name", proc: Proc{Comm: "claude-helper", Exe: "/bin/claude-helper", Args: []string{"claude-helper"}}, want: false},
		{name: "unreadable arguments", proc: Proc{Comm: "2.1.272"}, want: false},
		{name: "empty", proc: Proc{}, want: false},
		{name: "relative versions name", proc: Proc{Exe: "versions"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClaude(tc.proc); got != tc.want {
				t.Errorf("IsClaude(%+v) = %v, want %v", tc.proc, got, tc.want)
			}
		})
	}
}

func TestProcName(t *testing.T) {
	cases := []struct {
		name string
		proc Proc
		want string
	}{
		{name: "from exe", proc: Proc{Comm: "2.1.272", Exe: "/x/claude"}, want: "claude"},
		{name: "from comm", proc: Proc{Comm: "launchd"}, want: "launchd"},
		{name: "none", proc: Proc{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.proc.Name(); got != tc.want {
				t.Errorf("Name() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTable(t *testing.T) {
	table := NewTable([]Proc{
		{PID: 1, PPID: 0, Comm: "init"},
		{PID: 10, PPID: 1, Comm: "sh"},
		{PID: 11, PPID: 10, Comm: "old"},
		{PID: 11, PPID: 10, Comm: "claude"},
	})
	if table.Len() != 3 {
		t.Fatalf("Len = %d, want 3", table.Len())
	}
	cases := []struct {
		name     string
		pid      int
		wantPPID int
		wantOK   bool
		wantComm string
	}{
		{name: "leaf", pid: 11, wantPPID: 10, wantOK: true, wantComm: "claude"},
		{name: "middle", pid: 10, wantPPID: 1, wantOK: true, wantComm: "sh"},
		{name: "root", pid: 1, wantPPID: 0, wantOK: true, wantComm: "init"},
		{name: "unknown", pid: 99},
	}
	parents := table.Parents()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ppid, ok := parents(tc.pid)
			if ppid != tc.wantPPID || ok != tc.wantOK {
				t.Errorf("Parents(%d) = %d, %v; want %d, %v", tc.pid, ppid, ok, tc.wantPPID, tc.wantOK)
			}
			p, ok := table.Lookup(tc.pid)
			if ok != tc.wantOK || p.Comm != tc.wantComm {
				t.Errorf("Lookup(%d) = %+v, %v; want comm %q, %v", tc.pid, p, ok, tc.wantComm, tc.wantOK)
			}
		})
	}
}

// fakeSignaler serves a scripted process and records signals.
type fakeSignaler struct {
	proc    Proc
	readErr error
	sigErr  error
	sent    []syscall.Signal
}

func (f *fakeSignaler) signalVerified(_ int, sig syscall.Signal, verify func(Proc) error) error {
	if f.readErr != nil {
		return f.readErr
	}
	if err := verify(f.proc); err != nil {
		return err
	}
	if f.sigErr != nil {
		return f.sigErr
	}
	f.sent = append(f.sent, sig)
	return nil
}

func TestKillVerification(t *testing.T) {
	start := time.Unix(1789492217, 44216000)
	claude := Proc{PID: 500, Start: start, Exe: "/u/.local/bin/claude", Args: []string{"claude"}}
	boom := errors.New("boom")
	cases := []struct {
		name     string
		pid      int
		expected time.Time
		fake     fakeSignaler
		wantErr  error
		wantSent bool
	}{
		{name: "same claude process", pid: 500, expected: start, fake: fakeSignaler{proc: claude}, wantSent: true},
		{name: "pid reused", pid: 500, expected: start.Add(time.Microsecond), fake: fakeSignaler{proc: claude}, wantErr: ErrIdentityChanged},
		{name: "not claude", pid: 500, expected: start, fake: fakeSignaler{proc: Proc{PID: 500, Start: start, Comm: "sleep", Args: []string{"sleep"}}}, wantErr: ErrNotClaude},
		{name: "gone", pid: 500, expected: start, fake: fakeSignaler{readErr: ErrNotFound}, wantErr: ErrNotFound},
		{name: "signal failure", pid: 500, expected: start, fake: fakeSignaler{proc: claude, sigErr: boom}, wantErr: boom},
		{name: "pid 1", pid: 1, expected: start, fake: fakeSignaler{proc: claude}, wantErr: ErrInvalidPID},
		{name: "pid 0 is a process group", pid: 0, expected: start, fake: fakeSignaler{proc: claude}, wantErr: ErrInvalidPID},
		{name: "negative pid is every process", pid: -1, expected: start, fake: fakeSignaler{proc: claude}, wantErr: ErrInvalidPID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := tc.fake
			err := kill(&fake, tc.pid, tc.expected, syscall.SIGTERM)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("kill: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("kill error = %v, want %v", err, tc.wantErr)
			}
			if sent := len(fake.sent) == 1; sent != tc.wantSent {
				t.Errorf("signal sent = %v, want %v", sent, tc.wantSent)
			}
		})
	}
}
