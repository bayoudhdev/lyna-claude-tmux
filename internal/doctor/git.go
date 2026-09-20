package doctor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
)

// Identifiers of the rows about the git of a project.
const (
	gitRepositoryID = "git-repository"
	gitOperationID  = "git-operation"
	gitWorktreesID  = "git-worktrees"
	gitHeadID       = "git-head"
)

// Titles of the same rows.
const (
	gitRepositoryTitle = "Project repository"
	gitOperationTitle  = "Git operation"
	gitWorktreesTitle  = "Project worktrees"
	gitHeadTitle       = "Project HEAD"
)

// gitMinMajor and gitMinMinor are the oldest git the workspace runs on.
// `git worktree list --porcelain -z` separates its records with NUL from 2.36
// on, and every worktree the workspace opens is read from that output: an
// older git answers nothing, and the one before NUL records read a path
// holding a newline as two worktrees. The rest of what the workspace runs is
// older than that: `rev-parse --path-format=absolute` (2.31), an interactive
// rebase whose todo list it writes through a sequence editor, and
// `push --force-with-lease=<ref>:<expect>` (1.8.5).
const (
	gitMinMajor = 2
	gitMinMinor = 36
)

// gitReader is the reading side of git a project report needs. git.Runner
// implements it. Nothing here writes: a doctor that repaired a repository
// would change the very thing it is reporting on, so every row names the
// command to run and none of them carries an Action for `doctor --fix`.
type gitReader interface {
	Repo(ctx context.Context, dir string) (git.Repo, error)
	InProgress(ctx context.Context, dir string) (vcs.InProgress, error)
	Worktrees(ctx context.Context, dir string) ([]vcs.Worktree, error)
}

// checkGitProject reports the git of the project the report is about: the
// working tree the project directory sits in, an operation left in the
// middle, the worktrees git records against the directory the workspace opens
// them in, and where HEAD stands. The git row above has already reported the
// binary and its version, so nothing here repeats them.
func checkGitProject(ctx context.Context, d Deps) []Result {
	path, err := d.LookPath("git")
	if err != nil {
		return nil
	}
	if d.ProjectDir == "" {
		return []Result{{
			ID: gitRepositoryID, Title: gitRepositoryTitle, Status: StatusSkip, Detail: "no project directory",
		}}
	}
	// The runner keeps a reading free of side effects: no pager, no optional
	// index lock and an environment that cannot point -C at another
	// repository.
	return gitProject(ctx, d, git.Runner{Bin: path, Timeout: d.Timeout})
}

// gitProject reports the working tree the project directory sits in. Each
// reading is taken once: the worktree list says both what git records and
// where HEAD stands, and what git is in the middle of explains a HEAD that is
// detached on purpose.
func gitProject(ctx context.Context, d Deps, r gitReader) []Result {
	repo, err := r.Repo(ctx, d.ProjectDir)
	if err != nil {
		return []Result{gitRepoError(d, err)}
	}
	p, progErr := r.InProgress(ctx, d.ProjectDir)
	list, listErr := r.Worktrees(ctx, d.ProjectDir)
	return []Result{
		gitRepositoryRow(repo),
		gitOperationRow(p, progErr),
		gitWorktreesRow(d, repo, list, listErr),
		gitHeadRow(repo, list, listErr, p, progErr),
	}
}

// gitRepoError reports a project directory git answers nothing about.
func gitRepoError(d Deps, err error) Result {
	r := Result{ID: gitRepositoryID, Title: gitRepositoryTitle, Status: StatusWarn}
	if errors.Is(err, git.ErrNotRepository) {
		r.Detail = d.ProjectDir + " is inside no git working tree, so the changes pane, the review and the worktrees of the workspace stay empty"
		r.Fix = "git init"
		return r
	}
	r.Detail = "the working tree of " + d.ProjectDir + " cannot be read: " + err.Error()
	r.Fix = "git status (run it in the project directory to see what git reports)"
	return r
}

// gitRepositoryRow says which working tree the project directory sits in and
// whether it is the one the repository was cloned into or one opened beside
// it. A linked worktree shares its refs and its objects with the main one, so
// what a command does there is seen from the other.
func gitRepositoryRow(repo git.Repo) Result {
	r := Result{ID: gitRepositoryID, Title: gitRepositoryTitle, Status: StatusOK}
	if repo.GitDir == repo.CommonDir {
		r.Detail = repo.Root + " is the main worktree of the repository"
		return r
	}
	r.Detail = repo.Root + " is a linked worktree; the repository it shares is at " + repo.CommonDir
	return r
}

// gitOperationRow reports an operation the repository stopped in the middle
// of, which holds the branch where it is and makes most commands refuse.
func gitOperationRow(p vcs.InProgress, err error) Result {
	res := Result{ID: gitOperationID, Title: gitOperationTitle}
	if err != nil {
		res.Status = StatusWarn
		res.Detail = "what git is in the middle of cannot be read: " + err.Error()
		res.Fix = "git status (run it in the project directory to see what git reports)"
		return res
	}
	if !p.Running() {
		res.Status, res.Detail = StatusOK, "no rebase, merge, cherry pick, revert or bisection is in the middle"
		return res
	}
	name, fix := gitOperation(p.Kind)
	var b strings.Builder
	b.WriteString(name + " is in the middle")
	if p.Branch != "" {
		b.WriteString(" of " + p.Branch)
	}
	if p.Total > 0 {
		fmt.Fprintf(&b, ", at step %d of %d", p.Step, p.Total)
	}
	if len(p.Heads) > 0 {
		b.WriteString(", stopped on " + gitShort(p.Heads[0]))
	}
	if p.Bisecting && p.Kind != vcs.OperationBisect {
		b.WriteString("; a bisection is running as well")
	}
	res.Status, res.Detail, res.Fix = StatusWarn, b.String(), fix
	return res
}

