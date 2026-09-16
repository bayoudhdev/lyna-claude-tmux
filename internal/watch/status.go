// Package watch reads the working tree changes of a git repository and keeps
// them current: parsers for git's machine formats, a runner that executes git
// safely, the change model the changes pane draws, and a watcher that refreshes
// on file events, on the tmux signal Claude's edit hooks send, and on an
// optional interval.
package watch

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrMalformed reports git output that does not follow the documented format.
var ErrMalformed = errors.New("watch: malformed git output")

// Kind classifies a status entry.
type Kind int

const (
	// KindChanged is an ordinary tracked change (modified, added, deleted,
	// type change).
	KindChanged Kind = iota
	// KindRenamed is a rename recorded in the index or worktree.
	KindRenamed
	// KindCopied is a copy.
	KindCopied
	// KindUnmerged is a path with merge conflicts.
	KindUnmerged
	// KindUntracked is a file git does not track.
	KindUntracked
	// KindIgnored is an ignored file (only listed when requested).
	KindIgnored
)

// Unchanged is the status code of a side (index or worktree) with no change.
const Unchanged = '.'

// Entry is one path of `git status --porcelain=v2`.
type Entry struct {
	Kind Kind
	// Index and Worktree are the staged and unstaged status codes: '.', M, T,
	// A, D, R, C or U. Untracked and ignored entries use '?' and '!' for both.
	Index    byte
	Worktree byte
	// Path is the repository-relative path, exactly as git reports it. It is
	// untrusted display data: sanitize before drawing.
	Path string
	// OrigPath is the source of a rename or copy.
	OrigPath string
}

// Branch is the branch header of `git status --branch`.
type Branch struct {
	// OID is the HEAD commit, "" before the first commit.
	OID string
	// Head is the branch name, "" when HEAD is detached.
	Head     string
	Detached bool
	// Initial is true before the first commit.
	Initial bool
	// Upstream is the tracking branch, "" when none is configured.
	Upstream string
	// AheadBehind reports whether Ahead and Behind are known (the upstream
	// exists).
	AheadBehind bool
	Ahead       int
	Behind      int
}

// Status is a parsed `git status --porcelain=v2 -z --branch` output.
type Status struct {
	Branch  Branch
	Entries []Entry
}

// ParseStatus parses `git status --porcelain=v2 -z --branch` output. Header
// lines git may add in later versions are ignored; unknown entry types are
// refused because their field layout (and any extra NUL field) is unknown.
func ParseStatus(data []byte) (Status, error) {
	var st Status
	fields := splitNUL(data)
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			return Status{}, fmt.Errorf("%w: empty status record", ErrMalformed)
		}
		var err error
		switch rec[0] {
		case '#':
			err = st.Branch.parseHeader(rec)
		case '1':
			err = st.addEntry(rec, KindChanged, 9, "")
		case '2':
			if i+1 >= len(fields) {
				return Status{}, fmt.Errorf("%w: rename without original path", ErrMalformed)
			}
			i++
			err = st.addEntry(rec, KindRenamed, 10, fields[i])
		case 'u':
			err = st.addEntry(rec, KindUnmerged, 11, "")
		case '?', '!':
			err = st.addUntracked(rec)
		default:
			err = fmt.Errorf("%w: unknown status record type %q", ErrMalformed, rec[0])
		}
		if err != nil {
			return Status{}, err
		}
	}
	return st, nil
}

func (b *Branch) parseHeader(rec string) error {
	key, value, _ := strings.Cut(strings.TrimPrefix(rec, "# "), " ")
	switch key {
	case "branch.oid":
		if value == "(initial)" {
			b.Initial = true
			return nil
		}
		if !isHex(value) {
			return fmt.Errorf("%w: branch oid %q", ErrMalformed, value)
		}
		b.OID = value
	case "branch.head":
		if value == "(detached)" {
			b.Detached = true
			return nil
		}
		if value == "" {
			return fmt.Errorf("%w: empty branch head", ErrMalformed)
		}
		b.Head = value
	case "branch.upstream":
		b.Upstream = value
	case "branch.ab":
		a, bh, ok := strings.Cut(value, " ")
		ahead, err1 := strconv.Atoi(strings.TrimPrefix(a, "+"))
		behind, err2 := strconv.Atoi(strings.TrimPrefix(bh, "-"))
		if !ok || !strings.HasPrefix(a, "+") || !strings.HasPrefix(bh, "-") || err1 != nil || err2 != nil || ahead < 0 || behind < 0 {
			return fmt.Errorf("%w: branch.ab %q", ErrMalformed, value)
		}
		b.AheadBehind, b.Ahead, b.Behind = true, ahead, behind
	}
	return nil
}

