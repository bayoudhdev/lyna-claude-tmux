package procx

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// userHZ is the clock tick rate of /proc start times. It is a fixed userspace
// ABI value; identity comparisons stay exact even on a kernel that differs,
// because both sides of a comparison use the same conversion.
const userHZ = 100

// maxProcFile bounds reads of /proc files (cmdline can be long, never huge).
const maxProcFile = 1 << 20

// ptsMajorFirst and ptsMajorLast bound the Unix98 pseudo-terminal majors.
const (
	ptsMajorFirst = 136
	ptsMajorLast  = 143
)

var bootTime = sync.OnceValues(func() (int64, error) {
	data, err := fsx.ReadFileLimited("/proc/stat", maxProcFile)
	if err != nil {
		return 0, err
	}
	return parseBootTime(data)
})

func listAll() ([]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	namer := newTTYNamer("/dev/pts", "/dev")
	procs := make([]Proc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := readLinux(pid, namer)
		if err != nil {
			// Processes exit between the directory read and the stat read.
			continue
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func readProc(pid int) (Proc, error) {
	return readLinux(pid, newTTYNamer("/dev/pts", "/dev"))
}

func readLinux(pid int, namer *ttyNamer) (Proc, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	data, err := fsx.ReadFileLimited(dir+"/stat", maxProcFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			return Proc{}, ErrNotFound
		}
		return Proc{}, err
	}
	st, err := parseStat(data)
	if err != nil {
		return Proc{}, err
	}
	boot, err := bootTime()
	if err != nil {
		return Proc{}, err
	}
	p := Proc{
		PID:   st.PID,
		PPID:  st.PPID,
		Start: time.Unix(boot, 0).Add(time.Duration(st.StartTick) * (time.Second / userHZ)),
		Comm:  st.Comm,
	}
	if major, minor, ok := linuxTTY(st.TTYNr); ok {
		if major >= ptsMajorFirst && major <= ptsMajorLast {
			p.TTY = "/dev/pts/" + strconv.FormatUint(uint64(major-ptsMajorFirst)<<8|uint64(minor), 10)
		} else {
			p.TTY = namer.name(unix.Mkdev(major, minor))
		}
	}
	if exe, err := os.Readlink(dir + "/exe"); err == nil {
		p.Exe = exe
	}
	if cmd, err := fsx.ReadFileLimited(dir+"/cmdline", maxProcFile); err == nil {
		p.Args = parseCmdline(cmd)
	}
	return p, nil
}

func rdev(st unix.Stat_t) uint64 { return st.Rdev }

type osSignaler struct{}

// signalVerified pins the process with a pidfd before reading it, so the
// process that is verified is exactly the one signaled. Kernels without pidfd
// support (before 5.3) fall back to a plain re-read and kill.
func (osSignaler) signalVerified(pid int, sig syscall.Signal, verify func(Proc) error) error {
	fd, err := unix.PidfdOpen(pid, 0)
	switch {
	case err == nil:
		defer func() { _ = unix.Close(fd) }()
	case errors.Is(err, unix.ESRCH):
		return ErrNotFound
	case errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM):
		fd = -1
	default:
		return err
	}
	p, err := readProc(pid)
	if err != nil {
		return err
	}
	if err := verify(p); err != nil {
		return err
	}
	if fd >= 0 {
		err = unix.PidfdSendSignal(fd, sig, nil, 0)
	} else {
		err = unix.Kill(pid, sig)
	}
	if errors.Is(err, unix.ESRCH) {
		return ErrNotFound
	}
	return err
}
