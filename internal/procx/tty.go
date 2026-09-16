//go:build darwin || linux

package procx

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// ttyNamer maps terminal device numbers to /dev paths. The kernel reports a
// controlling terminal as a device number only; the directory scan runs at
// most once per snapshot and only when some process has a terminal.
type ttyNamer struct {
	dirs  []string
	once  sync.Once
	names map[uint64]string
}

func newTTYNamer(dirs ...string) *ttyNamer {
	return &ttyNamer{dirs: dirs}
}

func (n *ttyNamer) name(dev uint64) string {
	n.once.Do(func() { n.names = scanTTYs(n.dirs) })
	return n.names[dev]
}

// scanTTYs lists character devices that are terminals: tty* and console
// entries of a directory, and every entry of a pts directory.
func scanTTYs(dirs []string) map[uint64]string {
	names := make(map[uint64]string)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		pts := filepath.Base(dir) == "pts"
		for _, e := range entries {
			name := e.Name()
			if !pts && !strings.HasPrefix(name, "tty") && name != "console" {
				continue
			}
			path := filepath.Join(dir, name)
			var st unix.Stat_t
			if err := unix.Lstat(path, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR {
				continue
			}
			dev := rdev(st)
			if _, dup := names[dev]; !dup {
				names[dev] = path
			}
		}
	}
	return names
}
