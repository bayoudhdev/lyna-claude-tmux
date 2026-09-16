// Package procx reads the operating system process table: parent links for
// the agent join, the identity of a process (PID plus start time) and whether
// it is a Claude Code process. Kill signals a process only after re-reading it
// and confirming it is still the same Claude process, so a recycled PID never
// receives a signal meant for an agent that already exited.
//
// "Is a Claude Code process" is decided from what the kernel reports, the
// executable path and the command name, not from the argument vector a process
// and its parent choose (IsClaude).
package procx

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
)

var (
	// ErrNotFound reports a process that does not exist (any more).
	ErrNotFound = errors.New("procx: no such process")
	// ErrIdentityChanged reports a PID now held by another process than the
	// one the caller listed: its start time differs.
	ErrIdentityChanged = errors.New("procx: process identity changed (pid reused)")
	// ErrNotClaude reports a process that is not a Claude Code process.
	ErrNotClaude = errors.New("procx: process is not claude")
	// ErrInvalidPID reports a PID that must never be signaled: 0 and negative
	// values address process groups, 1 is init or launchd.
	ErrInvalidPID = errors.New("procx: invalid pid")
	// ErrUnsupported reports a platform without a process table reader.
	ErrUnsupported = errors.New("procx: process table not supported on this platform")
)

// Proc is one process.
type Proc struct {
	PID  int
	PPID int
	// Start is when the process started. PID and Start together identify a
	// process across PID reuse. Compare it only with values this package
	// produced: the kernel's precision differs per platform (microseconds on
	// macOS, clock ticks on Linux).
	Start time.Time
	// Comm is the kernel's short command name (truncated by the kernel).
	Comm string
	// Exe is the executable path when readable, else "". On macOS it is the
	// path the process was started with; on Linux the resolved binary.
	Exe string
	// Args is the argument vector when readable (processes of the same user),
	// else nil.
	Args []string
	// TTY is the controlling terminal device path such as /dev/ttys003 or
	// /dev/pts/4, "" when the process has none or it cannot be named.
	TTY string
}

// Name returns the executable base name: from Exe when known, else Comm.
func (p Proc) Name() string {
	if p.Exe != "" {
		return filepath.Base(p.Exe)
	}
	return p.Comm
}

// Table is a snapshot of the process table indexed by PID.
type Table struct {
	procs map[int]Proc
}

// NewTable builds a table from processes; a later duplicate PID wins.
func NewTable(procs []Proc) Table {
	t := Table{procs: make(map[int]Proc, len(procs))}
	for _, p := range procs {
		t.procs[p.PID] = p
	}
	return t
}

// Snapshot reads every process visible to the current user.
func Snapshot() (Table, error) {
	procs, err := listAll()
	if err != nil {
		return Table{}, err
	}
	return NewTable(procs), nil
}

// Read reads one process.
func Read(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, fmt.Errorf("read pid %d: %w", pid, ErrInvalidPID)
	}
	return readProc(pid)
}

// Len returns the number of processes.
func (t Table) Len() int { return len(t.procs) }

// Lookup returns the process with this PID.
func (t Table) Lookup(pid int) (Proc, bool) {
	p, ok := t.procs[pid]
	return p, ok
}

// Parents adapts the table to the agent join's ancestry function.
func (t Table) Parents() agent.Parents {
	return func(pid int) (int, bool) {
		p, ok := t.procs[pid]
		if !ok {
			return 0, false
		}
		return p.PPID, true
	}
}

// IsClaude reports whether a process is Claude Code: the native binary (run
// through its "claude" link or from the installer's versions directory), or a
// JavaScript runtime executing the claude package entry point. MCP servers and
// plugins that merely live under a ".claude" directory are not Claude.
//
// The answer rests on Exe and Comm, which the kernel fills in at exec. A
// process does not choose either, while its parent chooses argv[0] freely: a
// program started with argv[0] = "/anywhere/claude" is not Claude Code, and
// Kill would otherwise accept it as one. Args are read only to name the script
// a runtime the kernel identified is running, and argv[0] counts only where
// the kernel gave no executable path and it agrees with the command name.
func IsClaude(p Proc) bool {
	if p.Comm == claudeName || baseName(p.Exe) == claudeName || nativeInstall(p.Exe) {
		return true
	}
	if len(p.Args) == 0 {
		return false
	}
	if p.Exe == "" && baseName(p.Args[0]) == p.Comm && nativeInstall(p.Args[0]) {
		return true
	}
	if !scriptRuntime(baseName(p.Exe)) && !scriptRuntime(p.Comm) {
		return false
	}
	for _, a := range p.Args[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return claudeScript(a)
	}
	return false
}

const claudeName = "claude"

// baseName is filepath.Base without its "." result for an empty path.
func baseName(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

// nativeInstall matches the native installer layout
// <prefix>/claude/versions/<version>, which is what the kernel reports as the
// executable once the "claude" link is resolved.
func nativeInstall(path string) bool {
	if path == "" || !strings.Contains(path, "/") {
		return false
	}
	dir := filepath.Dir(path)
	return filepath.Base(dir) == "versions" && filepath.Base(filepath.Dir(dir)) == claudeName
}

func scriptRuntime(name string) bool {
	switch name {
	case "node", "nodejs", "bun", "deno":
		return true
	}
	return false
}

// claudeScript matches the claude launcher script or the npm package entry
// point (@anthropic-ai/claude-code/cli.js).
func claudeScript(path string) bool {
	if baseName(path) == claudeName {
		return true
	}
	path = filepath.ToSlash(path)
	for _, entry := range []string{"cli.js", "cli.mjs"} {
		if strings.HasSuffix(path, "/claude-code/"+entry) {
			return true
		}
	}
	return false
}

// Kill sends sig to pid after re-reading the process: it refuses when the
// process is gone, when its start time differs from expectedStart (the PID was
// reused) and when it is no longer a Claude process. On Linux the check and the
// signal go through one pidfd, which closes the reuse window entirely.
func Kill(pid int, expectedStart time.Time, sig syscall.Signal) error {
	return kill(osSignaler{}, pid, expectedStart, sig)
}

// signaler abstracts the platform: pin the process, verify it through the
// pin, then signal through the pin.
type signaler interface {
	signalVerified(pid int, sig syscall.Signal, verify func(Proc) error) error
}

func kill(s signaler, pid int, expectedStart time.Time, sig syscall.Signal) error {
	if pid <= 1 {
		return fmt.Errorf("kill %d: %w", pid, ErrInvalidPID)
	}
	err := s.signalVerified(pid, sig, func(p Proc) error {
		if !p.Start.Equal(expectedStart) {
			return ErrIdentityChanged
		}
		if !IsClaude(p) {
			return ErrNotClaude
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	return nil
}
