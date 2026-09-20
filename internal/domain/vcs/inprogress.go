package vcs

import (
	"fmt"
	"strconv"
	"strings"
)

// Operation is what git stopped in the middle of.
type Operation int

const (
	// OperationNone is a repository in the middle of nothing.
	OperationNone Operation = iota
	OperationMerge
	OperationRebase
	// OperationApply is `git am`, which stops the same way a rebase does.
	OperationApply
	OperationCherryPick
	OperationRevert
	OperationBisect
)

// String names an operation the way the view says it.
func (o Operation) String() string {
	switch o {
	case OperationNone:
		return "none"
	case OperationMerge:
		return "merge"
	case OperationRebase:
		return "rebase"
	case OperationApply:
		return "apply"
	case OperationCherryPick:
		return "cherry pick"
	case OperationRevert:
		return "revert"
	case OperationBisect:
		return "bisect"
	}
	return "unknown"
}

// InProgress is the operation a repository stopped in the middle of, which is
// what the banner offers to carry on, skip or abort.
type InProgress struct {
	Kind Operation
	// Branch is the branch being replayed or bisected, empty when HEAD is
	// detached.
	Branch string
	// Onto is the commit a rebase replays onto.
	Onto string
	// Heads are the commits being applied: the one a rebase, a cherry pick or
	// a revert stopped on, or every branch of a merge.
	Heads []string
	// Step and Total are where a rebase or an application stands, both zero
	// when the operation does not count.
	Step, Total int
	// Bisecting marks a bisection, which git runs beside any of the above.
	Bisecting bool
}

// Running reports an operation waiting for the user.
func (p InProgress) Running() bool { return p.Kind != OperationNone }

// The files of the git directory that say what is running. A reader takes
// exactly these, so a repository is read with a known number of small reads.
const (
	fileMergeHead      = "MERGE_HEAD"
	fileCherryPickHead = "CHERRY_PICK_HEAD"
	fileRevertHead     = "REVERT_HEAD"
	fileBisectLog      = "BISECT_LOG"
	fileBisectStart    = "BISECT_START"
	fileRebaseMerge    = "rebase-merge/"
	fileRebaseApply    = "rebase-apply/"
)

// InProgressPaths are the files ParseInProgress reads, relative to the git
// directory of the working tree. Files that do not exist are left out of the
// map given to it.
var InProgressPaths = []string{
	fileMergeHead,
	fileCherryPickHead,
	fileRevertHead,
	fileBisectLog,
	fileBisectStart,
	fileRebaseMerge + "head-name",
	fileRebaseMerge + "onto",
	fileRebaseMerge + "msgnum",
	fileRebaseMerge + "end",
	fileRebaseMerge + "stopped-sha",
	fileRebaseApply + "head-name",
	fileRebaseApply + "onto",
	fileRebaseApply + "next",
	fileRebaseApply + "last",
	fileRebaseApply + "original-commit",
	fileRebaseApply + "applying",
}

// detachedHead is what git writes as the name of a head that is on no branch.
const detachedHead = "detached HEAD"

// ParseInProgress reads the files of InProgressPaths that exist. The order
// they are looked at is the order git itself reports them, so a rebase that
// stopped on a conflict is a rebase and not the merge it is carried out with.
func ParseInProgress(files map[string]string) (InProgress, error) {
	p := InProgress{}
	_, p.Bisecting = files[fileBisectLog]
	switch {
	case has(files, fileRebaseMerge+"head-name"):
		if err := p.readRebase(files, fileRebaseMerge, "msgnum", "end", "stopped-sha"); err != nil {
			return InProgress{}, err
		}
		p.Kind = OperationRebase
	case has(files, fileRebaseApply+"next"):
		if err := p.readRebase(files, fileRebaseApply, "next", "last", "original-commit"); err != nil {
			return InProgress{}, err
		}
		p.Kind = OperationRebase
		if has(files, fileRebaseApply+"applying") {
			p.Kind = OperationApply
		}
	case has(files, fileCherryPickHead):
		heads, err := oids(files, fileCherryPickHead)
		if err != nil {
			return InProgress{}, err
		}
		p.Kind, p.Heads = OperationCherryPick, heads
	case has(files, fileRevertHead):
		heads, err := oids(files, fileRevertHead)
		if err != nil {
			return InProgress{}, err
		}
		p.Kind, p.Heads = OperationRevert, heads
	case has(files, fileMergeHead):
		heads, err := oids(files, fileMergeHead)
		if err != nil {
			return InProgress{}, err
		}
		p.Kind, p.Heads = OperationMerge, heads
	case p.Bisecting:
		p.Kind = OperationBisect
		if err := p.readBisect(files); err != nil {
			return InProgress{}, err
		}
	}
	return p, nil
}

