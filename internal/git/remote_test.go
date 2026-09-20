package git

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// TestRunnerRemotes reads the remotes of a repository through the fake, which
// holds both the command and what its output is turned into.
func TestRunnerRemotes(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		err     error
		want    []Remote
		wantErr bool
	}{
		{
			name: "a remote read and written at the same URL",
			out:  "origin\t/tmp/o.git (fetch)\norigin\t/tmp/o.git (push)\n",
			want: []Remote{{Name: "origin", FetchURL: "/tmp/o.git", PushURL: "/tmp/o.git"}},
		},
		{
			name: "no remote at all",
			out:  "",
			want: nil,
		},
		{
			name:    "output that is no remote",
			out:     "origin /tmp/o.git (fetch)\n",
			wantErr: true,
		},
		{
			name:    "a command that failed",
			err:     ErrNotInstalled,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{"remote": {Stdout: []byte(tc.out)}}, err: tc.err}
			got, err := Runner{Executor: f}.Remotes(context.Background(), "/repo")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Remotes() = %+v, want it refused", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Remotes() error = %v", err)
			}
			if args := f.calls[0][5:]; !slices.Equal(args, []string{"remote", "-v"}) {
				t.Fatalf("the command ran %v, want the remotes read", args)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Remotes() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRemotes(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    []Remote
		wantErr bool
	}{
		{
			name: "one remote",
			out:  "origin\t/srv/o.git (fetch)\norigin\t/srv/o.git (push)\n",
			want: []Remote{{Name: "origin", FetchURL: "/srv/o.git", PushURL: "/srv/o.git"}},
		},
		{
			name: "a remote whose pushes go elsewhere",
			out:  "origin\t/srv/read.git (fetch)\norigin\t/srv/write.git (push)\n",
			want: []Remote{{Name: "origin", FetchURL: "/srv/read.git", PushURL: "/srv/write.git"}},
		},
		{
			name: "two remotes in the order git printed them",
			out: "upstream\t/srv/u.git (fetch)\nupstream\t/srv/u.git (push)\n" +
				"origin\t/srv/o.git (fetch)\norigin\t/srv/o.git (push)\n",
			want: []Remote{
				{Name: "upstream", FetchURL: "/srv/u.git", PushURL: "/srv/u.git"},
				{Name: "origin", FetchURL: "/srv/o.git", PushURL: "/srv/o.git"},
			},
		},
		{
			name: "a path holding spaces and parentheses",
			out:  "origin\t/srv/my repo (mirror).git (fetch)\n",
			want: []Remote{{Name: "origin", FetchURL: "/srv/my repo (mirror).git"}},
		},
		{
			name: "a remote git printed for one direction only",
			out:  "origin\t/srv/o.git (push)\n",
			want: []Remote{{Name: "origin", PushURL: "/srv/o.git"}},
		},
		{
			name: "a name holding the characters a section carries",
			out:  "fork-2.x_b\t/srv/f.git (fetch)\n",
			want: []Remote{{Name: "fork-2.x_b", FetchURL: "/srv/f.git"}},
		},
		{
			name: "no remote at all",
			out:  "",
			want: nil,
		},
		{
			name:    "a line with no tab",
			out:     "origin /srv/o.git (fetch)\n",
			wantErr: true,
		},
		{
			name:    "a line with no direction",
			out:     "origin\t/srv/o.git\n",
			wantErr: true,
		},
		{
			name:    "a remote with no URL",
			out:     "origin\t (fetch)\n",
			wantErr: true,
		},
		{
			name:    "a name that would be an option",
			out:     "--upload-pack=id\t/srv/o.git (fetch)\n",
			wantErr: true,
		},
		{
			name:    "a name no section carries",
			out:     "org/in\t/srv/o.git (fetch)\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRemotes([]byte(tc.out))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRemotes() = %+v, want it refused", got)
				}
				if !errors.Is(err, vcs.ErrMalformed) {
					t.Fatalf("parseRemotes() error = %v, want it malformed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRemotes() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseRemotes() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// renderRemotes writes remotes back the way git prints them, which is what
// the round trip of the fuzz target is checked against.
func renderRemotes(list []Remote) []byte {
	var b strings.Builder
	for _, rm := range list {
		if rm.FetchURL != "" {
			fmt.Fprintf(&b, "%s\t%s (fetch)\n", rm.Name, rm.FetchURL)
		}
		if rm.PushURL != "" {
			fmt.Fprintf(&b, "%s\t%s (push)\n", rm.Name, rm.PushURL)
		}
	}
	return []byte(b.String())
}

func FuzzParseRemotes(f *testing.F) {
	f.Add("origin\t/srv/o.git (fetch)\norigin\t/srv/o.git (push)\n")
	f.Add("origin\t/srv/my repo (mirror).git (fetch)\n")
	f.Add("up\tu (push)\norigin\to (fetch)\n")
	f.Add("origin /srv/o.git (fetch)\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, out string) {
		list, err := parseRemotes([]byte(out))
		if err != nil {
			if len(list) != 0 {
				t.Fatalf("parseRemotes(%q) = %+v with error %v, want nothing", out, list, err)
			}
			return
		}
		seen := map[string]bool{}
		for _, rm := range list {
			if err := validateRemoteName(rm.Name); err != nil {
				t.Fatalf("parseRemotes(%q) kept the name %q: %v", out, rm.Name, err)
			}
			if rm.FetchURL == "" && rm.PushURL == "" {
				t.Fatalf("parseRemotes(%q) kept %q with no URL at all", out, rm.Name)
			}
			if seen[rm.Name] {
				t.Fatalf("parseRemotes(%q) = %+v, want one record per remote", out, list)
			}
			seen[rm.Name] = true
		}
		again, err := parseRemotes(renderRemotes(list))
		if err != nil {
			t.Fatalf("parseRemotes(%q) written back is refused: %v", out, err)
		}
		if !reflect.DeepEqual(list, again) {
			t.Fatalf("parseRemotes(%q) = %+v, written back and read = %+v", out, list, again)
		}
	})
}

// TestRunnerFetchArgv holds the command a fetch builds, and proves that what
// it refuses starts no process at all.
func TestRunnerFetchArgv(t *testing.T) {
	cases := []struct {
		name    string
		fetch   Fetch
		want    []string
		wantErr bool
	}{
		{
			name:  "a fetch of the remote nothing named",
			fetch: Fetch{},
			want:  []string{"fetch", "--", "origin"},
		},
		{
			name:  "a fetch of a named remote",
			fetch: Fetch{Remote: "upstream"},
			want:  []string{"fetch", "--", "upstream"},
		},
		{
			name:  "a fetch that drops what the remote dropped",
			fetch: Fetch{Prune: true},
			want:  []string{"fetch", "--prune", "--", "origin"},
		},
		{
			name:  "a fetch of every remote",
			fetch: Fetch{All: true, Prune: true},
			want:  []string{"fetch", "--all", "--prune"},
		},
		{
			name:  "a fetch of the tags",
			fetch: Fetch{Tags: true},
			want:  []string{"fetch", "--tags", "--", "origin"},
		},
		{
			name:  "a fetch of the last commits alone",
			fetch: Fetch{Depth: 5},
			want:  []string{"fetch", "--depth=5", "--", "origin"},
		},
		{
			name: "a fetch of refspecs of its own",
			fetch: Fetch{
				Remote:   "origin",
				Refspecs: []string{"refs/heads/*:refs/remotes/origin/*", "+main:refs/remotes/origin/main"},
			},
			want: []string{"fetch", "--", "origin", "refs/heads/*:refs/remotes/origin/*", "+main:refs/remotes/origin/main"},
		},
		{
			name:    "a fetch of every remote and of one of them",
			fetch:   Fetch{All: true, Remote: "origin"},
			wantErr: true,
		},
		{
			name:    "a fetch of every remote with refspecs of one",
			fetch:   Fetch{All: true, Refspecs: []string{"main"}},
			wantErr: true,
		},
		{
			name:    "a remote name that would be an option",
			fetch:   Fetch{Remote: "--upload-pack=id"},
			wantErr: true,
		},
		{
			name:    "a remote name no section carries",
			fetch:   Fetch{Remote: "org/in"},
			wantErr: true,
		},
		{
			name:    "a refspec that would be an option",
			fetch:   Fetch{Refspecs: []string{"-x:refs/remotes/origin/x"}},
			wantErr: true,
		},
		{
			name:    "a refspec standing for two things at once",
			fetch:   Fetch{Refspecs: []string{"refs/*/*:refs/remotes/origin/*"}},
			wantErr: true,
		},
		{
			name:    "a refspec with nothing on one side",
			fetch:   Fetch{Refspecs: []string{":refs/remotes/origin/x"}},
			wantErr: true,
		},
		{
			name:    "a depth that is no depth",
			fetch:   Fetch{Depth: -1},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{"fetch": {}}}
			assertArgv(t, f, Runner{Executor: f}.Fetch(context.Background(), "/repo", tc.fetch), tc.want, tc.wantErr)
		})
	}
}

