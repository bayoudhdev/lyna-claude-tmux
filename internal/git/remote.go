package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// DefaultRemote is the remote a command works with when none is named.
const DefaultRemote = "origin"

// Remote is a remote of the repository: the name commands take and the two
// URLs git keeps for it, which differ when pushes go somewhere else.
type Remote struct {
	// Name is what a fetch, a pull or a push names.
	Name string
	// FetchURL is where refs are read from, PushURL where they are written.
	// A remote configured with one URL carries it in both.
	FetchURL string
	PushURL  string
}

// Remotes lists the remotes of the repository containing dir, in the order
// git prints them.
func (r Runner) Remotes(ctx context.Context, dir string) ([]Remote, error) {
	out, err := r.git(ctx, dir, "remote", "-v")
	if err != nil {
		return nil, err
	}
	return parseRemotes(out)
}

// parseRemotes reads what `git remote -v` prints: one line per direction,
// holding the name, a tab, the URL and the direction in parentheses. The two
// lines of a remote are folded into one record. The URL is whatever stands
// between the tab and the direction, since a remote on this machine is a path
// and a path holds spaces.
func parseRemotes(out []byte) ([]Remote, error) {
	var list []Remote
	at := map[string]int{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("%w: remote line %q", vcs.ErrMalformed, line)
		}
		if err := validateRemoteName(name); err != nil {
			return nil, err
		}
		url, push, ok := cutDirection(rest)
		if !ok {
			return nil, fmt.Errorf("%w: remote line %q", vcs.ErrMalformed, line)
		}
		i, seen := at[name]
		if !seen {
			i = len(list)
			at[name] = i
			list = append(list, Remote{Name: name})
		}
		if push {
			list[i].PushURL = url
		} else {
			list[i].FetchURL = url
		}
	}
	return list, nil
}

// cutDirection splits a URL from the direction git prints after it, refusing
// a line that carries neither: a URL of its own may end in parentheses, so
// the suffix is cut from the end and never searched for.
func cutDirection(rest string) (url string, push, ok bool) {
	if u, found := strings.CutSuffix(rest, " (fetch)"); found {
		return u, false, u != ""
	}
	if u, found := strings.CutSuffix(rest, " (push)"); found {
		return u, true, u != ""
	}
	return "", false, false
}

// validateRemoteName holds a remote to the characters a configuration section
// carries, and refuses a leading dash so a name read from a repository or
// typed by a user never reaches a command as an option of its own.
func validateRemoteName(name string) error {
	bad := func() error { return fmt.Errorf("%w: remote name %q", vcs.ErrMalformed, name) }
	if name == "" || strings.HasPrefix(name, "-") {
		return bad()
	}
	for i := range len(name) {
		switch c := name[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return bad()
		}
	}
	return nil
}

// remoteName is the remote a command runs against, the default one when none
// is named.
func remoteName(name string) (string, error) {
	if name == "" {
		return DefaultRemote, nil
	}
	if err := validateRemoteName(name); err != nil {
		return "", err
	}
	return name, nil
}

// validateRefspec accepts a refspec: an optional leading plus, a source, and
// a destination after a colon when the refs land under another name. Each
// side is a ref name, with at most one star standing for what varies.
func validateRefspec(spec string) error {
	sides := strings.SplitN(strings.TrimPrefix(spec, "+"), ":", 2)
	for _, side := range sides {
		if strings.Count(side, "*") > 1 {
			return fmt.Errorf("%w: refspec %q", vcs.ErrMalformed, spec)
		}
		// A ref name carries no star, so the one a pattern holds is checked
		// as the ordinary character it stands for.
		if err := vcs.ValidateRefName(strings.Replace(side, "*", "x", 1)); err != nil {
			return fmt.Errorf("%w: refspec %q", vcs.ErrMalformed, spec)
		}
	}
	return nil
}

// Fetch describes what to bring in from a remote.
type Fetch struct {
	// Remote is the remote to read, the default one when empty.
	Remote string
	// Refspecs are the refs to bring in and where they land, the ones the
	// remote is configured with when empty.
	Refspecs []string
	// All reads every remote of the repository instead of one.
	All bool
	// Prune drops the refs of a remote that the remote itself dropped, which
	// is what tells a branch merged elsewhere from one still being worked on.
	Prune bool
	// Tags brings in every tag of the remote, not only the ones standing on
	// the commits fetched.
	Tags bool
	// Depth cuts the history brought in to that many commits, the whole of it
	// when zero.
	Depth int
}

// Fetch brings refs in from a remote. It moves no branch of the project: what
// it reads lands under the refs of the remote, which is what makes it the
// half of a pull that changes nothing in the working tree.
func (r Runner) Fetch(ctx context.Context, dir string, f Fetch) error {
	args, err := fetchArgs(f)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, args...)
	return err
}

func fetchArgs(f Fetch) ([]string, error) {
	if f.All && (f.Remote != "" || len(f.Refspecs) > 0) {
		return nil, fmt.Errorf("git fetch: every remote is read, so none is named")
	}
	if f.Depth < 0 {
		return nil, fmt.Errorf("git fetch: depth %d", f.Depth)
	}
	args := []string{"fetch"}
	if f.All {
		args = append(args, "--all")
	}
	if f.Prune {
		args = append(args, "--prune")
	}
	if f.Tags {
		args = append(args, "--tags")
	}
	if f.Depth > 0 {
		args = append(args, "--depth="+strconv.Itoa(f.Depth))
	}
	if f.All {
		return args, nil
	}
	name, err := remoteName(f.Remote)
	if err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}
	for _, spec := range f.Refspecs {
		if err := validateRefspec(spec); err != nil {
			return nil, fmt.Errorf("git fetch: %w", err)
		}
	}
	args = append(args, "--", name)
	return append(args, f.Refspecs...), nil
}

