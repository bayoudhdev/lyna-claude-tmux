package git

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/gittest"
)

// origin gives a repository a bare remote of its own, on the branch name the
// project uses, and returns where it stands. Nothing leaves the machine: the
// remote of a test is another repository in its own temporary directory.
func origin(t *testing.T, r *repo) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(r.Dir), "origin.git")
	r.Git("init", "-q", "--bare", "-b", "main", path)
	r.Git("remote", "add", "origin", path)
	return path
}

// otherClone is a second working tree on the same remote, which is how the
// remote moves under the repository being tested.
func otherClone(t *testing.T, r *repo, remote string) *gittest.Repo {
	t.Helper()
	other := gittest.At(t, "clone")
	r.Git("clone", "-q", "-b", "main", remote, other.Dir)
	return other
}

// remoteRef reads a branch of the bare remote, the empty string when it holds
// none of that name. A push is proved by the repository that received it,
// never by the one that sent it. The second -C moves git into the remote and
// keeps the hermetic environment of the test.
func remoteRef(t *testing.T, r *repo, remote, branch string) string {
	t.Helper()
	out, err := r.Try("-C", remote, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// remoteBranches lists the branches the bare remote holds.
func remoteBranches(t *testing.T, r *repo, remote string) []string {
	t.Helper()
	return strings.Fields(r.Git("-C", remote, "for-each-ref", "--format=%(refname:short)", "refs/heads/"))
}

// localRef is what a ref of the repository under test stands on, the empty
// string when there is no such ref.
func localRef(t *testing.T, r *repo, ref string) string {
	t.Helper()
	out, err := r.Try("rev-parse", "--verify", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// TestIntegrationRemotes reads the remotes of a real repository, one of them
// written somewhere other than where it is read.
func TestIntegrationRemotes(t *testing.T) {
	r := series(t, "first commit subject")
	remote := origin(t, r)
	write := remote + "-write"
	r.Git("init", "-q", "--bare", "-b", "main", write)
	r.Git("remote", "set-url", "--push", "origin", write)
	r.Git("remote", "add", "fork", remote)

	got, err := r.Runner.Remotes(t.Context(), r.Dir)
	if err != nil {
		t.Fatalf("Remotes() error = %v", err)
	}
	byName := map[string]Remote{}
	for _, rm := range got {
		byName[rm.Name] = rm
	}
	want := map[string]Remote{
		"origin": {Name: "origin", FetchURL: remote, PushURL: write},
		"fork":   {Name: "fork", FetchURL: remote, PushURL: remote},
	}
	if !reflect.DeepEqual(byName, want) {
		t.Fatalf("Remotes() = %+v, want %+v", got, want)
	}
}

func TestIntegrationFetch(t *testing.T) {
	ctx := t.Context()

	t.Run("a commit of the remote lands under its refs", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)
		r.Git("push", "-q", "origin", "main")
		other := otherClone(t, r, remote)
		other.Commit("a commit of someone else", "b.txt", "b\n")
		other.Git("push", "-q", "origin", "main")

		if err := r.Runner.Fetch(ctx, r.Dir, Fetch{}); err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		want := strings.TrimSpace(other.Git("rev-parse", "HEAD"))
		if got := localRef(t, r, "refs/remotes/origin/main"); got != want {
			t.Fatalf("refs/remotes/origin/main = %q, want the commit of the remote %q", got, want)
		}
		if got := subjects(t, r); !slices.Equal(got, []string{"first commit subject"}) {
			t.Fatalf("the branch reads %v, want a fetch to have moved nothing", got)
		}
	})

	t.Run("a branch the remote dropped is dropped here", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)
		r.Git("branch", "side")
		r.Git("push", "-q", "origin", "main", "side")
		if err := r.Runner.Fetch(ctx, r.Dir, Fetch{Remote: "origin"}); err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if localRef(t, r, "refs/remotes/origin/side") == "" {
			t.Fatal("the branch of the remote was not read at all")
		}
		r.Git("-C", remote, "update-ref", "-d", "refs/heads/side")

		if err := r.Runner.Fetch(ctx, r.Dir, Fetch{Prune: true}); err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if got := localRef(t, r, "refs/remotes/origin/side"); got != "" {
			t.Fatalf("refs/remotes/origin/side = %q, want it gone with the branch", got)
		}
		if localRef(t, r, "refs/remotes/origin/main") == "" {
			t.Fatal("the branch the remote still holds was dropped as well")
		}
		if !slices.Contains(branchNames(t, r), "side") {
			t.Fatal("the branch of the project was dropped, want only the refs of the remote pruned")
		}
	})
}

