// Package githead reads the checked-out branch of a git repository straight
// from its metadata files, without starting git.
//
// The hook and the status line both show the branch, and both run on every
// Claude event, so spawning `git` there would dominate their cost. Reading
// .git/HEAD covers the cases that matter for a label: a branch, a detached
// HEAD (shown as a short hash) and linked worktrees or submodules, whose .git
// is a file pointing at the real git directory.
//
// Repository contents are untrusted. Files are read without following a final
// symbolic link, bounded in size, and parsed strictly. A gitdir pointer comes
// from the repository, so it is constrained as well: it may name a git
// directory of this working tree or of an enclosing one, or anything below the
// user's home directory, and nothing else is opened.
package githead

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

const (
	// MaxFileBytes bounds every metadata file read. A HEAD file is one short line.
	MaxFileBytes = 4 << 10
	// ShortHashLen is the length of the abbreviated hash shown for a detached HEAD.
	ShortHashLen = 7
	// maxDepth bounds the walk from the start directory towards the root.
	maxDepth = 128
)

var (
	// ErrNoRepository is returned when no enclosing repository is found.
	ErrNoRepository = errors.New("githead: not inside a git repository")
	// ErrInvalidHead is returned when HEAD is neither a ref nor a hash.
	ErrInvalidHead = errors.New("githead: unrecognized HEAD content")
	// ErrGitDirOutside is returned when a .git file points at a directory
	// that belongs to neither the working tree nor the user.
	ErrGitDirOutside = errors.New("githead: .git points outside the repository and the home directory")
)

// homeDir is the user's home directory, or "" when it is unknown. It bounds
// the gitdir pointers that leave the working tree, which is where git puts
// the git directory of a linked worktree. Tests replace it.
var homeDir = sync.OnceValue(func() string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Clean(home)
})

// ReadFunc reads a regular file bounded by limit bytes. It must not follow a
// final symbolic link.
type ReadFunc func(path string, limit int64) ([]byte, error)

// ReadFile is the production ReadFunc.
func ReadFile(path string, limit int64) ([]byte, error) {
	return fsx.ReadFileNoFollow(path, limit)
}

// Branch returns the label for the HEAD of the repository enclosing dir: the
// branch name, or the short hash when HEAD is detached. read defaults to
// ReadFile when nil.
func Branch(dir string, read ReadFunc) (string, error) {
	if read == nil {
		read = ReadFile
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return "", fmt.Errorf("githead: directory must be absolute: %q", dir)
	}
	d := filepath.Clean(dir)
	for range maxDepth {
		label, found, err := atDir(d, read)
		if found {
			return label, err
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "", ErrNoRepository
}

// atDir looks for repository metadata directly inside d. found reports that d
// holds a .git entry, in which case the walk stops whatever the outcome.
func atDir(d string, read ReadFunc) (label string, found bool, err error) {
	gitPath := filepath.Join(d, ".git")
	head, err := read(filepath.Join(gitPath, "HEAD"), MaxFileBytes)
	if err == nil {
		label, err := Parse(head)
		return label, true, err
	}
	if !missing(err) {
		return "", true, err
	}

	// A linked worktree or submodule: .git is a file naming the git directory.
	link, err := read(gitPath, MaxFileBytes)
	if err != nil {
		if missing(err) {
			return "", false, nil
		}
		// .git exists but is not a readable file (a directory without HEAD,
		// a symbolic link, a special file): this is the repository boundary.
		return "", true, err
	}
	gitDir, err := parseGitDirFile(link)
	if err != nil {
		return "", true, err
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(d, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if !gitDirAllowed(gitDir, d, homeDir()) {
		return "", true, fmt.Errorf("%w: %s", ErrGitDirOutside, gitDir)
	}
	head, err = read(filepath.Join(gitDir, "HEAD"), MaxFileBytes)
	if err != nil {
		return "", true, err
	}
	label, err = Parse(head)
	return label, true, err
}

// missing reports errors that mean "no such entry here": the path does not
// exist, or a parent component is a regular file (.git/HEAD when .git is a file).
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// gitDirAllowed reports whether a .git file in d may point at gitDir. The
// pointer is repository controlled, so without a bound a checkout could have
// every hook event and every status line render open a path of its choosing.
// Three targets are accepted, which is every shape git itself writes: the git
// directory of this working tree (below d), the git directory of an enclosing
// working tree, which is where a linked worktree and a submodule live, and
// anything below the user's home directory.
func gitDirAllowed(gitDir, d, home string) bool {
	if within(gitDir, d) || within(gitDir, home) {
		return true
	}
	owner, ok := workTreeOf(gitDir)
	return ok && within(d, owner)
}

// workTreeOf returns the working tree that owns a git directory: the parent of
// its outermost ".git" component, as in <tree>/.git/worktrees/<name>.
func workTreeOf(gitDir string) (string, bool) {
	for dir := gitDir; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		if filepath.Base(dir) == ".git" {
			return parent, true
		}
		dir = parent
	}
}

// within reports whether path is dir or below it. An empty dir contains
// nothing.
func within(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseGitDirFile(data []byte) (string, error) {
	line := firstLine(data)
	rest, ok := strings.CutPrefix(line, "gitdir:")
	rest = strings.TrimSpace(rest)
	if !ok || rest == "" || strings.ContainsFunc(rest, isControl) {
		return "", fmt.Errorf("githead: invalid .git file")
	}
	return rest, nil
}

// Parse interprets the content of a HEAD file.
func Parse(data []byte) (string, error) {
	line := firstLine(data)
	if ref, ok := strings.CutPrefix(line, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		if !validRef(ref) {
			return "", ErrInvalidHead
		}
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return name, nil
		}
		return strings.TrimPrefix(ref, "refs/"), nil
	}
	if isHash(line) {
		return line[:ShortHashLen], nil
	}
	return "", ErrInvalidHead
}

func firstLine(data []byte) string {
	s := string(data)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// isHash accepts SHA-1 and SHA-256 object names.
func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validRef applies the parts of git's ref name rules that keep a label sane:
// a refs/ prefix, no control characters, spaces or the characters git forbids.
func validRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || len(ref) == len("refs/") {
		return false
	}
	if strings.Contains(ref, "..") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") {
		return false
	}
	return !strings.ContainsFunc(ref, func(r rune) bool {
		return isControl(r) || r == ' ' || strings.ContainsRune("~^:?*[\\", r)
	})
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