// TestRunnerPullArgv holds the command a pull builds. A merging pull carries
// --no-edit, so nothing ever waits for an editor.
func TestRunnerPullArgv(t *testing.T) {
	cases := []struct {
		name    string
		pull    Pull
		want    []string
		wantErr bool
	}{
		{
			name: "a pull of what the branch follows",
			pull: Pull{},
			want: []string{"pull", "--no-rebase", "--no-edit", "--", "origin"},
		},
		{
			name: "a pull of a named branch",
			pull: Pull{Branch: "main"},
			want: []string{"pull", "--no-rebase", "--no-edit", "--", "origin", "main"},
		},
		{
			name: "a pull held to a fast forward",
			pull: Pull{Remote: "upstream", Branch: "main", FastForwardOnly: true},
			want: []string{"pull", "--ff-only", "--", "upstream", "main"},
		},
		{
			name: "a pull that replays the commits of the branch",
			pull: Pull{Rebase: true, AutoStash: true},
			want: []string{"pull", "--rebase", "--autostash", "--", "origin"},
		},
		{
			name: "a merging pull that puts the changes away first",
			pull: Pull{AutoStash: true},
			want: []string{"pull", "--no-rebase", "--no-edit", "--autostash", "--", "origin"},
		},
		{
			name:    "a pull that both replays and only moves forward",
			pull:    Pull{Rebase: true, FastForwardOnly: true},
			wantErr: true,
		},
		{
			name:    "a branch name that would be an option",
			pull:    Pull{Branch: "--exec=id"},
			wantErr: true,
		},
		{
			name:    "a branch name git refuses",
			pull:    Pull{Branch: "feat/.x"},
			wantErr: true,
		},
		{
			name:    "a remote name that would be an option",
			pull:    Pull{Remote: "-o"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{"pull": {}}}
			assertArgv(t, f, Runner{Executor: f}.Pull(context.Background(), "/repo", tc.pull), tc.want, tc.wantErr)
		})
	}
}

