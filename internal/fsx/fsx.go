// Package fsx holds the file primitives lyna-tmux uses for everything it owns on
// disk: private directories, atomic writes, bounded reads and a size-capped log.
//
// State files are never followed through symbolic links. A link planted in the
// state directory by another local user or a malicious checkout would otherwise
// redirect a write to an arbitrary file owned by the victim.
package fsx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Permission bits for everything lyna-tmux creates.
const (
	PrivateDir  fs.FileMode = 0o700
	PrivateFile fs.FileMode = 0o600
)

var (
	// ErrSymlink is returned when a path that must be a regular file or directory is a link.
	ErrSymlink = errors.New("fsx: refusing to follow symbolic link")
	// ErrNotDir is returned when a directory path exists as something else.
	ErrNotDir = errors.New("fsx: not a directory")
	// ErrTooLarge is returned when a bounded read exceeds its limit.
	ErrTooLarge = errors.New("fsx: input exceeds size limit")
)

// EnsurePrivateDir creates dir (and missing parents) with mode 0700. An existing
// final component must be a real directory; group and other permission bits on
// it are removed. The directory the missing components are created in, and
// every component this call creates, are checked as well, so a link planted
// where lyna-tmux is about to create is refused rather than followed.
func EnsurePrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	// ENOTDIR reports a component above dir that is not a directory; the
	// creation walk names it instead of reporting the whole path as unstatable.
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		if err := mkdirPrivate(dir); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s: %w", dir, ErrSymlink)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %w", dir, ErrNotDir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, info.Mode().Perm()&PrivateDir); err != nil {
			return fmt.Errorf("restrict %s: %w", dir, err)
		}
	}
	return nil
}

// mkdirPrivate creates dir and its missing parents one component at a time,
// each with mode 0700, checking the directory it creates them in and every
// component it creates. os.MkdirAll checks neither: another local user who
// plants a symbolic link where lyna-tmux is about to create, in a shared
// directory such as the one LYNA_TMUX_HOME may name, would have the private
// state created through that link and then written into a directory they own.
//
// Components that already exist above the one being created are left alone:
// they were there before lyna-tmux, and a home directory reached through a
// link is a normal dotfiles layout, not a substitution.
func mkdirPrivate(dir string) error {
	var missing []string
	for p := filepath.Clean(dir); ; {
		info, err := os.Lstat(p)
		if err == nil {
			// Whatever this is decides where everything below it lands.
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%s: %w", p, ErrSymlink)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s: %w", p, ErrNotDir)
			}
			break
		}
		// ENOTDIR means a component above p is a regular file, which the
		// parent of the walk reports as itself; keep climbing to name it.
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		p := missing[i]
		if err := os.Mkdir(p, PrivateDir); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", p, err)
		}
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", p, ErrSymlink)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: %w", p, ErrNotDir)
		}
	}
	return nil
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory, fsyncs it and renames it over the target. Readers observe either the
// old or the new content, never a truncated file. A symbolic link at path is refused.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", path, ErrSymlink)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, statErr)
	}

	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp for %s: %w", path, err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp for %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", path, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp to %s: %w", path, err)
	}
	return nil
}

// ReadLimited reads r to the end, failing with ErrTooLarge past limit bytes.
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// ReadFileNoFollow reads a regular file without following a final symbolic link,
// bounded by limit bytes.
func ReadFileNoFollow(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if isLoop(err) {
			return nil, fmt.Errorf("%s: %w", path, ErrSymlink)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	return ReadLimited(f, limit)
}

// ReadFileLimited reads a file, following links, bounded by limit bytes. It is
// meant for user-owned inputs such as config.toml, which dotfile managers link.
func ReadFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadLimited(f, limit)
}

// AppendCapped appends line to a private log file. When the file already holds
// more than maxBytes it is rotated to path+".1" first, so the log never grows
// without bound. The final newline is added when missing.
func AppendCapped(path string, line []byte, maxBytes int64) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", path, ErrSymlink)
		}
		if info.Size() > maxBytes {
			if err := os.Rename(path, path+".1"); err != nil {
				return fmt.Errorf("rotate %s: %w", path, err)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, PrivateFile)
	if err != nil {
		if isLoop(err) {
			return fmt.Errorf("%s: %w", path, ErrSymlink)
		}
		return err
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line[:len(line):len(line)], '\n')
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	return errors.Join(werr, cerr)
}

func isLoop(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
