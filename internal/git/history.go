package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// maxStateFile caps a file read from the git directory. The files that say
// what git is in the middle of hold an object name, a counter or a ref; a
// repository that ships a large one is not read into memory for it.
const maxStateFile = 64 << 10

// LogOptions bounds a reading of a history.
type LogOptions struct {
	// Max is how many commits to read at most, the domain's cap when zero.
	Max int
	// Skip is how many commits to leave out, which pages the graph.
	Skip int
	// Revs are the revisions to walk: branches, tags, object names or ranges.
	// Empty walks every ref of the repository, HEAD included.
	Revs []string
	// Paths limits the walk to the commits touching them, relative to the top
	// of the working tree.
	Paths []string
	// FirstParent follows the first parent of a merge alone, which reads a
	// branch as the series of merges that built it.
	FirstParent bool
}

// Log reads the history of the repository containing dir. The order is
// topological, as a drawn graph needs it: a commit is never shown before a
// commit that follows it, whatever the dates say.
func (r Runner) Log(ctx context.Context, dir string, opt LogOptions) ([]vcs.Commit, error) {
	maxCount := opt.Max
	if maxCount <= 0 || maxCount > vcs.MaxCommits {
		maxCount = vcs.MaxCommits
	}
	if opt.Skip < 0 {
		return nil, fmt.Errorf("git log: skip %d", opt.Skip)
	}
	args := []string{
		"log", "--decorate=full", "-z", "--format=" + vcs.LogFormat,
		"--topo-order", "--max-count=" + strconv.Itoa(maxCount),
	}
	if opt.Skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(opt.Skip))
	}
	if opt.FirstParent {
		args = append(args, "--first-parent")
	}
	if len(opt.Revs) == 0 {
		args = append(args, "--all")
	}
	for _, rev := range opt.Revs {
		if err := checkRev(rev); err != nil {
			return nil, err
		}
		args = append(args, rev)
	}
	// Paths go after the separator, so a file named like a revision is read as
	// the file it is.
	args = append(args, "--")
	for _, p := range opt.Paths {
		if err := vcs.ValidatePath(p); err != nil {
			return nil, fmt.Errorf("git log: %w", err)
		}
		args = append(args, p)
	}
	out, err := r.git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return vcs.ParseLog(out)
}

// Show reads one commit in full: its message, who wrote and landed it, and
// the files it touched. A merge is read against its first parent, which is
// the change the merge brought into the branch.
func (r Runner) Show(ctx context.Context, dir, rev string) (vcs.CommitDetail, error) {
	if err := checkRev(rev); err != nil {
		return vcs.CommitDetail{}, err
	}
	out, err := r.git(ctx, dir, "show", "-z", "--numstat", "--first-parent",
		"--no-ext-diff", "--no-textconv", "--no-color", "--decorate=full",
		"--format="+vcs.ShowFormat, rev, "--")
	if err != nil {
		return vcs.CommitDetail{}, err
	}
	return vcs.ParseShow(out)
}

// Stashes lists the stashes of the repository containing dir, the one pushed
// last first.
func (r Runner) Stashes(ctx context.Context, dir string) ([]vcs.Stash, error) {
	out, err := r.git(ctx, dir, "stash", "list", "-z", "--format="+vcs.StashFormat)
	if err != nil {
		return nil, err
	}
	return vcs.ParseStashes(out)
}

// InProgress reports the operation the repository containing dir stopped in
// the middle of.
func (r Runner) InProgress(ctx context.Context, dir string) (vcs.InProgress, error) {
	repo, err := r.Repo(ctx, dir)
	if err != nil {
		return vcs.InProgress{}, err
	}
	return ReadInProgress(repo.GitDir)
}

// ReadInProgress reads the state files of a git directory. It runs no
// command, so a view refreshing on a file event pays a handful of small reads
// and nothing else.
func ReadInProgress(gitDir string) (vcs.InProgress, error) {
	files := make(map[string]string, len(vcs.InProgressPaths))
	for _, name := range vcs.InProgressPaths {
		content, ok, err := readStateFile(filepath.Join(gitDir, filepath.FromSlash(name)))
		if err != nil {
			return vcs.InProgress{}, err
		}
		if ok {
			files[name] = content
		}
	}
	return vcs.ParseInProgress(files)
}

// readStateFile reads one file of a git directory. A file that is not there
// is not an error: it is how git says the operation is not running. Anything
// that is not a plain file is refused rather than followed, since the git
// directory of a repository is data like any other.
func readStateFile(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	switch {
	// A directory that is not there (no rebase running) and a directory that
	// is a file instead both mean the same: the operation is not running.
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("git state: %w", err)
	case !info.Mode().IsRegular():
		return "", false, fmt.Errorf("git state: %s is not a plain file", path)
	case info.Size() > maxStateFile:
		return "", false, fmt.Errorf("git state: %s holds %d bytes: %w", path, info.Size(), ErrOutputTooLarge)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git state: %w", err)
	}
	if len(data) > maxStateFile {
		return "", false, fmt.Errorf("git state: %s holds %d bytes: %w", path, len(data), ErrOutputTooLarge)
	}
	return string(data), true, nil
}

// checkRev refuses a revision git would read as an option of its own.
func checkRev(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("git log: revision %q", rev)
	}
	return nil
}