// addEntry parses a changed, renamed or unmerged record with n space
// separated fields, the last of which is the path (paths may contain spaces).
func (st *Status) addEntry(rec string, kind Kind, n int, orig string) error {
	f := strings.SplitN(rec, " ", n)
	if len(f) != n {
		return fmt.Errorf("%w: short status record %q", ErrMalformed, rec)
	}
	xy := f[1]
	if len(xy) != 2 || !statusCode(xy[0]) || !statusCode(xy[1]) {
		return fmt.Errorf("%w: status code %q", ErrMalformed, xy)
	}
	if kind == KindRenamed {
		switch {
		case strings.HasPrefix(f[8], "R"):
		case strings.HasPrefix(f[8], "C"):
			kind = KindCopied
		default:
			return fmt.Errorf("%w: rename score %q", ErrMalformed, f[8])
		}
		if err := checkPath(orig); err != nil {
			return err
		}
	}
	path := f[n-1]
	if err := checkPath(path); err != nil {
		return err
	}
	st.Entries = append(st.Entries, Entry{Kind: kind, Index: xy[0], Worktree: xy[1], Path: path, OrigPath: orig})
	return nil
}

func (st *Status) addUntracked(rec string) error {
	if len(rec) < 3 || rec[1] != ' ' {
		return fmt.Errorf("%w: status record %q", ErrMalformed, rec)
	}
	path := rec[2:]
	if err := checkPath(path); err != nil {
		return err
	}
	kind, code := KindUntracked, byte('?')
	if rec[0] == '!' {
		kind, code = KindIgnored, '!'
	}
	st.Entries = append(st.Entries, Entry{Kind: kind, Index: code, Worktree: code, Path: path})
	return nil
}

func statusCode(c byte) bool {
	return strings.IndexByte(".MTADRCU", c) >= 0
}

func isHex(s string) bool {
	if len(s) < 4 {
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

// checkPath accepts repository-relative paths only. git never reports
// absolute paths or ".." components; seeing one means the output is not what
// it claims to be, and later consumers (review, open in editor) must never be
// pointed outside the repository.
func checkPath(p string) error {
	if p == "" || p[0] == '/' {
		return fmt.Errorf("%w: path %q", ErrMalformed, p)
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == ".." {
			return fmt.Errorf("%w: path %q leaves the repository", ErrMalformed, p)
		}
	}
	return nil
}

// splitNUL splits NUL-terminated records; a missing final terminator is
// tolerated.
func splitNUL(data []byte) []string {
	data = bytes.TrimSuffix(data, []byte{0})
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(p)
	}
	return out
}

// NumStat is one path of `git diff --numstat -z`.
type NumStat struct {
	Path     string
	OrigPath string
	Added    int
	Deleted  int
	// Binary is true for binary files, which have no line counts.
	Binary bool
}

// ParseNumstat parses `git diff --numstat -z` output, including the rename
// form in which the path field is empty and the original and new paths follow
// as separate NUL-terminated fields.
func ParseNumstat(data []byte) ([]NumStat, error) {
	fields := splitNUL(data)
	var out []NumStat
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("%w: numstat record %q", ErrMalformed, fields[i])
		}
		ns := NumStat{Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			ns.Binary = true
		} else {
			a, err1 := strconv.Atoi(parts[0])
			d, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil || a < 0 || d < 0 {
				return nil, fmt.Errorf("%w: numstat counts %q", ErrMalformed, fields[i])
			}
			ns.Added, ns.Deleted = a, d
		}
		if ns.Path == "" {
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("%w: numstat rename without paths", ErrMalformed)
			}
			ns.OrigPath, ns.Path = fields[i+1], fields[i+2]
			i += 2
			if err := checkPath(ns.OrigPath); err != nil {
				return nil, err
			}
		}
		if err := checkPath(ns.Path); err != nil {
			return nil, err
		}
		out = append(out, ns)
	}
	return out, nil
}
