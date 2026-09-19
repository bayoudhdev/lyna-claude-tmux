// Package vcs is the git a workspace reads, parsed and modeled: the status of
// a working tree and the files changed in it, the worktrees of a project and
// its branches. Nothing here runs a command or touches a file: bytes in,
// values out.
package vcs

import (
	"errors"
	"fmt"
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
