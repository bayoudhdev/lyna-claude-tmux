package vcs

import (
	"fmt"
	"strings"
	"time"
)

// MaxTags bounds one reading of the tags of a project.
const MaxTags = 4096

// TagFormat is the format ParseTags reads, to be passed to
// `git for-each-ref --format=<this> refs/tags/`. An annotated tag is an
// object of its own, so the commit it names is asked for separately and is
// empty for a tag that is only a ref.
const TagFormat = "%(refname)%00%(objecttype)%00%(objectname)%00%(*objectname)%00%(creatordate:unix)%00%(contents:subject)"

// tagFields is how many fields TagFormat writes per tag.
const tagFields = 6

// tagsPrefix is the namespace a tag lives in.
const tagsPrefix = "refs/tags/"

// Tag is one tag of a project.
type Tag struct {
	// Name is the tag without its namespace, Ref the ref in full.
	Name string
	Ref  string
	// OID is what the ref points at: the tag object of an annotated tag, the
	// commit of one that is only a ref.
	OID string
	// Commit is the commit the tag names, whichever of the two it is.
	Commit string
	// Annotated marks a tag carrying a message and a date of its own.
	Annotated bool
	// Created is when the tag was made, or when the commit was made for a tag
	// that is only a ref.
	Created time.Time
	// Subject is the first line of the message: the tag's own when it has
	// one, the commit's otherwise. Untrusted display data.
	Subject string
}

// ParseTags reads `git for-each-ref --format=TagFormat refs/tags/`. The
// records are separated by newlines, the fields within one by NUL, so a
// message holding a newline is read as the one field it is.
func ParseTags(data []byte) ([]Tag, error) {
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	var list []Tag
	for record := range strings.SplitSeq(text, "\n") {
		if len(list) == MaxTags {
			break
		}
		t, err := parseTag(record)
		if err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, nil
}

func parseTag(record string) (Tag, error) {
	f := strings.Split(record, "\x00")
	if len(f) != tagFields {
		return Tag{}, fmt.Errorf("%w: a tag of %d fields", ErrMalformed, len(f))
	}
	t := Tag{Ref: f[0], OID: f[2], Commit: f[3], Subject: f[5]}
	name, ok := strings.CutPrefix(t.Ref, tagsPrefix)
	if !ok || name == "" {
		return Tag{}, fmt.Errorf("%w: tag ref %q", ErrMalformed, t.Ref)
	}
	t.Name = name
	if !IsObjectName(t.OID) {
		return Tag{}, fmt.Errorf("%w: tag object name %q", ErrMalformed, t.OID)
	}
	switch f[1] {
	case "tag":
		t.Annotated = true
		if !IsObjectName(t.Commit) {
			return Tag{}, fmt.Errorf("%w: tag %q names the commit %q", ErrMalformed, t.Name, t.Commit)
		}
	case "commit":
		// A tag that is only a ref points at the commit itself, and git has
		// nothing to dereference.
		if t.Commit != "" {
			return Tag{}, fmt.Errorf("%w: tag %q is a commit and names %q as well", ErrMalformed, t.Name, t.Commit)
		}
		t.Commit = t.OID
	default:
		return Tag{}, fmt.Errorf("%w: tag %q points at a %q", ErrMalformed, t.Name, f[1])
	}
	var err error
	if t.Created, err = unixTime(f[4]); err != nil {
		return Tag{}, err
	}
	return t, nil
}