func TestIntegrationPull(t *testing.T) {
	ctx := t.Context()

	// behind is a repository whose remote holds a commit it has not got.
	behind := func(t *testing.T) *repo {
		t.Helper()
		r := series(t, "first commit subject")
		remote := origin(t, r)
		r.Git("push", "-q", "-u", "origin", "main")
		other := otherClone(t, r, remote)
		other.Commit("a commit of someone else", "b.txt", "b\n")
		other.Git("push", "-q", "origin", "main")
		return r
	}

	t.Run("a pull that moves the branch forward", func(t *testing.T) {
		r := behind(t)
		if err := r.Runner.Pull(ctx, r.Dir, Pull{Branch: "main", FastForwardOnly: true}); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
		want := []string{"a commit of someone else", "first commit subject"}
		if got := subjects(t, r); !slices.Equal(got, want) {
			t.Fatalf("the branch reads %v, want %v", got, want)
		}
		if got := head(t, r); got != "main" {
			t.Fatalf("the working tree stands on %q, want main", got)
		}
	})

	t.Run("a pull that replays the commits of the branch", func(t *testing.T) {
		r := behind(t)
		r.Commit("a commit of mine", "c.txt", "c\n")
		was := commitNamed(t, r, "a commit of mine")

		if err := r.Runner.Pull(ctx, r.Dir, Pull{Branch: "main", Rebase: true}); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
		want := []string{"a commit of mine", "a commit of someone else", "first commit subject"}
		if got := subjects(t, r); !slices.Equal(got, want) {
			t.Fatalf("the branch reads %v, want %v", got, want)
		}
		if got := commitNamed(t, r, "a commit of mine"); got == was {
			t.Fatalf("the commit still stands at %s, want it replayed over the commit of the remote", got)
		}
	})

	t.Run("a merging pull writes the merge without an editor", func(t *testing.T) {
		r := behind(t)
		r.Commit("a commit of mine", "c.txt", "c\n")

		if err := r.Runner.Pull(ctx, r.Dir, Pull{Branch: "main"}); err != nil {
			t.Fatalf("Pull() error = %v", err)
		}
		got := subjects(t, r)
		if len(got) != 4 || !strings.HasPrefix(got[0], "Merge branch") {
			t.Fatalf("the branch reads %v, want a merge commit over four commits", got)
		}
	})

	t.Run("a pull held to a fast forward stops at a branch of its own", func(t *testing.T) {
		r := behind(t)
		r.Commit("a commit of mine", "c.txt", "c\n")

		if err := r.Runner.Pull(ctx, r.Dir, Pull{Branch: "main", FastForwardOnly: true}); err == nil {
			t.Fatal("the pull went through, want it refused: the branch has a commit of its own")
		}
		want := []string{"a commit of mine", "first commit subject"}
		if got := subjects(t, r); !slices.Equal(got, want) {
			t.Fatalf("the branch reads %v, want %v left as it stood", got, want)
		}
	})
}

func TestIntegrationPush(t *testing.T) {
	ctx := t.Context()

	t.Run("a branch created on the remote", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)
		r.Git("checkout", "-q", "-b", "side")

		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "side"}); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
		if got := remoteBranches(t, r, remote); !slices.Equal(got, []string{"side"}) {
			t.Fatalf("the remote holds %v, want the branch alone", got)
		}
		if got, want := remoteRef(t, r, remote, "side"), localRef(t, r, "refs/heads/side"); got != want {
			t.Fatalf("the remote stands on %q, want the commit pushed %q", got, want)
		}
	})

	t.Run("a push that makes the branch follow the remote", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)

		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "main", SetUpstream: true}); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
		if got := upstreamOf(t, r, "main"); got != "refs/remotes/origin/main" {
			t.Fatalf("main follows %q, want refs/remotes/origin/main", got)
		}
		if remoteRef(t, r, remote, "main") == "" {
			t.Fatal("the remote holds no main, want the branch pushed")
		}
	})

	t.Run("a push the remote moved under", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)
		r.Git("push", "-q", "-u", "origin", "main")
		stale := localRef(t, r, "HEAD")
		other := otherClone(t, r, remote)
		other.Commit("a commit of someone else", "b.txt", "b\n")
		other.Git("push", "-q", "origin", "main")
		r.Commit("a commit of mine", "c.txt", "c\n")
		moved := remoteRef(t, r, remote, "main")

		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "main"}); err == nil {
			t.Fatal("the push went through, want it refused: the remote moved")
		}
		if got := remoteRef(t, r, remote, "main"); got != moved {
			t.Fatalf("the remote stands on %q, want the commit of someone else %q", got, moved)
		}
		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "main", Lease: true, LeaseExpect: stale}); err == nil {
			t.Fatal("the push went through, want it refused: the remote left that commit behind")
		}

		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "main", Lease: true, LeaseExpect: moved}); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
		if got, want := remoteRef(t, r, remote, "main"), localRef(t, r, "HEAD"); got != want {
			t.Fatalf("the remote stands on %q, want the commit pushed %q", got, want)
		}
	})

	t.Run("a branch deleted on the remote", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)
		r.Git("branch", "side")
		r.Git("push", "-q", "origin", "main", "side")

		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "side", Delete: true}); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
		if got := remoteBranches(t, r, remote); !slices.Equal(got, []string{"main"}) {
			t.Fatalf("the remote holds %v, want the deleted branch gone from it", got)
		}
		if !slices.Contains(branchNames(t, r), "side") {
			t.Fatal("the branch of the project was deleted as well")
		}
	})

	t.Run("a push that writes nothing", func(t *testing.T) {
		r := series(t, "first commit subject")
		remote := origin(t, r)

		if err := r.Runner.Push(ctx, r.Dir, Push{Branch: "main", DryRun: true}); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
		if got := remoteBranches(t, r, remote); len(got) != 0 {
			t.Fatalf("the remote holds %v, want a dry run to have written nothing", got)
		}
	})
}
