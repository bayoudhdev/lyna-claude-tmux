package vcs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxCommits bounds one reading of a history. The graph loads a page at a
// time, so this is the size of a page rather than the size of a project.
const MaxCommits = 4096

// LogFormat is the format ParseLog reads, to be passed to
// `git log --decorate=full -z --format=<this>`. With -z git separates the
// commits with NUL as well, so the whole reading is one run of NUL terminated
// fields, seven to a commit.
const LogFormat = "%H%x00%P%x00%an%x00%at%x00%ct%x00%D%x00%s"

// logFields is how many fields LogFormat writes per commit.
const logFields = 7

// RefKind is what a ref pointing at a commit is.
type RefKind int

const (
	// RefBranch is a branch of the project, RefRemote a branch of a remote,
	// RefTag a tag, and RefOther anything else git decorates a commit with
	// (a note, a stash, a ref of another namespace).
	RefBranch RefKind = iota
	RefRemote
	RefTag
	RefOther
)

// Ref is one decoration of a commit.
type Ref struct {
	// Name is the ref without its namespace ("main", "origin/main",
	// "v1.1.0"), and Full the ref as git writes it. Both are untrusted
	// display data.
	Name string
	Full string
	Kind RefKind
	// Head marks the ref HEAD is on.
	Head bool
}

// Commit is one commit of a history.
type Commit struct {
	// OID is the commit, Parents the commits it follows: none for the first
	// commit of a history, two or more for a merge.
	OID     string
	Parents []string
	// Author is who wrote it, Authored when they did and Committed when it
	// landed, which a rebase moves and a commit does not.
	Author    string
	Authored  time.Time
	Committed time.Time
	// Refs are the branches, tags and remote branches pointing at it.
	Refs []Ref
	// Subject is the first line of the message.
	Subject string
}

// Merge reports a commit with more than one parent.
func (c Commit) Merge() bool { return len(c.Parents) > 1 }

// Short is the commit abbreviated the way a history is read, seven characters
// like git's own default.
func (c Commit) Short() string {
	if len(c.OID) < 7 {
		return c.OID
	}
	return c.OID[:7]
}

// ParseLog reads `git log --decorate=full -z --format=LogFormat`.
func ParseLog(data []byte) ([]Commit, error) {
	fields := splitNUL(data)
	if len(fields)%logFields != 0 {
		return nil, fmt.Errorf("%w: a history of %d fields, which is no whole number of commits", ErrMalformed, len(fields))
	}
	list := make([]Commit, 0, len(fields)/logFields)
	for i := 0; i+logFields <= len(fields); i += logFields {
		if len(list) == MaxCommits {
			break
		}
		c, err := parseCommit(fields[i : i+logFields])
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, nil
}

func parseCommit(f []string) (Commit, error) {
	c := Commit{OID: f[0], Author: f[2], Subject: f[6]}
	if !IsObjectName(c.OID) {
		return Commit{}, fmt.Errorf("%w: commit object name %q", ErrMalformed, c.OID)
	}
	if f[1] != "" {
		c.Parents = strings.Split(f[1], " ")
		for _, p := range c.Parents {
			if !IsObjectName(p) {
				return Commit{}, fmt.Errorf("%w: parent object name %q", ErrMalformed, p)
			}
		}
	}
	var err error
	if c.Authored, err = unixTime(f[3]); err != nil {
		return Commit{}, err
	}
	if c.Committed, err = unixTime(f[4]); err != nil {
		return Commit{}, err
	}
	if c.Refs, err = parseRefs(f[5]); err != nil {
		return Commit{}, err
	}
	return c, nil
}

func unixTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	seconds, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: commit date %q", ErrMalformed, s)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// parseRefs reads what git decorates a commit with, which it writes in full
// ("HEAD -> refs/heads/main, tag: refs/tags/v1.1.0") so a branch whose name
// holds a slash is never read as a remote.
func parseRefs(decoration string) ([]Ref, error) {
	if decoration == "" {
		return nil, nil
	}
	var refs []Ref
	for _, part := range strings.Split(decoration, ", ") {
		if part == "" {
			return nil, fmt.Errorf("%w: empty ref in %q", ErrMalformed, decoration)
		}
		r := Ref{}
		if rest, ok := strings.CutPrefix(part, "HEAD -> "); ok {
			r.Head, part = true, rest
		}
		if rest, ok := strings.CutPrefix(part, "tag: "); ok {
			part = rest
		}
		r.Full = part
		switch {
		case part == "HEAD":
			r.Name, r.Kind, r.Head = "HEAD", RefOther, true
		case strings.HasPrefix(part, headsPrefix):
			r.Name, r.Kind = strings.TrimPrefix(part, headsPrefix), RefBranch
		case strings.HasPrefix(part, remotesPrefix):
			r.Name, r.Kind = strings.TrimPrefix(part, remotesPrefix), RefRemote
		case strings.HasPrefix(part, tagsPrefix):
			r.Name, r.Kind = strings.TrimPrefix(part, tagsPrefix), RefTag
		default:
			r.Name, r.Kind = part, RefOther
		}
		if r.Name == "" {
			return nil, fmt.Errorf("%w: ref %q names nothing", ErrMalformed, part)
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// remotesPrefix is the namespace a branch of a remote lives in; tagsPrefix is
// the one a tag lives in, beside the tags themselves.
const remotesPrefix = "refs/remotes/"
