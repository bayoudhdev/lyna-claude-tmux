package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// SetupSaved is the outcome of SaveSetup.
type SetupSaved struct {
	Path string
	// Backup is the copy of the replaced file, empty when there was none.
	Backup string
}

// SetupTarget returns the configuration file the setup wizard writes. A file
// that is a symbolic link is refused, before any question is asked: the file
// it points to belongs to the user and is edited in place.
func SetupTarget(h Host) (string, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return "", err
	}
	path := paths.ConfigFile()
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return path, nil
	case err != nil:
		return path, fmt.Errorf("stat %s: %w", path, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return path, fmt.Errorf("%s: %w; edit the file it points to with lmux config edit", path, ErrConfigSymlink)
	case !info.Mode().IsRegular():
		return path, fmt.Errorf("%s is not a regular file", path)
	}
	return path, nil
}

// SaveSetup validates a configuration and writes it as the configuration
// file. An existing file is first copied next to it with a .bak suffix.
func SaveSetup(h Host, cfg config.Config) (SetupSaved, error) {
	if err := cfg.Validate(); err != nil {
		return SetupSaved{}, err
	}
	data, err := config.Marshal(cfg)
	if err != nil {
		return SetupSaved{}, err
	}
	path, err := SetupTarget(h)
	if err != nil {
		return SetupSaved{}, err
	}
	res := SetupSaved{Path: path}
	old, err := fsx.ReadFileNoFollow(path, config.MaxFileSize)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return SetupSaved{}, fmt.Errorf("read %s: %w", path, err)
	default:
		res.Backup = path + ".bak"
		if err := fsx.WriteFileAtomic(res.Backup, old, fsx.PrivateFile); err != nil {
			return SetupSaved{}, fmt.Errorf("back up %s: %w", path, err)
		}
	}
	if err := fsx.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return SetupSaved{}, err
	}
	if err := fsx.WriteFileAtomic(path, data, fsx.PrivateFile); err != nil {
		return SetupSaved{}, fmt.Errorf("write %s: %w", path, err)
	}
	return res, nil
}

// ApplySetup regenerates the tmux configuration from the saved file and
// makes a running lyna-tmux server load it. It reports whether a server was
// running; a stopped server reads the new file when it next starts.
func ApplySetup(ctx context.Context, h Host) (running bool, err error) {
	s, err := OpenServer(ctx, h)
	if err != nil {
		return false, err
	}
	return s.Sync(ctx)
}