// TestRunnerPushArgv holds the command a push builds, the lease among them:
// forcing outright and holding to what the remote stands on are two different
// commands, and asking for both starts none.
func TestRunnerPushArgv(t *testing.T) {
	cases := []struct {
		name    string
		push    Push
		want    []string
		wantErr bool
	}{
		{
			name: "a push of the branch the working tree stands on",
			push: Push{},
			want: []string{"push", "--", "origin"},
		},
		{
			name: "a push that makes the branch follow the remote",
			push: Push{Branch: "feat/x", SetUpstream: true},
			want: []string{"push", "--set-upstream", "--", "origin", "feat/x"},
		},
		{
			name: "a push written over the remote",
			push: Push{Remote: "upstream", Branch: "main", Force: true},
			want: []string{"push", "--force", "--", "upstream", "main"},
		},
		{
			name: "a push held to where the remote was last seen",
			push: Push{Branch: "main", Lease: true},
			want: []string{"push", "--force-with-lease", "--", "origin", "main"},
		},
		{
			name: "a push held to a commit of the remote",
			push: Push{Branch: "main", Lease: true, LeaseExpect: hashA},
			want: []string{"push", "--force-with-lease=main:" + hashA, "--", "origin", "main"},
		},
		{
			name: "a push of the tags",
			push: Push{Tags: true},
			want: []string{"push", "--tags", "--", "origin"},
		},
		{
			name: "a branch deleted on the remote",
			push: Push{Branch: "side", Delete: true},
			want: []string{"push", "--delete", "--", "origin", "side"},
		},
		{
			name: "a push that writes nothing",
			push: Push{Branch: "main", DryRun: true},
			want: []string{"push", "--dry-run", "--", "origin", "main"},
		},
		{
			name:    "a push both forced and held to the remote",
			push:    Push{Branch: "main", Force: true, Lease: true},
			wantErr: true,
		},
		{
			name:    "a commit expected of a push that holds to nothing",
			push:    Push{Branch: "main", LeaseExpect: hashA},
			wantErr: true,
		},
		{
			name:    "a commit expected on no branch",
			push:    Push{Lease: true, LeaseExpect: hashA},
			wantErr: true,
		},
		{
			name:    "an expected commit that is no commit",
			push:    Push{Branch: "main", Lease: true, LeaseExpect: "main"},
			wantErr: true,
		},
		{
			name:    "a deletion of no branch",
			push:    Push{Delete: true},
			wantErr: true,
		},
		{
			name:    "a deletion that also follows the remote",
			push:    Push{Branch: "side", Delete: true, SetUpstream: true},
			wantErr: true,
		},
		{
			name:    "a deletion that also sends the tags",
			push:    Push{Branch: "side", Delete: true, Tags: true},
			wantErr: true,
		},
		{
			name:    "a deletion written over the remote",
			push:    Push{Branch: "side", Delete: true, Force: true},
			wantErr: true,
		},
		{
			name:    "no branch to follow the remote",
			push:    Push{SetUpstream: true},
			wantErr: true,
		},
		{
			name:    "a remote name that would be an option",
			push:    Push{Remote: "--receive-pack=id"},
			wantErr: true,
		},
		{
			name:    "a branch name that would be an option",
			push:    Push{Branch: "-f"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{"push": {}}}
			assertArgv(t, f, Runner{Executor: f}.Push(context.Background(), "/repo", tc.push), tc.want, tc.wantErr)
		})
	}
}

// assertArgv holds what the fake recorded against the command expected. A
// refusal has to leave the fake untouched: git errors on its own, so only a
// command that never ran proves the guard is in the argv builder.
func assertArgv(t *testing.T, f *fakeGit, err error, want []string, wantErr bool) {
	t.Helper()
	if wantErr {
		if err == nil {
			t.Fatal("the command went through, want it refused")
		}
		if len(f.calls) != 0 {
			t.Fatalf("the command ran %v, want nothing run at all", f.calls)
		}
		return
	}
	if err != nil {
		t.Fatalf("the command failed: %v", err)
	}
	if args := f.calls[0][5:]; !slices.Equal(args, want) {
		t.Fatalf("the command ran %v, want %v", args, want)
	}
}
