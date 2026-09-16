package fsx

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestEnsurePrivateDir(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string) string
		// owned is the path, relative to the temporary root, below which every
		// component is one the call created and must be a private directory.
		// Empty means the whole tree below the root.
		owned   string
		wantErr error
	}{
		{
			name:  "creates nested",
			setup: func(_ *testing.T, root string) string { return filepath.Join(root, "a", "b", "c") },
		},
		{
			name: "tightens existing permissions",
			setup: func(t *testing.T, root string) string {
				dir := filepath.Join(root, "open")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return dir
			},
		},
		{
			name: "refuses symlink",
			setup: func(t *testing.T, root string) string {
				target := filepath.Join(root, "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			wantErr: ErrSymlink,
		},
		{
			name: "refuses file",
			setup: func(t *testing.T, root string) string {
				file := filepath.Join(root, "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return file
			},
			wantErr: ErrNotDir,
		},
		{
			// The substitution the package exists to refuse: another local
			// user plants a link where lyna-tmux is about to create, and the
			// private state lands in a directory they own.
			name: "refuses to create inside a linked parent",
			setup: func(t *testing.T, root string) string {
				victim := filepath.Join(root, "victim")
				if err := os.Mkdir(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "planted")
				if err := os.Symlink(victim, link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "state")
			},
			wantErr: ErrSymlink,
		},
		{
			name: "refuses to create below a linked grandparent",
			setup: func(t *testing.T, root string) string {
				victim := filepath.Join(root, "victim")
				if err := os.Mkdir(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "planted")
				if err := os.Symlink(victim, link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "state", "settings")
			},
			wantErr: ErrSymlink,
		},
		{
			name: "refuses to create below a dangling link",
			setup: func(t *testing.T, root string) string {
				link := filepath.Join(root, "planted")
				if err := os.Symlink(filepath.Join(root, "gone"), link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "state")
			},
			wantErr: ErrSymlink,
		},
		{
			name: "refuses to create inside a regular file",
			setup: func(t *testing.T, root string) string {
				file := filepath.Join(root, "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(file, "state")
			},
			wantErr: ErrNotDir,
		},
		{
			// A parent that was already there is not a substitution: a home
			// directory reached through a link is a normal dotfiles layout.
			name: "accepts an existing directory reached through a link",
			setup: func(t *testing.T, root string) string {
				state := filepath.Join(root, "real", "state")
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "dotfiles")
				if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "state", "settings")
			},
			owned: "dotfiles/state",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := tc.setup(t, root)
			before := treeOf(t, root)
			err := EnsurePrivateDir(dir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if got := treeOf(t, root); !slices.Equal(got, before) {
					t.Fatalf("a refused call changed the tree\n got  %q\n want %q", got, before)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			bound := root
			if tc.owned != "" {
				bound = filepath.Join(root, filepath.FromSlash(tc.owned))
			}
			assertPrivateBelow(t, dir, bound)
		})
	}
}

// assertPrivateBelow checks that dir and every component down to bound is a
// real directory with the private mode, following no link on the way.
func assertPrivateBelow(t *testing.T, dir, bound string) {
	t.Helper()
	for p := filepath.Clean(dir); len(p) > len(filepath.Clean(bound)); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			t.Fatalf("%s is %v, want a real directory", p, info.Mode())
		}
		if perm := info.Mode().Perm(); perm != PrivateDir {
			t.Fatalf("%s perm = %o, want %o", p, perm, PrivateDir)
		}
	}
}

// treeOf lists every path below root, links included and never followed.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// TestEnsurePrivateDirConcurrent runs the creation of one nested path from
// several goroutines: each component is created once and every call has to
// agree on the result, which is the branch that sees its own component
// already there.
func TestEnsurePrivateDirConcurrent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state", "lyna-tmux", "settings")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = EnsurePrivateDir(dir)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != PrivateDir {
		t.Fatalf("Lstat = %v, %v", info, err)
	}
}

