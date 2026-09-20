package vcs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxBranches bounds one reading of the branches of a project, for the same
// reason MaxWorktrees bounds its worktrees.
const MaxBranches = 1024

// BranchFormat is the format ParseBranches reads, to be passed to
// `git for-each-ref --format=<this> refs/heads/`. The fields are separated by
// NUL and the records by the newline for-each-ref writes between them, which
// none of the fields can hold: a ref name, an object name, a date and the
// subject of a commit are all single line.
const BranchFormat = "%(refname)%00%(objectname)%00%(HEAD)%00%(upstream)%00" +
	"%(upstream:track,nobracket)%00%(committerdate:unix)%00%(contents:subject)"

// branchFields is how many fields BranchFormat writes per ref.
const branchFields = 7

// LocalBranch is one branch of `git for-each-ref refs/heads/`.
type LocalBranch struct {
	// Name is the branch without its namespace ("task-a"), Ref the whole ref
	// ("refs/heads/task-a"). Both are untrusted display data.
	Name string
	Ref  string
	// OID is the commit the branch points at.
	OID string
	// Head marks the branch HEAD is on in the worktree that was read.
	Head bool
	// Upstream is the ref the branch tracks ("refs/remotes/origin/task-a"),
	// empty for a branch that tracks nothing.
	Upstream string
	// Gone marks a branch whose upstream no longer exists.
	Gone bool
	// Ahead and Behind count the commits the branch has that its upstream does
	// not, and the other way round. Both are zero when there is no upstream.
	Ahead, Behind int
	// Tip is when the commit was committed, and Subject its first line.
	Tip     time.Time
	Subject string
}

// ParseBranches reads `git for-each-ref --format=BranchFormat refs/heads/`.
func ParseBranches(data []byte) ([]LocalBranch, error) {
	var list []LocalBranch
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		if len(list) == MaxBranches {
			break
		}
		b, err := parseBranch(line)
		if err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, nil
}

func parseBranch(rec string) (LocalBranch, error) {
	fields := strings.Split(rec, "\x00")
	if len(fields) != branchFields {
		return LocalBranch{}, fmt.Errorf("%w: branch record of %d fields", ErrMalformed, len(fields))
	}
	b := LocalBranch{
		Ref: fields[0], OID: fields[1], Head: fields[2] == "*",
		Upstream: fields[3], Subject: fields[6],
	}
	if !strings.HasPrefix(b.Ref, headsPrefix) || b.Ref == headsPrefix {
		return LocalBranch{}, fmt.Errorf("%w: branch ref %q", ErrMalformed, b.Ref)
	}
	b.Name = strings.TrimPrefix(b.Ref, headsPrefix)
	if !IsObjectName(b.OID) {
		return LocalBranch{}, fmt.Errorf("%w: branch object name %q", ErrMalformed, b.OID)
	}
	// Every count is a count against an upstream, so a branch that tracks
	// nothing cannot be ahead of, behind or gone from anything.
	if b.Upstream == "" && fields[4] != "" {
		return LocalBranch{}, fmt.Errorf("%w: branch track %q without an upstream", ErrMalformed, fields[4])
	}
	if err := b.parseTrack(fields[4]); err != nil {
		return LocalBranch{}, err
	}
	if fields[5] != "" {
		seconds, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil {
			return LocalBranch{}, fmt.Errorf("%w: branch date %q", ErrMalformed, fields[5])
		}
		b.Tip = time.Unix(seconds, 0).UTC()
	}
	return b, nil
}

// parseTrack reads what git says of a branch against its upstream: nothing
// when they are level or there is no upstream, "gone" when the upstream was
// removed, and "ahead 1", "behind 2" or "ahead 1, behind 2" otherwise.
func (b *LocalBranch) parseTrack(track string) error {
	switch track {
	case "":
		return nil
	case "gone":
		b.Gone = true
		return nil
	}
	for _, part := range strings.Split(track, ", ") {
		word, count, ok := strings.Cut(part, " ")
		if !ok {
			return fmt.Errorf("%w: branch track %q", ErrMalformed, track)
		}
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return fmt.Errorf("%w: branch track %q", ErrMalformed, track)
		}
		switch word {
		case "ahead":
			b.Ahead = n
		case "behind":
			b.Behind = n
		default:
			return fmt.Errorf("%w: branch track %q", ErrMalformed, track)
		}
	}
	return nil
}

// ValidateRefName refuses what git itself refuses in a ref, and a leading
// dash on top of it, so a name read from a repository or typed by a user is
// never handed to a command as an option or as a path of its own. The rules
// are the ones `git check-ref-format` holds a branch to.
func ValidateRefName(name string) error {
	bad := func() error { return fmt.Errorf("%w: ref name %q", ErrMalformed, name) }
	switch {
	case name == "" || name == "@" || name == "HEAD":
		return bad()
	case strings.HasPrefix(name, "-"), strings.HasPrefix(name, "/"), strings.HasSuffix(name, "/"):
		return bad()
	case strings.HasSuffix(name, "."), strings.Contains(name, ".."), strings.Contains(name, "@{"):
		return bad()
	}
	for _, r := range name {
		if r < ' ' || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad()
		}
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return bad()
		}
	}
	return nil
}
