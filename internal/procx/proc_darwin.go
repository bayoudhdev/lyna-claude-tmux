package procx

import (
	"bytes"
	"errors"
	"math"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Device numbers are signed on macOS: NODEV (-1) marks a process without a
// terminal, and terminal devices (majors below 128) are never negative.

func listAll() ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	namer := newTTYNamer("/dev")
	procs := make([]Proc, 0, len(kps))
	for i := range kps {
		procs = append(procs, fromKinfo(&kps[i], namer))
	}
	return procs, nil
}

func readProc(pid int) (Proc, error) {
	// The slice form reports a PID that does not exist as an empty result;
	// the single-record form turns it into a generic EIO.
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return Proc{}, err
	}
	if len(kps) == 0 || int(kps[0].Proc.P_pid) != pid {
		return Proc{}, ErrNotFound
	}
	return fromKinfo(&kps[0], newTTYNamer("/dev")), nil
}

func fromKinfo(kp *unix.KinfoProc, namer *ttyNamer) Proc {
	comm := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	p := Proc{
		PID:   int(kp.Proc.P_pid),
		PPID:  int(kp.Eproc.Ppid),
		Start: time.Unix(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)*int64(time.Microsecond)),
		Comm:  string(comm),
	}
	if dev := kp.Eproc.Tdev; dev >= 0 {
		p.TTY = namer.name(uint64(dev))
	}
	// Arguments of other users' processes (and of zombies) are unreadable;
	// the process still belongs in the table for the ancestry walk.
	if raw, err := unix.SysctlRaw("kern.procargs2", p.PID); err == nil {
		if exe, args, err := parseProcArgs2(raw); err == nil {
			p.Exe, p.Args = exe, args
		}
	}
	return p
}

// rdev returns the device number of a device file; a negative number, which
// no terminal has, maps to a key no process looks up.
func rdev(st unix.Stat_t) uint64 {
	if st.Rdev < 0 {
		return math.MaxUint64
	}
	return uint64(st.Rdev)
}

type osSignaler struct{}

// signalVerified cannot pin the process on macOS (there is no process
// descriptor); the window between the re-read and the signal is a few
// microseconds, against PID reuse that needs the whole PID space to wrap.
func (osSignaler) signalVerified(pid int, sig syscall.Signal, verify func(Proc) error) error {
	p, err := readProc(pid)
	if err != nil {
		return err
	}
	if err := verify(p); err != nil {
		return err
	}
	if err := unix.Kill(pid, sig); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrNotFound
		}
		return err
	}
	return nil
}
