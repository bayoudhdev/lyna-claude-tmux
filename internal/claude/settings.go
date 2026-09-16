package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// tempSettingsName matches the temporary file fsx.WriteFileAtomic leaves
// behind when a write is interrupted before its rename.
var tempSettingsName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^\.[0-9a-f]{16}\.json\.tmp-[0-9]+$`) })

// WriteSettings stores a generated settings document in settingsDir under
// its content-addressed name and returns the file path. The directory is
// created with mode 0700 and the file written atomically with mode 0600; a
// symbolic link at either is refused. Writing the same document again returns
// the same path and refreshes its modification time, which PruneSettings uses
// to tell files in use from stale ones.
func WriteSettings(settingsDir string, data []byte) (string, error) {
	if !filepath.IsAbs(settingsDir) {
		return "", fmt.Errorf("claude: settings directory must be absolute (got %q)", settingsDir)
	}
	if !json.Valid(data) {
		return "", errors.New("claude: refusing to write a settings file that is not valid JSON")
	}
	if err := fsx.EnsurePrivateDir(settingsDir); err != nil {
		return "", fmt.Errorf("claude: settings directory: %w", err)
	}
	path := filepath.Join(settingsDir, claudecfg.SettingsFileName(data))
	if err := fsx.WriteFileAtomic(path, data, fsx.PrivateFile); err != nil {
		return "", fmt.Errorf("claude: write settings: %w", err)
	}
	return path, nil
}

// PruneOptions select which generated settings files PruneSettings removes.
type PruneOptions struct {
	// Keep lists settings files still in use, as paths or base names.
	Keep []string
	// MaxAge, when positive, removes only files not written for longer than
	// this. Zero removes every generated file not in Keep.
	MaxAge time.Duration
	// Now is the reference time for MaxAge; zero uses time.Now.
	Now time.Time
}

// PruneSettings removes stale generated files from settingsDir and returns
// the removed base names, sorted. Only regular files whose names match the
// content-addressed pattern (and leftover temporary files from interrupted
// writes, by age only) are considered; links, directories and anything else
// are never touched or followed. A missing directory prunes nothing.
//
// At least one of Keep or MaxAge must be set, so a caller cannot remove the
// settings of running sessions by passing empty options.
func PruneSettings(settingsDir string, opts PruneOptions) ([]string, error) {
	if !filepath.IsAbs(settingsDir) {
		return nil, fmt.Errorf("claude: settings directory must be absolute (got %q)", settingsDir)
	}
	if len(opts.Keep) == 0 && opts.MaxAge <= 0 {
		return nil, errors.New("claude: prune needs a keep list or a maximum age")
	}
	info, err := os.Lstat(settingsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("claude: settings directory: %w", err)
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("claude: settings directory %s: %w", settingsDir, fsx.ErrSymlink)
	case !info.IsDir():
		return nil, fmt.Errorf("claude: settings directory %s: %w", settingsDir, fsx.ErrNotDir)
	}
	entries, err := os.ReadDir(settingsDir)
	if err != nil {
		return nil, fmt.Errorf("claude: read settings directory: %w", err)
	}
	keep := make(map[string]bool, len(opts.Keep))
	for _, k := range opts.Keep {
		keep[filepath.Base(k)] = true
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	var removed []string
	var errs []error
	for _, e := range entries {
		name := e.Name()
		generated := claudecfg.IsSettingsFileName(name)
		temp := tempSettingsName().MatchString(name)
		// DirEntry types come from lstat: a link is never a regular file here.
		if !e.Type().IsRegular() || keep[name] || (!generated && !temp) || (temp && opts.MaxAge <= 0) {
			continue
		}
		if opts.MaxAge > 0 {
			fi, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if now.Sub(fi.ModTime()) <= opts.MaxAge {
				continue
			}
		}
		if err := os.Remove(filepath.Join(settingsDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, name)
	}
	slices.Sort(removed)
	if len(errs) > 0 {
		return removed, fmt.Errorf("claude: prune settings: %w", errors.Join(errs...))
	}
	return removed, nil
}
