// Package claude is the adapter around the Claude Code executable: locating
// it, reading its version and running agents, and storing the per-launch
// settings files lyna-tmux generates.
package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// InstallGuidance tells a user how to get Claude Code.
const InstallGuidance = "install Claude Code with `curl -fsSL https://claude.ai/install.sh | bash` " +
	"(see https://code.claude.com/docs/en/setup), or set claude.command in the lyna-tmux config"

// ErrNotFound matches every NotFoundError.
var ErrNotFound = errors.New("claude: Claude Code executable not found")

// NotFoundError reports where the executable was looked for.
type NotFoundError struct {
	// Command is the name or path that was resolved.
	Command string
	// Searched lists the absolute paths checked after PATH, in order.
	Searched []string
}

// Error implements error with the install guidance.
func (e *NotFoundError) Error() string {
	var b strings.Builder
	b.WriteString("claude: Claude Code executable not found")
	if e.Command != "" && e.Command != "claude" {
		b.WriteString(" at " + e.Command)
	} else {
		b.WriteString(" on PATH")
		if len(e.Searched) > 0 {
			b.WriteString(" or in " + strings.Join(e.Searched, ", "))
		}
	}
	b.WriteString("; " + InstallGuidance)
	return b.String()
}

// Is makes errors.Is(err, ErrNotFound) true.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// LookPathFunc resolves a command name on PATH, as exec.LookPath does.
type LookPathFunc func(file string) (string, error)

// ExistsFunc reports whether an absolute path is an executable file.
type ExistsFunc func(path string) bool

// FallbackPaths are the install locations checked when claude is not on PATH:
// the native installer's launcher, then the older per-user local install
// under the Claude configuration directory (CLAUDE_CONFIG_DIR or ~/.claude).
func FallbackPaths(getenv func(string) string, home string) []string {
	var out []string
	if filepath.IsAbs(home) {
		out = append(out, filepath.Join(home, ".local", "bin", "claude"))
	}
	if claudeHome := xdg.ClaudeHome(getenv, home); filepath.IsAbs(claudeHome) {
		out = append(out, filepath.Join(claudeHome, "local", "claude"))
	}
	return out
}

// Find locates the claude executable: PATH first, then FallbackPaths. The
// result is always absolute; a PATH hit relative to the working directory is
// ignored, since a checkout could plant one.
func Find(lookPath LookPathFunc, getenv func(string) string, home string, exists ExistsFunc) (string, error) {
	if p, err := lookPath("claude"); err == nil && filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	searched := FallbackPaths(getenv, home)
	for _, p := range searched {
		if exists(p) {
			return p, nil
		}
	}
	return "", &NotFoundError{Command: "claude", Searched: searched}
}

// ResolveCommand resolves a configured claude.command. A bare name is looked
// up on PATH; a path must be absolute or start with "~/" and name an
// executable file. An empty command falls back to Find.
func ResolveCommand(command string, lookPath LookPathFunc, getenv func(string) string, home string, exists ExistsFunc) (string, error) {
	switch {
	case command == "" || command == "claude":
		return Find(lookPath, getenv, home, exists)
	case strings.HasPrefix(command, "~/"):
		if !filepath.IsAbs(home) {
			return "", &NotFoundError{Command: command}
		}
		command = filepath.Join(home, command[2:])
	case !strings.Contains(command, "/"):
		p, err := lookPath(command)
		if err != nil || !filepath.IsAbs(p) {
			return "", &NotFoundError{Command: command}
		}
		return filepath.Clean(p), nil
	case !filepath.IsAbs(command):
		return "", &NotFoundError{Command: command}
	}
	command = filepath.Clean(command)
	if !exists(command) {
		return "", &NotFoundError{Command: command}
	}
	return command, nil
}

// IsExecutable reports whether path, following links, is a regular file with
// an execute bit. It is the ExistsFunc used outside tests.
func IsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
