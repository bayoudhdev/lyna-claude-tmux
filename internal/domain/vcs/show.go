package vcs

import (
	"fmt"
	"strings"
)

// ShowFormat is the format ParseShow reads, to be passed to
// `git show -z --numstat --first-parent --format=<this>`. The fields are
// separated by NUL, the message comes last because it is the only one that
// holds newlines, and the files the commit touched follow it.
const ShowFormat = "%H%x00%P%x00%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct%x00%D%x00%B"

// showFields is how many NUL separated fields come before the message, which
// is the last of the format and carries the files after it.
const showFields = 9

// MaxMessage bounds the message a commit is read with: a view draws the
// beginning of a message, not a file someone committed as one.
const MaxMessage = 64 << 10

// MaxShowFiles bounds the files one commit is read with, for the same reason
// MaxCommits bounds a history.
const MaxShowFiles = 4096

// CommitDetail is one commit read in full: what the history already says of
// it, the whole of its message, who committed it, and the files it touched.
type CommitDetail struct {
	Commit Commit
	// AuthorEmail is the address the author signed with, Committer and
	// CommitterEmail who landed it, which a rebase or a patch makes someone
	// else. All three are untrusted display data.
	AuthorEmail    string
	Committer      string
	CommitterEmail string
	// Body is the message under its subject, with the blank line between
	// them taken off, empty for a commit whose message is one line.
	Body string
	// Files are the files the commit touched, against its first parent for a
	// merge.
	Files []NumStat
}

// Added and Deleted are the lines the commit changed, binary files apart.
func (d CommitDetail) Added() int {
	var n int
	for _, f := range d.Files {
		n += f.Added
	}
	return n
}

// Deleted is the other half of Added.
func (d CommitDetail) Deleted() int {
	var n int
	for _, f := range d.Files {
		n += f.Deleted
	}
	return n
}

// ParseShow reads `git show -z --numstat --format=ShowFormat`.
func ParseShow(data []byte) (CommitDetail, error) {
	parts := strings.SplitN(string(data), "\x00", showFields+1)
	if len(parts) != showFields+1 {
		return CommitDetail{}, fmt.Errorf("%w: a commit of %d fields, want %d", ErrMalformed, len(parts), showFields+1)
	}
	c := Commit{OID: parts[0], Author: parts[2]}
	if !IsObjectName(c.OID) {
		return CommitDetail{}, fmt.Errorf("%w: commit object name %q", ErrMalformed, c.OID)
	}
	if parts[1] != "" {
		c.Parents = strings.Split(parts[1], " ")
		for _, p := range c.Parents {
			if !IsObjectName(p) {
				return CommitDetail{}, fmt.Errorf("%w: parent object name %q", ErrMalformed, p)
			}
		}
	}
	var err error
	if c.Authored, err = unixTime(parts[4]); err != nil {
		return CommitDetail{}, err
	}
	if c.Committed, err = unixTime(parts[7]); err != nil {
		return CommitDetail{}, err
	}
	if c.Refs, err = parseRefs(parts[8]); err != nil {
		return CommitDetail{}, err
	}
	d := CommitDetail{
		Commit:      c,
		AuthorEmail: parts[3], Committer: parts[5], CommitterEmail: parts[6],
	}
	message := parts[showFields]
	d.Commit.Subject, d.Body, err = splitMessage(message)
	if err != nil {
		return CommitDetail{}, err
	}
	if d.Files, err = showFiles(message); err != nil {
		return CommitDetail{}, err
	}
	return d, nil
}

// splitMessage takes the message apart: its first line is the subject, what
// is under it the body, and everything past the NUL that ends the message
// belongs to the files.
func splitMessage(message string) (subject, body string, err error) {
	message, _, _ = strings.Cut(message, "\x00")
	if len(message) > MaxMessage {
		return "", "", fmt.Errorf("%w: a commit message of %d bytes", ErrMalformed, len(message))
	}
	subject, body, _ = strings.Cut(strings.TrimRight(message, "\n"), "\n")
	return subject, strings.Trim(body, "\n"), nil
}

// showFiles reads the numstat records that follow the message. git ends the
// message with a NUL, writes a newline before the records, and writes a
// second NUL for a merge read against no parent of its own; a record always
// begins with its counts, so what comes before the first of them is the
// separator and nothing else.
func showFiles(message string) ([]NumStat, error) {
	_, rest, ok := strings.Cut(message, "\x00")
	if !ok {
		return nil, nil
	}
	rest = strings.TrimLeft(rest, "\x00\n")
	files, err := ParseNumstat([]byte(rest))
	if err != nil {
		return nil, err
	}
	if len(files) > MaxShowFiles {
		files = files[:MaxShowFiles]
	}
	return files, nil
}
