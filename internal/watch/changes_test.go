package watch

import (
	"reflect"
	"testing"
)

func TestBuild(t *testing.T) {
	branch := Branch{Head: "main", OID: hashA}
	cases := []struct {
		name     string
		status   Status
		unstaged []NumStat
		staged   []NumStat
		want     Changes
	}{
		{
			name: "staged and unstaged counts add up, sorted by path",
			status: Status{Branch: branch, Entries: []Entry{
				{Kind: KindUntracked, Index: '?', Worktree: '?', Path: "z.txt"},
				{Kind: KindChanged, Index: 'M', Worktree: 'M', Path: "b.go"},
				{Kind: KindRenamed, Index: 'R', Worktree: '.', Path: "a new.go", OrigPath: "a.go"},
				{Kind: KindChanged, Index: '.', Worktree: 'M', Path: "img.png"},
			}},
			unstaged: []NumStat{{Path: "b.go", Added: 2, Deleted: 1}, {Path: "img.png", Binary: true}},
			staged:   []NumStat{{Path: "b.go", Added: 10}, {Path: "a new.go", OrigPath: "a.go", Added: 1, Deleted: 1}},
			want: Changes{
				Branch: branch,
				Files: []File{
					{Kind: KindRenamed, Path: "a new.go", OrigPath: "a.go", Index: 'R', Worktree: '.', Added: 1, Deleted: 1},
					{Kind: KindChanged, Path: "b.go", Index: 'M', Worktree: 'M', Added: 12, Deleted: 1},
					{Kind: KindChanged, Path: "img.png", Index: '.', Worktree: 'M', Binary: true},
					{Kind: KindUntracked, Path: "z.txt", Index: '?', Worktree: '?'},
				},
				Added:   13,
				Deleted: 2,
			},
		},
		{
			name:   "clean tree",
			status: Status{Branch: branch},
			want:   Changes{Branch: branch, Files: []File{}},
		},
		{
			name: "same path twice keeps both in stable order",
			status: Status{Entries: []Entry{
				{Kind: KindCopied, Index: 'C', Worktree: '.', Path: "x", OrigPath: "b"},
				{Kind: KindCopied, Index: 'C', Worktree: '.', Path: "x", OrigPath: "a"},
			}},
			want: Changes{Files: []File{
				{Kind: KindCopied, Path: "x", OrigPath: "a", Index: 'C', Worktree: '.'},
				{Kind: KindCopied, Path: "x", OrigPath: "b", Index: 'C', Worktree: '.'},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(tc.status, tc.unstaged, tc.staged)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Build =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestFileStagedUnstaged(t *testing.T) {
	cases := []struct {
		name             string
		file             File
		staged, unstaged bool
	}{
		{name: "staged only", file: File{Kind: KindChanged, Index: 'A', Worktree: '.'}, staged: true},
		{name: "unstaged only", file: File{Kind: KindChanged, Index: '.', Worktree: 'M'}, unstaged: true},
		{name: "both", file: File{Kind: KindChanged, Index: 'M', Worktree: 'D'}, staged: true, unstaged: true},
		{name: "untracked", file: File{Kind: KindUntracked, Index: '?', Worktree: '?'}, unstaged: true},
		{name: "conflict", file: File{Kind: KindUnmerged, Index: 'U', Worktree: 'U'}, unstaged: true},
		{name: "ignored", file: File{Kind: KindIgnored, Index: '!', Worktree: '!'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.file.Staged(); got != tc.staged {
				t.Errorf("Staged() = %v, want %v", got, tc.staged)
			}
			if got := tc.file.Unstaged(); got != tc.unstaged {
				t.Errorf("Unstaged() = %v, want %v", got, tc.unstaged)
			}
			want := 0
			if tc.staged {
				want = 1
			}
			ch := Changes{Files: []File{tc.file}}
			if ch.Staged() != want || ch.Clean() {
				t.Errorf("Changes.Staged() = %d (want %d), Clean() = %v", ch.Staged(), want, ch.Clean())
			}
		})
	}
	if !(Changes{}).Clean() {
		t.Error("empty Changes is not clean")
	}
}
