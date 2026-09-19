package vcs

import (
	"cmp"
	"slices"
)

// File is one changed path with its staged and unstaged state and line
// counts.
type File struct {
	Kind     Kind
	Path     string
	OrigPath string
	// Index and Worktree are the status codes (see Entry).
	Index    byte
	Worktree byte
	// Added and Deleted sum the staged and unstaged line counts. Untracked
	// files have no counts: git does not diff them.
	Added   int
	Deleted int
	Binary  bool
}

// Staged reports whether the file has changes in the index.
func (f File) Staged() bool {
	return f.Kind != KindUntracked && f.Kind != KindIgnored && f.Kind != KindUnmerged && f.Index != Unchanged
}

// Unstaged reports whether the file has changes in the working tree that are
// not staged. Untracked and conflicted files count as unstaged.
func (f File) Unstaged() bool {
	switch f.Kind {
	case KindUntracked, KindUnmerged:
		return true
	case KindIgnored:
		return false
	}
	return f.Worktree != Unchanged
}

// Changes is the state of a working tree.
type Changes struct {
	Head Head
	// Files are sorted by path (then original path), so the order is stable
	// across refreshes.
	Files   []File
	Added   int
	Deleted int
}

// Clean reports whether nothing changed.
func (c Changes) Clean() bool { return len(c.Files) == 0 }

// Staged counts files with staged changes.
func (c Changes) Staged() int {
	n := 0
	for _, f := range c.Files {
		if f.Staged() {
			n++
		}
	}
	return n
}

// Build joins a status with the unstaged (`git diff --numstat`) and staged
// (`git diff --cached --numstat`) line counts.
func Build(st Status, unstaged, staged []NumStat) Changes {
	type counts struct {
		added, deleted int
		binary         bool
	}
	byPath := make(map[string]counts, len(unstaged)+len(staged))
	for _, list := range [][]NumStat{unstaged, staged} {
		for _, ns := range list {
			c := byPath[ns.Path]
			c.added += ns.Added
			c.deleted += ns.Deleted
			c.binary = c.binary || ns.Binary
			byPath[ns.Path] = c
		}
	}
	ch := Changes{Head: st.Head, Files: make([]File, 0, len(st.Entries))}
	for _, e := range st.Entries {
		f := File{Kind: e.Kind, Path: e.Path, OrigPath: e.OrigPath, Index: e.Index, Worktree: e.Worktree}
		if c, ok := byPath[e.Path]; ok {
			f.Added, f.Deleted, f.Binary = c.added, c.deleted, c.binary
		}
		ch.Added += f.Added
		ch.Deleted += f.Deleted
		ch.Files = append(ch.Files, f)
	}
	slices.SortStableFunc(ch.Files, func(a, b File) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.OrigPath, b.OrigPath))
	})
	return ch
}
