package procx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
)

// The parsers below are platform independent so every platform's format is
// tested and fuzzed on any development machine.

// maxArgs bounds the argument vector kept per process; real command lines are
// far shorter, and the bound keeps a hostile argc from allocating without end.
const maxArgs = 4096

var errMalformed = errors.New("procx: malformed process record")

// statFields is the part of /proc/<pid>/stat this package uses.
type statFields struct {
	PID       int
	Comm      string
	PPID      int
	TTYNr     int64
	StartTick int64
}

// parseStat parses a Linux /proc/<pid>/stat line. The command name sits in
// parentheses and may itself contain spaces and parentheses, so the fields
// after it are located from the last closing parenthesis.
func parseStat(data []byte) (statFields, error) {
	open := bytes.IndexByte(data, '(')
	closing := bytes.LastIndexByte(data, ')')
	if open <= 0 || closing < open {
		return statFields{}, errMalformed
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(data[:open])))
	if err != nil || pid < 0 {
		return statFields{}, errMalformed
	}
	rest := bytes.Fields(data[closing+1:])
	// state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt
	// utime stime cutime cstime priority nice num_threads itrealvalue starttime
	const startIndex = 19
	if len(rest) <= startIndex {
		return statFields{}, errMalformed
	}
	ppid, err := strconv.Atoi(string(rest[1]))
	if err != nil || ppid < 0 {
		return statFields{}, errMalformed
	}
	tty, err := strconv.ParseInt(string(rest[4]), 10, 64)
	if err != nil {
		return statFields{}, errMalformed
	}
	start, err := strconv.ParseInt(string(rest[startIndex]), 10, 64)
	if err != nil || start < 0 {
		return statFields{}, errMalformed
	}
	return statFields{
		PID:       pid,
		Comm:      string(data[open+1 : closing]),
		PPID:      ppid,
		TTYNr:     tty,
		StartTick: start,
	}, nil
}

// parseBootTime extracts "btime <seconds>" from /proc/stat.
func parseBootTime(data []byte) (int64, error) {
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		f := bytes.Fields(line)
		if len(f) == 2 && string(f[0]) == "btime" {
			v, err := strconv.ParseInt(string(f[1]), 10, 64)
			if err != nil || v < 0 {
				return 0, errMalformed
			}
			return v, nil
		}
	}
	return 0, errMalformed
}

// linuxTTY splits a Linux tty_nr (the kernel's new_encode_dev layout) into its
// major and minor numbers. ok is false for "no controlling terminal".
func linuxTTY(ttyNr int64) (major, minor uint32, ok bool) {
	if ttyNr <= 0 || ttyNr > math.MaxUint32 {
		return 0, 0, false
	}
	v := uint32(ttyNr)
	major = (v >> 8) & 0xfff
	minor = (v & 0xff) | ((v >> 12) & 0xfff00)
	return major, minor, true
}

// parseCmdline splits /proc/<pid>/cmdline (NUL-terminated arguments).
func parseCmdline(data []byte) []string {
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	if len(parts) > maxArgs {
		parts = parts[:maxArgs]
	}
	args := make([]string, len(parts))
	for i, p := range parts {
		args[i] = string(p)
	}
	return args
}

// parseProcArgs2 parses the macOS kern.procargs2 buffer: a native-endian argc,
// the executable path, NUL padding, then argc NUL-terminated arguments (the
// environment follows and is ignored).
func parseProcArgs2(data []byte) (exe string, args []string, err error) {
	if len(data) < 4 {
		return "", nil, errMalformed
	}
	n := binary.LittleEndian.Uint32(data[:4])
	if n > math.MaxInt32 {
		return "", nil, errMalformed
	}
	argc := int(n)
	rest := data[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil, errMalformed
	}
	exe = string(rest[:end])
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argc = min(argc, maxArgs)
	args = make([]string, 0, min(argc, 64))
	for range argc {
		if len(rest) == 0 {
			break
		}
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return exe, args, nil
}