// gitOperation names an operation left in the middle and the commands that
// end it. Carrying on comes first and throwing the work away last, since one
// of them cannot be undone.
func gitOperation(k vcs.Operation) (name, fix string) {
	switch k {
	case vcs.OperationMerge:
		return "a merge", "git merge --continue (or git merge --abort to put the branch back)"
	case vcs.OperationRebase:
		return "a rebase", "git rebase --continue (git rebase --skip leaves this commit out, git rebase --abort puts the branch back)"
	case vcs.OperationApply:
		return "a patch application", "git am --continue (git am --skip leaves this patch out, git am --abort puts the branch back)"
	case vcs.OperationCherryPick:
		return "a cherry pick", "git cherry-pick --continue (git cherry-pick --skip leaves this commit out, git cherry-pick --abort puts the branch back)"
	case vcs.OperationRevert:
		return "a revert", "git revert --continue (git revert --skip leaves this commit out, git revert --abort puts the branch back)"
	case vcs.OperationBisect:
		return "a bisection", "git bisect good or git bisect bad to carry on (git bisect reset puts HEAD back where it started)"
	}
	return "an operation (" + k.String() + ")", "git status (run it in the project directory to see what git reports)"
}

// gitWorktreesRow reports where the workspace opens the worktrees of the
// project and the ones git still records although their directory is gone,
// which is what keeps a name from being reused.
func gitWorktreesRow(d Deps, repo git.Repo, list []vcs.Worktree, err error) Result {
	r := Result{ID: gitWorktreesID, Title: gitWorktreesTitle}
	if err != nil {
		r.Status = StatusWarn
		r.Detail = "the worktrees of the repository cannot be read: " + err.Error()
		r.Fix = "git worktree list (run it in the project directory to see what git reports)"
		return r
	}
	dir := filepath.Join(repo.Root, filepath.FromSlash(vcs.WorktreeDir))
	var gone []string
	for _, w := range list {
		// git marks a worktree it no longer finds; one whose directory went
		// away since git last looked is caught by the reading of the disk.
		if w.Prunable || !d.Exists(w.Path) {
			gone = append(gone, w.Path)
		}
	}
	if len(gone) > 0 {
		r.Status = StatusWarn
		r.Detail = fmt.Sprintf("git records %s whose directory is gone: %s; the workspace opens its own under %s",
			gitWorktrees(len(gone)), strings.Join(gone, ", "), dir)
		r.Fix = "git worktree prune"
		return r
	}
	r.Status = StatusOK
	r.Detail = fmt.Sprintf("%s, opened under %s", gitWorktrees(len(list)), dir)
	return r
}

// gitHeadRow reports where HEAD stands in the working tree the report is
// about, which the worktree list already says. An operation left in the
// middle detaches HEAD itself, so it is reported once, by the row that also
// says how to end it.
func gitHeadRow(repo git.Repo, list []vcs.Worktree, listErr error, p vcs.InProgress, progErr error) Result {
	r := Result{ID: gitHeadID, Title: gitHeadTitle}
	if listErr != nil {
		r.Status, r.Detail = StatusSkip, "the worktrees of the repository cannot be read, so where HEAD stands is unknown"
		return r
	}
	w, ok := gitWorktreeAt(list, repo.Root)
	running := progErr == nil && p.Running()
	switch {
	case !ok:
		r.Status = StatusWarn
		r.Detail = repo.Root + " is not among the worktrees git records for this repository"
		r.Fix = "git worktree repair"
	case w.Detached && running:
		name, _ := gitOperation(p.Kind)
		r.Status = StatusSkip
		r.Detail = "HEAD is detached at " + gitShort(w.Head) + ", which is how git carries " + name + " out; the " +
			gitOperationTitle + " row says how to end it"
	case w.Detached:
		r.Status = StatusWarn
		r.Detail = "HEAD is detached at " + gitShort(w.Head) + ", so a commit made here belongs to no branch and is lost once HEAD moves"
		r.Fix = "git switch -c <name> keeps what is here on a branch of its own, git switch <branch> goes back to one"
	case gitUnborn(w.Head):
		r.Status, r.Detail = StatusOK, "on branch "+w.Name()+", which has no commit yet"
	default:
		r.Status, r.Detail = StatusOK, "on branch "+w.Name()+" at "+gitShort(w.Head)
	}
	return r
}

// gitUnborn reports a branch with no commit yet, which git lists with a head
// of nothing but zeros.
func gitUnborn(oid string) bool { return strings.Trim(oid, "0") == "" }

// gitWorktreeAt finds the worktree of a working tree root. git prints the
// path it recorded and rev-parse prints the one it resolved, so both are
// cleaned before they are compared.
func gitWorktreeAt(list []vcs.Worktree, root string) (vcs.Worktree, bool) {
	want := filepath.Clean(root)
	for _, w := range list {
		if filepath.Clean(w.Path) == want {
			return w, true
		}
	}
	return vcs.Worktree{}, false
}

// gitShort abbreviates an object name to the seven characters git itself
// shows by default.
func gitShort(oid string) string {
	if len(oid) < 7 {
		return oid
	}
	return oid[:7]
}

// gitWorktrees counts worktrees for a sentence.
func gitWorktrees(n int) string {
	if n == 1 {
		return "1 worktree"
	}
	return strconv.Itoa(n) + " worktrees"
}
