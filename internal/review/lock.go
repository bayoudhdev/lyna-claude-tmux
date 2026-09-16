package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// lockName is the advisory lock file serializing installs into one directory.
const lockName = ".install.lock"

// lockDir takes an exclusive advisory lock on dir without waiting: two
// concurrent installs would otherwise swap directories under each other.
// The lock is released when the returned function runs or the process exits.
func lockDir(dir string) (func(), error) {
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, fsx.PrivateFile)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, fmt.Errorf("%s: %w", path, fsx.ErrSymlink)
		}
		return nil, fmt.Errorf("review: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("review: lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
