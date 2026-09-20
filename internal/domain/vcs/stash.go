package vcs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxStashes bounds one reading of the stashes of a project.
const MaxStashes = 1024

// StashFormat is the format ParseStashes reads, to be passed to
// `git stash list -z --format=<this>`. With -z git separates the fields and
// the stashes alike with NUL, so the reading is one run of NUL terminated
// fields, five to a stash.
const StashFormat = "%gd%x00%H%x00%P%x00%ct%x00%gs"

// stashFields is how many fields StashFormat writes per stash.
const stashFields = 5

// A stash commit holds the working tree, the index it was made with and, when
// untracked files were stashed as well, a third commit holding those.
const (
	stashParents          = 2
	stashParentsUntracked = 3
)

// Stash is one entry of the stash list of a project.
type Stash struct {
	// Index is the position in the list and Ref the way git names it,
	// "stash@{0}" for the entry pushed last.
	Index int
	Ref   string
	// OID is the commit holding the stash and Base the commit it was made on.
	OID  string
	Base string
	// Created is when it was pushed.
	Created time.Time
	// Branch is the branch it was made on, empty when HEAD was detached.
	Branch string
	// Message is what it says, either the message given or the commit the
	// working tree stood on. Untrusted display data.
	Message string
	// WIP marks a stash pushed with no message of its own, and Untracked one
	// that carries untracked files.
	WIP       bool
	Untracked bool
}

// The prefixes git writes a stash message with, and what it calls a HEAD that
// is on no branch.
const (
	stashWIPPrefix = "WIP on "
	stashOnPrefix  = "On "
	stashNoBranch  = "(no branch)"
)

// ParseStashes reads `git stash list -z --format=StashFormat`.
func ParseStashes(data []byte) ([]Stash, error) {
	fields := splitNUL(data)
	if len(fields)%stashFields != 0 {
		return nil, fmt.Errorf("%w: a stash list of %d fields, which is no whole number of stashes", ErrMalformed, len(fields))
	}
	list := make([]Stash, 0, len(fields)/stashFields)
	for i := 0; i+stashFields <= len(fields); i += stashFields {
		if len(list) == MaxStashes {
			break
		}
		s, err := parseStash(fields[i : i+stashFields])
		if err != nil {
			return nil, err
		}
		// git lists the stashes from the one pushed last; a list that does not
		// count from zero upwards is not the list we asked for, and acting on
		// the wrong entry destroys work.
		if s.Index != len(list) {
			return nil, fmt.Errorf("%w: stash %s in position %d", ErrMalformed, s.Ref, len(list))
		}
		list = append(list, s)
	}
	return list, nil
}

func parseStash(f []string) (Stash, error) {
	s := Stash{Ref: f[0], OID: f[1]}
	index, err := stashIndex(s.Ref)
	if err != nil {
		return Stash{}, err
	}
	s.Index = index
	if !isHex(s.OID) {
		return Stash{}, fmt.Errorf("%w: stash object name %q", ErrMalformed, s.OID)
	}
	parents := strings.Split(f[2], " ")
	if len(parents) != stashParents && len(parents) != stashParentsUntracked {
		return Stash{}, fmt.Errorf("%w: stash %s of %d parents", ErrMalformed, s.Ref, len(parents))
	}
	for _, p := range parents {
		if !isHex(p) {
			return Stash{}, fmt.Errorf("%w: stash parent object name %q", ErrMalformed, p)
		}
	}
	s.Base, s.Untracked = parents[0], len(parents) == stashParentsUntracked
	if s.Created, err = unixTime(f[3]); err != nil {
		return Stash{}, err
	}
	s.Branch, s.Message, s.WIP = parseStashMessage(f[4])
	return s, nil
}

// stashIndex reads the "stash@{7}" git names an entry with.
func stashIndex(ref string) (int, error) {
	rest, ok := strings.CutPrefix(ref, "stash@{")
	if !ok || !strings.HasSuffix(rest, "}") {
		return 0, fmt.Errorf("%w: stash reference %q", ErrMalformed, ref)
	}
	digits := strings.TrimSuffix(rest, "}")
	index, err := strconv.Atoi(digits)
	// The reference names the entry a command acts on, so only the form git
	// writes is accepted: a padded or signed number would name the same entry
	// here and something else on the command line.
	if err != nil || index < 0 || strconv.Itoa(index) != digits {
		return 0, fmt.Errorf("%w: stash reference %q", ErrMalformed, ref)
	}
	return index, nil
}

// parseStashMessage reads the reflog subject of a stash, which git writes as
// "On <branch>: <message>" or, with no message given, "WIP on <branch>:
// <commit> <subject>". A subject of another shape is kept whole rather than
// cut at a guess: it is shown, never acted on.
func parseStashMessage(subject string) (branch, message string, wip bool) {
	rest, wip := strings.CutPrefix(subject, stashWIPPrefix)
	if !wip {
		var on bool
		if rest, on = strings.CutPrefix(subject, stashOnPrefix); !on {
			return "", subject, false
		}
	}
	// A branch name holds no colon, so the first ": " ends it.
	branch, message, found := strings.Cut(rest, ": ")
	if !found {
		return "", subject, false
	}
	if branch == stashNoBranch {
		branch = ""
	}
	return branch, message, wip
}