// FuzzEnsurePrivateDir checks that no relative path, however it is spelled,
// leaves a component that is not a private directory behind, and that nothing
// is created outside the root.
func FuzzEnsurePrivateDir(f *testing.F) {
	for _, seed := range []string{"state", "state/settings", "a/b/c", "..", "../escape", "./x", "", "a//b", "a/./b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		root := t.TempDir()
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if !strings.HasPrefix(filepath.Clean(dir)+string(filepath.Separator), filepath.Clean(root)+string(filepath.Separator)) {
			// The callers build paths below their own roots; a path that
			// escapes is the test's own doing, not something to create.
			return
		}
		if err := EnsurePrivateDir(dir); err != nil {
			return
		}
		for p := filepath.Clean(dir); len(p) > len(root); p = filepath.Dir(p) {
			info, err := os.Lstat(p)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				t.Fatalf("%s is %v, want a real directory", p, info.Mode())
			}
			if perm := info.Mode().Perm(); perm != PrivateDir {
				t.Fatalf("%s perm = %o, want %o", p, perm, PrivateDir)
			}
		}
	})
}

func TestWriteFileAtomic(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, path string)
		data    string
		wantErr error
	}{
		{name: "new file", data: "hello"},
		{
			name: "replaces existing",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("old content that is longer"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			data: "new",
		},
		{
			name: "refuses symlink target",
			setup: func(t *testing.T, path string) {
				victim := filepath.Join(filepath.Dir(path), "victim")
				if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, path); err != nil {
					t.Fatal(err)
				}
			},
			data:    "attack",
			wantErr: ErrSymlink,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if tc.setup != nil {
				tc.setup(t, path)
			}
			err := WriteFileAtomic(path, []byte(tc.data), PrivateFile)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if got, _ := os.ReadFile(filepath.Join(dir, "victim")); string(got) != "keep" {
					t.Fatalf("victim modified: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.data {
				t.Fatalf("content = %q, want %q", got, tc.data)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != PrivateFile {
				t.Fatalf("perm = %o", info.Mode().Perm())
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp-") {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestReadLimited(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		limit   int64
		wantErr error
	}{
		{name: "under", input: "abc", limit: 4},
		{name: "exact", input: "abcd", limit: 4},
		{name: "over", input: "abcde", limit: 4, wantErr: ErrTooLarge},
		{name: "empty", input: "", limit: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadLimited(strings.NewReader(tc.input), tc.limit)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && string(got) != tc.input {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestReadFileNoFollow(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string) string
		limit   int64
		want    string
		wantErr error
	}{
		{
			name: "regular",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "f")
				if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			limit: 10,
			want:  "data",
		},
		{
			name: "symlink refused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "f")
				if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
				l := filepath.Join(dir, "l")
				if err := os.Symlink(p, l); err != nil {
					t.Fatal(err)
				}
				return l
			},
			limit:   10,
			wantErr: ErrSymlink,
		},
		{
			name: "too large",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "f")
				if err := os.WriteFile(p, []byte("0123456789"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			limit:   5,
			wantErr: ErrTooLarge,
		},
		{
			name:    "missing",
			setup:   func(_ *testing.T, dir string) string { return filepath.Join(dir, "missing") },
			limit:   5,
			wantErr: os.ErrNotExist,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir())
			got, err := ReadFileNoFollow(path, tc.limit)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadFileLimitedFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(target, []byte("x = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileLimited(link, 100)
	if err != nil || string(got) != "x = 1" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAppendCapped(t *testing.T) {
	cases := []struct {
		name        string
		existing    string
		line        string
		max         int64
		wantCurrent string
		wantRotated string
	}{
		{name: "creates", line: "one", max: 100, wantCurrent: "one\n"},
		{name: "appends", existing: "one\n", line: "two\n", max: 100, wantCurrent: "one\ntwo\n"},
		{name: "rotates", existing: "0123456789\n", line: "fresh", max: 5, wantCurrent: "fresh\n", wantRotated: "0123456789\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "log")
			if tc.existing != "" {
				if err := os.WriteFile(path, []byte(tc.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := AppendCapped(path, []byte(tc.line), tc.max); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != tc.wantCurrent {
				t.Fatalf("current = %q, want %q", got, tc.wantCurrent)
			}
			rotated, _ := os.ReadFile(path + ".1")
			if !bytes.Equal(rotated, []byte(tc.wantRotated)) {
				t.Fatalf("rotated = %q, want %q", rotated, tc.wantRotated)
			}
		})
	}

	t.Run("refuses symlink", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "log")
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		if err := AppendCapped(path, []byte("x"), 100); !errors.Is(err, ErrSymlink) {
			t.Fatalf("err = %v, want ErrSymlink", err)
		}
	})
}
