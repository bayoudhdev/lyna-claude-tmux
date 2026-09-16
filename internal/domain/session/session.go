// Package session holds the naming rules for workspaces: session names,
// popup session names compatible with the per-directory scheme used by the
// upstream hatch plugin, and project root resolution.
package session

import (
	"crypto/md5" //nolint:gosec // Not a security use: matches the upstream per-directory session naming.
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// MaxNameLen bounds session names so status bars and pickers stay aligned.
	MaxNameLen = 48
	// DefaultName is used when a directory name has no usable characters.
	DefaultName = "workspace"
	// DefaultPopupPrefix is the upstream prefix for per-directory popup sessions.
	DefaultPopupPrefix = "claude-"
	// PopupHashLen is the length of the directory hash in a popup session name.
	PopupHashLen = 8
)

// ErrInvalidName reports a session name outside the allowed alphabet.
var ErrInvalidName = errors.New("invalid session name")

// Sanitize maps an arbitrary label (usually a directory name) to a session
// name made of [A-Za-z0-9_-]. Dots and colons are excluded because tmux
// targets use them as window and pane separators. Runs of replaced characters
// collapse to one '-', leading and trailing separators are trimmed, and the
// result never starts with '-' so it cannot be read as a flag.
func Sanitize(label string) string {
	var b strings.Builder
	lastDash := true // suppresses leading separators
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= MaxNameLen {
			break
		}
	}
	name := strings.Trim(b.String(), "-_")
	if len(name) > MaxNameLen {
		name = strings.TrimRight(name[:MaxNameLen], "-_")
	}
	if name == "" {
		return DefaultName
	}
	return name
}

// Validate reports whether name is usable as is: non-empty, at most
// MaxNameLen bytes, only [A-Za-z0-9_-], not starting with '-'.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidName)
	}
	if len(name) > MaxNameLen {
		return fmt.Errorf("%w: %q is longer than %d characters", ErrInvalidName, name, MaxNameLen)
	}
	if name[0] == '-' {
		return fmt.Errorf("%w: %q starts with '-'", ErrInvalidName, name)
	}
	for i := range len(name) {
		if c := name[i]; !isNameByte(c) {
			return fmt.Errorf("%w: %q contains %q (allowed: letters, digits, '_' and '-')", ErrInvalidName, name, c)
		}
	}
	return nil
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// Unique returns base, or base with the smallest "-N" suffix (N >= 2) for
// which taken reports false. The suffix replaces trailing characters when
// base is already at MaxNameLen.
func Unique(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		stem := base
		if len(stem)+len(suffix) > MaxNameLen {
			stem = strings.TrimRight(stem[:MaxNameLen-len(suffix)], "-_")
		}
		if candidate := stem + suffix; !taken(candidate) {
			return candidate
		}
	}
}

// PathHash returns the first 8 hex digits of md5(path + "\n"), the digest the
// upstream shell plugin gets from `printf '%s\n' "$path" | md5sum`. Keeping it
// byte-identical lets popup sessions created by either tool be found by both.
func PathHash(path string) string {
	sum := md5.Sum([]byte(path + "\n")) //nolint:gosec // Naming only, see import comment.
	return hex.EncodeToString(sum[:])[:PopupHashLen]
}

// PopupName returns the popup session name for a directory.
func PopupName(prefix, path string) string {
	if prefix == "" {
		prefix = DefaultPopupPrefix
	}
	return prefix + PathHash(path)
}

// IsPopup reports whether a session name belongs to a popup session: the
// prefix followed by the lowercase hex directory hash. The match is exact so
// that a workspace named after a project directory that starts with the
// prefix, such as claude-api, is left alone.
func IsPopup(prefix, name string) bool {
	if prefix == "" {
		prefix = DefaultPopupPrefix
	}
	hash, ok := strings.CutPrefix(name, prefix)
	if !ok || len(hash) != PopupHashLen {
		return false
	}
	for i := range len(hash) {
		if c := hash[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ProjectRoot returns the nearest ancestor of dir (dir included) that holds a
// .git entry, directory or file (worktrees and submodules use a file). When no
// repository is found it returns dir itself. dir is made absolute and cleaned
// first; symlinks are kept as the user typed them so session paths match the
// shell's working directory.
func ProjectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("resolve %q: not a directory", dir)
	}
	for cur := abs; ; {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		cur = parent
	}
}
