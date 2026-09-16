package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

var (
	// ErrConfigExists is returned by InitConfig when a configuration file is
	// already present and replacing it was not requested.
	ErrConfigExists = errors.New("configuration file already exists")
	// ErrConfigSymlink is returned when the configuration file to replace is a
	// symbolic link: the file it points to belongs to the user.
	ErrConfigSymlink = errors.New("configuration file is a symbolic link")
)

// defaultEditor is used when neither VISUAL nor EDITOR is set.
const defaultEditor = "vi"

// ConfigInit is the outcome of InitConfig.
type ConfigInit struct {
	Path string
	// Backup is the copy of the replaced file, empty when nothing was replaced.
	Backup string
}

// InitConfig writes the documented configuration template. An existing file
// is kept unless force is set; a replaced file is first copied next to it with
// a .bak suffix.
func InitConfig(h Host, force bool) (ConfigInit, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return ConfigInit{}, err
	}
	res := ConfigInit{Path: paths.ConfigFile()}
	info, err := os.Lstat(res.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return res, fmt.Errorf("stat %s: %w", res.Path, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return res, fmt.Errorf("%s: %w; edit the file it points to", res.Path, ErrConfigSymlink)
	case !force:
		return res, fmt.Errorf("%s: %w; pass --force to replace it (a backup is kept)", res.Path, ErrConfigExists)
	default:
		old, err := fsx.ReadFileNoFollow(res.Path, config.MaxFileSize)
		if err != nil {
			return res, fmt.Errorf("read %s: %w", res.Path, err)
		}
		res.Backup = res.Path + ".bak"
		if err := fsx.WriteFileAtomic(res.Backup, old, fsx.PrivateFile); err != nil {
			return ConfigInit{Path: res.Path}, fmt.Errorf("back up %s: %w", res.Path, err)
		}
	}
	if err := fsx.EnsurePrivateDir(filepath.Dir(res.Path)); err != nil {
		return res, err
	}
	if err := fsx.WriteFileAtomic(res.Path, config.Template(), fsx.PrivateFile); err != nil {
		return res, fmt.Errorf("write %s: %w", res.Path, err)
	}
	return res, nil
}

// EditorArgv returns the command that opens path in the user's editor. VISUAL
// wins over EDITOR, as for other terminal tools. The editor value may carry
// arguments, so it runs through the shell; the path is passed as a separate
// argument and is never parsed by the shell.
func EditorArgv(getenv func(string) string, path string) []string {
	editor := getenv("VISUAL")
	if editor == "" {
		editor = getenv("EDITOR")
	}
	if editor == "" {
		editor = defaultEditor
	}
	return []string{"/bin/sh", "-c", editor + ` "$@"`, editor, path}
}