// readRebase reads the state a rebase keeps, which the two backends write
// under their own directory and under their own names.
func (p *InProgress) readRebase(files map[string]string, dir, step, total, stopped string) error {
	branch, err := headName(files, dir+"head-name")
	if err != nil {
		return err
	}
	p.Branch = branch
	if has(files, dir+"onto") {
		onto, err := oid(files, dir+"onto")
		if err != nil {
			return err
		}
		p.Onto = onto
	}
	if p.Step, err = count(files, dir+step); err != nil {
		return err
	}
	if p.Total, err = count(files, dir+total); err != nil {
		return err
	}
	if has(files, dir+stopped) {
		head, err := oid(files, dir+stopped)
		if err != nil {
			return err
		}
		p.Heads = []string{head}
	}
	return nil
}

// readBisect reads where a bisection started, which git writes as the branch
// it was launched from or, on a detached head, as the commit itself.
func (p *InProgress) readBisect(files map[string]string) error {
	start, ok := line(files, fileBisectStart)
	if !ok || start == "" {
		return nil
	}
	if IsObjectName(start) {
		p.Heads = []string{start}
		return nil
	}
	if err := ValidateRefName(start); err != nil {
		return err
	}
	p.Branch = start
	return nil
}

func has(files map[string]string, name string) bool {
	_, ok := files[name]
	return ok
}

// line is the first line of a file git writes one value in.
func line(files map[string]string, name string) (string, bool) {
	v, ok := files[name]
	if !ok {
		return "", false
	}
	v, _, _ = strings.Cut(v, "\n")
	return v, true
}

func oid(files map[string]string, name string) (string, error) {
	v, _ := line(files, name)
	if !IsObjectName(v) {
		return "", fmt.Errorf("%w: %s holds %q, which is no object name", ErrMalformed, name, v)
	}
	return v, nil
}

// oids reads a file holding one object name per line, which is what a merge
// of more than two branches writes.
func oids(files map[string]string, name string) ([]string, error) {
	var out []string
	for _, v := range strings.Split(strings.TrimRight(files[name], "\n"), "\n") {
		if !IsObjectName(v) {
			return nil, fmt.Errorf("%w: %s holds %q, which is no object name", ErrMalformed, name, v)
		}
		out = append(out, v)
	}
	return out, nil
}

// headName reads the branch a rebase is replaying, which git names in full or
// calls a detached head.
func headName(files map[string]string, name string) (string, error) {
	v, ok := line(files, name)
	if !ok || v == detachedHead {
		return "", nil
	}
	branch, found := strings.CutPrefix(v, headsPrefix)
	if !found || branch == "" {
		return "", fmt.Errorf("%w: %s holds %q, which names no branch", ErrMalformed, name, v)
	}
	if err := ValidateRefName(branch); err != nil {
		return "", err
	}
	return branch, nil
}

// count reads a counter of a rebase, which git writes as a plain number.
func count(files map[string]string, name string) (int, error) {
	v, ok := line(files, name)
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("%w: %s holds %q, which is no count", ErrMalformed, name, v)
	}
	return n, nil
}