// Pull describes bringing a branch of a remote into the branch the working
// tree stands on.
type Pull struct {
	// Remote is the remote to read, the default one when empty.
	Remote string
	// Branch is the branch of the remote to bring in, the one the current
	// branch follows when empty.
	Branch string
	// FastForwardOnly refuses anything but moving the branch forward, which
	// is the pull that can lose nothing.
	FastForwardOnly bool
	// Rebase replays the commits of the branch over what came in instead of
	// merging it.
	Rebase bool
	// AutoStash puts the changes of the working tree away for the time of the
	// pull and brings them back afterwards.
	AutoStash bool
}

// Pull fetches a branch of a remote and brings it into the branch the working
// tree stands on. One that stops on a conflict leaves the repository in the
// middle of it, which InProgress reports and Continue or Abort answers.
func (r Runner) Pull(ctx context.Context, dir string, p Pull) error {
	args, err := pullArgs(p)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, args...)
	return err
}

func pullArgs(p Pull) ([]string, error) {
	if p.FastForwardOnly && p.Rebase {
		return nil, fmt.Errorf("git pull: a pull either replays the commits or only moves forward")
	}
	name, err := remoteName(p.Remote)
	if err != nil {
		return nil, fmt.Errorf("git pull: %w", err)
	}
	if p.Branch != "" {
		if err := vcs.ValidateRefName(p.Branch); err != nil {
			return nil, fmt.Errorf("git pull: %w", err)
		}
	}
	args := []string{"pull"}
	switch {
	case p.FastForwardOnly:
		args = append(args, "--ff-only")
	case p.Rebase:
		args = append(args, "--rebase")
	default:
		// git refuses a pull of divergent branches that does not say how they
		// are reconciled, and it takes the answer from the configuration of
		// the user otherwise; a merge is asked for here or not at all. The
		// runner hands every command an editor that exits at once, and the
		// merge says so in its arguments as well, where the intent is read
		// back from the command itself.
		args = append(args, "--no-rebase", "--no-edit")
	}
	if p.AutoStash {
		args = append(args, "--autostash")
	}
	args = append(args, "--", name)
	if p.Branch != "" {
		args = append(args, p.Branch)
	}
	return args, nil
}

// Push describes what to send to a remote.
type Push struct {
	// Remote is the remote to write to, the default one when empty.
	Remote string
	// Branch is the branch to send, the one the working tree stands on when
	// empty.
	Branch string
	// SetUpstream makes the branch follow the one it lands on, which is how a
	// branch of this project becomes a branch of the remote.
	SetUpstream bool
	// Force writes over whatever the remote holds, losing the commits it has
	// and this repository has not: the caller confirms it, this only runs it.
	Force bool
	// Lease writes over the remote only while it still stands where this
	// repository last saw it, so a commit pushed by someone else in between
	// stops the push.
	Lease bool
	// LeaseExpect is the commit the remote is expected to stand on, which
	// holds the lease to what was actually read rather than to whatever the
	// refs of the remote happen to say.
	LeaseExpect string
	// Tags sends the tags of this repository along.
	Tags bool
	// Delete removes Branch from the remote, leaving the branch here alone.
	Delete bool
	// DryRun reports what the push would do and writes nothing.
	DryRun bool
}

// Push sends a branch to a remote.
func (r Runner) Push(ctx context.Context, dir string, p Push) error {
	args, err := pushArgs(p)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, dir, args...)
	return err
}

func pushArgs(p Push) ([]string, error) {
	if err := checkPush(p); err != nil {
		return nil, err
	}
	name, err := remoteName(p.Remote)
	if err != nil {
		return nil, fmt.Errorf("git push: %w", err)
	}
	if p.Branch != "" {
		if err := vcs.ValidateRefName(p.Branch); err != nil {
			return nil, fmt.Errorf("git push: %w", err)
		}
	}
	args := []string{"push"}
	if p.SetUpstream {
		args = append(args, "--set-upstream")
	}
	switch {
	case p.Force:
		args = append(args, "--force")
	case p.LeaseExpect != "":
		args = append(args, "--force-with-lease="+p.Branch+":"+p.LeaseExpect)
	case p.Lease:
		args = append(args, "--force-with-lease")
	}
	if p.Tags {
		args = append(args, "--tags")
	}
	if p.Delete {
		args = append(args, "--delete")
	}
	if p.DryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--", name)
	if p.Branch != "" {
		args = append(args, p.Branch)
	}
	return args, nil
}

// checkPush refuses the combinations that would send something other than
// what was asked for, before any process starts.
func checkPush(p Push) error {
	if p.Force && p.Lease {
		return fmt.Errorf("git push: a push writes over the remote outright or holds to what it still stands on, not both")
	}
	if p.LeaseExpect != "" {
		switch {
		case !p.Lease:
			return fmt.Errorf("git push: a commit is expected of the remote only when the push holds to it")
		case p.Branch == "":
			return fmt.Errorf("git push: the branch the commit is expected on is not named")
		case !vcs.IsObjectName(p.LeaseExpect):
			return fmt.Errorf("git push: expected commit %q", p.LeaseExpect)
		}
	}
	if p.Delete {
		switch {
		case p.Branch == "":
			return fmt.Errorf("git push: the branch to delete on the remote is not named")
		case p.SetUpstream, p.Tags, p.Force, p.Lease:
			return fmt.Errorf("git push: a branch deleted on the remote is neither followed, nor forced, nor sent with the tags")
		}
	}
	if p.SetUpstream && p.Branch == "" {
		return fmt.Errorf("git push: the branch that follows the remote is not named")
	}
	return nil
}
