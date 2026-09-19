package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nul joins fields the way git terminates them with -z.
func nul(fields ...string) []byte {
	if len(fields) == 0 {
		return nil
	}
	return []byte(strings.Join(fields, "\x00") + "\x00")
}

const wtHead = "ed1be060c5ffb537563aa2b5a6b414e921d7d60b"

func TestParseWorktrees(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want []Worktree
		// wantErr expects a malformed reading.
		wantErr bool
	}{
		{name: "no output at all", data: nil},
		{
			name: "the project alone",
			data: nul("worktree /projects/acme-api", "HEAD "+wtHead, "branch refs/heads/main", ""),
			want: []Worktree{{Path: "/projects/acme-api", Head: wtHead, Branch: "refs/heads/main"}},
		},
		{
			name: "a worktree on a branch and one detached",
			data: nul(
				"worktree /projects/acme-api", "HEAD "+wtHead, "branch refs/heads/main", "",
				"worktree /projects/acme-api/.claude/worktrees/task-a", "HEAD "+wtHead, "branch refs/heads/task-a", "",
				"worktree /projects/detached", "HEAD "+wtHead, "detached", "",
			),
			want: []Worktree{
				{Path: "/projects/acme-api", Head: wtHead, Branch: "refs/heads/main"},
				{Path: "/projects/acme-api/.claude/worktrees/task-a", Head: wtHead, Branch: "refs/heads/task-a"},
				{Path: "/projects/detached", Head: wtHead, Detached: true},
			},
		},
		{
			name: "locked and prunable carry their reason",
			data: nul(
				"worktree /projects/held", "HEAD "+wtHead, "detached", "locked held by a test", "",
				"worktree /projects/gone", "HEAD "+wtHead, "branch refs/heads/task-c", "prunable gitdir file points to non-existent location", "",
			),
			want: []Worktree{
				{Path: "/projects/held", Head: wtHead, Detached: true, Locked: true, LockReason: "held by a test"},
				{
					Path: "/projects/gone", Head: wtHead, Branch: "refs/heads/task-c",
					Prunable: true, PruneReason: "gitdir file points to non-existent location",
				},
			},
		},
		{
			name: "a lock with no reason",
			data: nul("worktree /projects/held", "HEAD "+wtHead, "detached", "locked", ""),
			want: []Worktree{{Path: "/projects/held", Head: wtHead, Detached: true, Locked: true}},
		},
		{
			name: "a bare repository opens the list",
			data: nul("worktree /projects/acme.git", "bare", "", "worktree /projects/work", "HEAD "+wtHead, "branch refs/heads/main", ""),
			want: []Worktree{
				{Path: "/projects/acme.git", Bare: true},
				{Path: "/projects/work", Head: wtHead, Branch: "refs/heads/main"},
			},
		},
		{
			name: "a branch with no commit yet",
			data: nul("worktree /projects/fresh", "branch refs/heads/main", ""),
			want: []Worktree{{Path: "/projects/fresh", Branch: "refs/heads/main"}},
		},
		{
			name: "an attribute a newer git added",
			data: nul("worktree /projects/acme-api", "HEAD "+wtHead, "branch refs/heads/main", "sparse", "what-git-adds next", ""),
			want: []Worktree{{Path: "/projects/acme-api", Head: wtHead, Branch: "refs/heads/main"}},
		},
		{
			name: "the last worktree ends without a blank field",
			data: nul("worktree /projects/acme-api", "HEAD "+wtHead, "branch refs/heads/main"),
			want: []Worktree{{Path: "/projects/acme-api", Head: wtHead, Branch: "refs/heads/main"}},
		},
		{name: "an attribute before any worktree", data: nul("HEAD "+wtHead, ""), wantErr: true},
		{name: "a worktree with no path", data: nul("worktree", "HEAD "+wtHead, ""), wantErr: true},
		{name: "a head that is not an object name", data: nul("worktree /p", "HEAD nothex", ""), wantErr: true},
		{name: "a branch with no ref", data: nul("worktree /p", "branch", ""), wantErr: true},
		{name: "a ref that is only its namespace", data: nul("worktree /p", "branch refs/heads/", ""), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseWorktrees(tc.data)
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseWorktrees() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseWorktrees() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseWorktrees() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseWorktrees() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("worktree %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseWorktreesReadsGitItself reads the output of a real `git worktree
// list --porcelain -z`, captured from a project with a branch, a detached and
// locked worktree and one whose directory was deleted.
func TestParseWorktreesReadsGitItself(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "worktree-list.z"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWorktrees(data)
	if err != nil {
		t.Fatalf("ParseWorktrees() error = %v", err)
	}
	want := []Worktree{
		{Path: "/projects/acme-api/repo", Head: wtHead, Branch: "refs/heads/main"},
		{Path: "/projects/acme-api/wt-a", Head: wtHead, Branch: "refs/heads/task-a"},
		{Path: "/projects/acme-api/wt-b", Head: wtHead, Detached: true, Locked: true, LockReason: "held by a test"},
		{
			Path: "/projects/acme-api/wt-c", Head: wtHead, Branch: "refs/heads/task-c",
			Prunable: true, PruneReason: "gitdir file points to non-existent location",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseWorktrees() read %d worktrees, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("worktree %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseWorktreesCapsTheList(t *testing.T) {
	var fields []string
	for range MaxWorktrees + 10 {
		fields = append(fields, "worktree /projects/w", "HEAD "+wtHead, "")
	}
	got, err := ParseWorktrees(nul(fields...))
	if err != nil {
		t.Fatalf("ParseWorktrees() error = %v", err)
	}
	if len(got) != MaxWorktrees {
		t.Fatalf("ParseWorktrees() read %d worktrees, want the cap %d", len(got), MaxWorktrees)
	}
}

func TestWorktreeName(t *testing.T) {
	cases := []struct {
		name string
		wt   Worktree
		want string
	}{
		{name: "a branch", wt: Worktree{Branch: "refs/heads/task-a"}, want: "task-a"},
		{name: "a branch with slashes", wt: Worktree{Branch: "refs/heads/feat/git"}, want: "feat/git"},
		{name: "a ref of another namespace", wt: Worktree{Branch: "refs/tags/v1.1.0"}, want: "refs/tags/v1.1.0"},
		{name: "detached", wt: Worktree{Path: "/projects/acme-api/wt-b"}, want: "wt-b"},
		{name: "a path with a trailing slash", wt: Worktree{Path: "/projects/acme-api/wt-b/"}, want: "wt-b"},
		{name: "a path of one element", wt: Worktree{Path: "acme-api"}, want: "acme-api"},
		{name: "the root of a file system", wt: Worktree{Path: "//"}, want: ""},
		{name: "nothing at all", wt: Worktree{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.wt.Name(); got != tc.want {
				t.Fatalf("Name() = %q, want %q", got, tc.want)
			}
		})
	}
}

func FuzzParseWorktrees(f *testing.F) {
	f.Add(nul("worktree /projects/acme-api", "HEAD "+wtHead, "branch refs/heads/main", ""))
	f.Add(nul("worktree /p", "detached", "locked held by a test", ""))
	f.Add(nul("worktree /p", "prunable gitdir file points to non-existent location"))
	f.Add([]byte("worktree\x00\x00\x00"))
	f.Add([]byte("worktree 0\x00branch refs/heads/"))
	f.Add([]byte("worktree //"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseWorktrees(data)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseWorktrees() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if len(got) > MaxWorktrees {
			t.Fatalf("ParseWorktrees() read %d worktrees, past the cap %d", len(got), MaxWorktrees)
		}
		for _, w := range got {
			if w.Path == "" {
				t.Fatalf("worktree with no path: %+v", w)
			}
			if w.Head != "" && !isHex(w.Head) {
				t.Fatalf("worktree head %q is not an object name", w.Head)
			}
			if !w.Locked && w.LockReason != "" {
				t.Fatalf("a worktree that is not locked carries a reason: %+v", w)
			}
			if !w.Prunable && w.PruneReason != "" {
				t.Fatalf("a worktree that is not prunable carries a reason: %+v", w)
			}
			// Only a path made of separators, which git never lists, leaves
			// a worktree with nothing to call it.
			if w.Name() == "" && strings.Trim(w.Path, "/") != "" {
				t.Fatalf("worktree %+v has no name", w)
			}
		}
	})
}
