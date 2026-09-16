//go:build darwin || linux

package procx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// startSleeper starts `sleep 300` through argv0 (a path whose base name may be
// "claude"), and reaps it when the test ends.
func startSleeper(t *testing.T, argv0 string) *exec.Cmd {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not installed")
	}
	if argv0 == "" {
		argv0 = sleep
	} else {
		if err := os.Symlink(sleep, argv0); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(argv0, "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestSnapshotSelfAndChild(t *testing.T) {
	child := startSleeper(t, "")
	table, err := Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if table.Len() < 2 {
		t.Fatalf("snapshot has %d processes", table.Len())
	}
	self, ok := table.Lookup(os.Getpid())
	if !ok {
		t.Fatal("own process missing from the snapshot")
	}
	if self.PPID != os.Getppid() {
		t.Errorf("own PPID = %d, want %d", self.PPID, os.Getppid())
	}
	exe, _ := os.Executable()
	if self.Name() != filepath.Base(exe) {
		t.Errorf("own name = %q, want %q", self.Name(), filepath.Base(exe))
	}
	now := time.Now()
	if self.Start.IsZero() || self.Start.After(now) || now.Sub(self.Start) > 24*time.Hour {
		t.Errorf("own start = %v, want within the last day", self.Start)
	}

	got, ok := table.Lookup(child.Process.Pid)
	if !ok {
		t.Fatal("child missing from the snapshot")
	}
	if got.PPID != os.Getpid() {
		t.Errorf("child PPID = %d, want %d", got.PPID, os.Getpid())
	}
	if len(got.Args) != 2 || got.Args[1] != "300" || filepath.Base(got.Args[0]) != "sleep" {
		t.Errorf("child args = %q, want [sleep 300]", got.Args)
	}
	if got.Start.Before(self.Start) {
		t.Errorf("child start %v before parent start %v", got.Start, self.Start)
	}
	if ppid, ok := table.Parents()(child.Process.Pid); !ok || ppid != os.Getpid() {
		t.Errorf("Parents(child) = %d, %v", ppid, ok)
	}

	single, err := Read(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !single.Start.Equal(got.Start) || single.PPID != got.PPID || single.Exe != got.Exe {
		t.Errorf("Read = %+v, snapshot = %+v", single, got)
	}
}

func TestReadErrors(t *testing.T) {
	done := exec.Command("sleep", "0")
	if err := done.Run(); err != nil {
		t.Skipf("sleep not runnable: %v", err)
	}
	cases := []struct {
		name string
		pid  int
		want error
	}{
		{name: "exited and reaped", pid: done.Process.Pid, want: ErrNotFound},
		{name: "zero", pid: 0, want: ErrInvalidPID},
		{name: "negative", pid: -3, want: ErrInvalidPID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Read(tc.pid); !errors.Is(err, tc.want) {
				t.Errorf("Read(%d) error = %v, want %v", tc.pid, err, tc.want)
			}
		})
	}
}

func TestKillProcesses(t *testing.T) {
	cases := []struct {
		name       string
		claudeLink bool
		startShift time.Duration
		wantErr    error
	}{
		{name: "claude with matching identity is signaled", claudeLink: true},
		{name: "claude with another start time is refused", claudeLink: true, startShift: time.Second, wantErr: ErrIdentityChanged},
		{name: "non claude process is refused", wantErr: ErrNotClaude},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv0 := ""
			if tc.claudeLink {
				argv0 = filepath.Join(t.TempDir(), "claude")
			}
			cmd := startSleeper(t, argv0)
			pid := cmd.Process.Pid
			p, err := Read(pid)
			if err != nil {
				t.Fatal(err)
			}
			if IsClaude(p) != tc.claudeLink {
				t.Fatalf("IsClaude(%+v) = %v, want %v", p, !tc.claudeLink, tc.claudeLink)
			}
			err = Kill(pid, p.Start.Add(tc.startShift), syscall.SIGTERM)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Kill error = %v, want %v", err, tc.wantErr)
				}
				if _, err := Read(pid); err != nil {
					t.Fatalf("refused kill still affected the process: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Kill: %v", err)
			}
			waitErr := cmd.Wait()
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("wait = %v, want a signal exit", waitErr)
			}
			status, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
				t.Fatalf("exit status = %v, want SIGTERM", exitErr)
			}
		})
	}
	t.Run("reaped process is not found", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "claude")
		cmd := startSleeper(t, link)
		p, err := Read(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err := Kill(p.PID, p.Start, syscall.SIGTERM); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Kill after exit = %v, want ErrNotFound", err)
		}
	})
}

func TestSnapshotPaneTTY(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	panes, err := srv.Client.ListPanes(ctx, "")
	if err != nil || len(panes) == 0 {
		t.Fatalf("list panes: %v (%d panes)", err, len(panes))
	}
	table, err := Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range panes {
		p, ok := table.Lookup(pane.PID)
		if !ok {
			t.Fatalf("pane process %d missing", pane.PID)
		}
		if p.TTY != pane.TTY {
			t.Errorf("pane %s process tty = %q, tmux reports %q", pane.ID, p.TTY, pane.TTY)
		}
		if !strings.HasPrefix(p.TTY, "/dev/") {
			t.Errorf("tty %q is not a device path", p.TTY)
		}
	}
}

// TestKillRefusesSpoofedArgv0 starts a program that is not Claude Code with an
// argument vector naming one, which is all any process has to do to pass a
// check made on argv[0]. The kernel keeps reporting what it really is, so
// neither IsClaude nor Kill accepts it.
func TestKillRefusesSpoofedArgv0(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not installed")
	}
	cases := []struct{ name, argv0 string }{
		{name: "a claude link", argv0: filepath.Join(t.TempDir(), "claude")},
		{name: "a native installer path", argv0: "/opt/claude/versions/2.1.272"},
		{name: "the bare name", argv0: "claude"},
		{name: "a runtime running the package entry point", argv0: "node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &exec.Cmd{Path: sleep, Args: []string{tc.argv0, "300"}}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			p, err := Read(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			if IsClaude(p) {
				t.Fatalf("IsClaude(%+v) = true for a sleep process calling itself claude", p)
			}
			if err := Kill(cmd.Process.Pid, p.Start, syscall.SIGTERM); !errors.Is(err, ErrNotClaude) {
				t.Fatalf("Kill = %v, want ErrNotClaude", err)
			}
			if _, err := Read(cmd.Process.Pid); err != nil {
				t.Fatalf("the refused kill still reached the process: %v", err)
			}
		})
	}
}
