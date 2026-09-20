package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// maxMessage caps a commit message. A message is typed, not generated; a
// megabyte of it is a mistake, and the file it is written to is read back by
// git itself.
const maxMessage = 1 << 20

// CommitOptions describes a commit to write.
type CommitOptions struct {
	// Message is the message of the commit, required unless Keep amends
	// without touching it.
	Message string
	// Amend replaces the commit HEAD is on rather than writing a new one.
	Amend bool
	// Keep amends while keeping the message the commit already has.
	Keep bool
	// SignOff adds the trailer naming the committer.
	SignOff bool
	// NoVerify skips the hooks of the project, which the caller asks for
	// deliberately: a project's hooks are its own.
	NoVerify bool
	// AllowEmpty writes a commit although nothing is staged.
	AllowEmpty bool
	// Paths commits those paths as they stand in the working tree, leaving
	// the rest of the index alone. Empty commits what is staged.
	Paths []string
}

// Commit writes a commit in the repository containing dir and returns the
// object name it landed at. The message goes through a file: it never appears
// on a command line, where a shell, a process listing or a log would see it.
func (r Runner) Commit(ctx context.Context, dir string, opt CommitOptions) (string, error) {
	args := []string{"commit", "--no-status"}
	switch {
	case opt.Keep:
		if !opt.Amend {
			return "", fmt.Errorf("git commit: a message is needed to write a commit")
		}
		if opt.Message != "" {
			return "", fmt.Errorf("git commit: keeping the message and writing one are not both")
		}
		args = append(args, "--no-edit")
	default:
		if err := checkMessage(opt.Message); err != nil {
			return "", err
		}
	}
	if opt.Amend {
		args = append(args, "--amend")
	}
	if opt.SignOff {
		args = append(args, "--signoff")
	}
	if opt.NoVerify {
		args = append(args, "--no-verify")
	}
	if opt.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	if !opt.Keep {
		file, clean, err := messageFile(opt.Message)
		if err != nil {
			return "", err
		}
		defer clean()
		args = append(args, "--file="+file, "--cleanup=whitespace")
	}
	args = append(args, "--")
	for _, p := range opt.Paths {
		if err := vcs.ValidatePath(p); err != nil {
			return "", fmt.Errorf("git commit: %w", err)
		}
		args = append(args, p)
	}
	if _, err := r.git(ctx, dir, args...); err != nil {
		return "", err
	}
	return r.Head(ctx, dir)
}

// Head is the commit HEAD stands on.
func (r Runner) Head(ctx context.Context, dir string) (string, error) {
	out, err := r.git(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	oid := strings.TrimRight(string(out), "\n")
	if !vcs.IsObjectName(oid) {
		return "", fmt.Errorf("%w: rev-parse printed %q", vcs.ErrMalformed, oid)
	}
	return oid, nil
}

// checkMessage refuses a message git would refuse or truncate, before
// anything is written.
func checkMessage(msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("git commit: the message is empty")
	}
	if len(msg) > maxMessage {
		return fmt.Errorf("git commit: the message holds %d bytes, more than %d", len(msg), maxMessage)
	}
	if strings.ContainsRune(msg, 0) {
		return fmt.Errorf("git commit: the message holds a NUL byte")
	}
	return nil
}

// messageFile writes the message where git reads it from: a directory of its
// own, readable by nobody else, removed as soon as the commit is written.
func messageFile(msg string) (string, func(), error) {
	// MkdirTemp makes the directory readable by its owner alone, so the
	// message is never open to another user of the machine.
	dir, err := os.MkdirTemp("", "lmux-commit-")
	if err != nil {
		return "", nil, fmt.Errorf("git commit: %w", err)
	}
	clean := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "message")
	if err := os.WriteFile(path, []byte(msg), 0o600); err != nil {
		clean()
		return "", nil, fmt.Errorf("git commit: %w", err)
	}
	return path, clean, nil
}
