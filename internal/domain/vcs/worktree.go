// Package vcs is the git a workspace reads, parsed and modeled: the status of
// a working tree and the files changed in it, the worktrees of a project and
// its branches. Nothing here runs a command or touches a file: bytes in,
// values out.
package vcs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrMalformed reports git output that does not follow the documented format.
var ErrMalformed = errors.New("vcs: malformed git output")

// MaxWorktrees bounds one reading of the worktrees of a project. A project
// with more than this is reported up to the cap rather than refused: the
// reading is drawn, and a list that long is already past what a view shows.
const MaxWorktrees = 512

// Worktree is one entry of `git worktree list --porcelain -z`.
type Worktree struct {
	// Path is the working tree, as git prints it: absolute, and untrusted
	// display data like every path here (sanitize before drawing).
	Path string
	// Head is the commit checked out there, empty for a worktree whose branch
	// has no commit yet.
	Head string
	// Branch is the full ref of the branch checked out ("refs/heads/task-a"),
	// empty when HEAD is detached or the entry is bare.
	Branch string
	// Bare marks the bare repository a worktree list starts with when the
	// project has no working tree of its own.
	Bare bool
	// Detached marks a worktree on a commit rather than on a branch.
	Detached bool
	// Locked marks a worktree git refuses to remove, with the reason given
	// when it was locked, which is empty for a lock with no reason.
	Locked     bool
	LockReason string
	// Prunable marks a worktree git no longer finds where it was registered,
	// with the reason it says so.
	Prunable    bool
	PruneReason string
}

// Name is what the worktree is called: its branch without the ref prefix, and
// for a detached or bare one the last element of its path. It is the name the
// views show and the one `lmux worktree` takes.
func (w Worktree) Name() string {
	if w.Branch != "" {
		return strings.TrimPrefix(w.Branch, headsPrefix)
	}
	path := strings.TrimRight(w.Path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// headsPrefix is the ref namespace of a branch.
const headsPrefix = "refs/heads/"

// ParseWorktrees reads `git worktree list --porcelain -z`: one attribute per
// NUL-terminated field, an empty field between two worktrees. An attribute
// this does not know is skipped rather than refused, so a newer git that adds
// one keeps being read.
func ParseWorktrees(data []byte) ([]Worktree, error) {
	var (
		list    []Worktree
		current Worktree
		open    bool
	)
	end := func() {
		if !open {
			return
		}
		if len(list) < MaxWorktrees {
			list = append(list, current)
		}
		current, open = Worktree{}, false
	}
	for _, field := range splitNUL(data) {
		if field == "" {
			end()
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			end()
			if value == "" {
				return nil, fmt.Errorf("%w: worktree with no path", ErrMalformed)
			}
			current, open = Worktree{Path: value}, true
			continue
		}
		if !open {
			return nil, fmt.Errorf("%w: %q before any worktree", ErrMalformed, key)
		}
		switch key {
		case "HEAD":
			if !isHex(value) {
				return nil, fmt.Errorf("%w: worktree head %q", ErrMalformed, value)
			}
			current.Head = value
		case "branch":
			// A ref that ends where its namespace does names nothing, and a
			// worktree of no name is one no view can show or act on.
			if value == "" || strings.HasSuffix(value, "/") {
				return nil, fmt.Errorf("%w: worktree branch ref %q", ErrMalformed, value)
			}
			current.Branch = value
		case "bare":
			current.Bare = true
		case "detached":
			current.Detached = true
		case "locked":
			current.Locked, current.LockReason = true, value
		case "prunable":
			current.Prunable, current.PruneReason = true, value
		}
	}
	end()
	return list, nil
}

// ValidateWorktreeName checks the name a workspace gives a worktree: 1 to 64
// characters of letters, digits, '.', '_' and '-', not starting with '-' or
// '.', and no "..". The name becomes a directory and a branch, so anything a
// path or a command line would read as something else is refused rather than
// cleaned.
func ValidateWorktreeName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("worktree name must be 1 to 64 characters, got %q", name)
	}
	if name[0] == '-' || name[0] == '.' {
		return fmt.Errorf("worktree name %q must not start with '-' or '.'", name)
	}
	for i := range len(name) {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("worktree name %q contains %q (allowed: letters, digits, '.', '_', '-')", name, c)
		}
		if c == '.' && i+1 < len(name) && name[i+1] == '.' {
			return fmt.Errorf("worktree name %q must not contain \"..\"", name)
		}
	}
	return nil
}

// WorktreeDir is where the worktrees of a project live, under the top of the
// working tree. It is the directory a Claude launch uses as well, so a
// worktree opened here is the same one an agent is started in.
const WorktreeDir = ".claude/worktrees"

// WorktreePath is where the worktree of that name belongs in the project
// rooted at root. A name that is not one is refused rather than cleaned, so
// no path is ever built outside the project.
func WorktreePath(root, name string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: project root %q is not an absolute path", ErrMalformed, root)
	}
	if err := ValidateWorktreeName(name); err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(WorktreeDir), name), nil
}
