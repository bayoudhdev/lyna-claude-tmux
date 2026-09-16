package githead

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sha1Hex   = "0123456789abcdef0123456789abcdef01234567"
	sha256Hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "branch", in: "ref: refs/heads/main\n", want: "main"},
		{name: "nested branch", in: "ref: refs/heads/feature/login-v2\n", want: "feature/login-v2"},
		{name: "no space after colon", in: "ref:refs/heads/dev", want: "dev"},
		{name: "crlf", in: "ref: refs/heads/win\r\n", want: "win"},
		{name: "other ref", in: "ref: refs/remotes/origin/main\n", want: "remotes/origin/main"},
		{name: "detached sha1", in: sha1Hex + "\n", want: "0123456"},
		{name: "detached sha256", in: sha256Hex, want: "0123456"},
		{name: "unicode branch", in: "ref: refs/heads/café-漢字\n", want: "café-漢字"},
		{name: "extra lines ignored", in: "ref: refs/heads/main\ngarbage\n", want: "main"},
		{name: "empty", in: "", wantErr: true},
		{name: "not refs", in: "ref: heads/main", wantErr: true},
		{name: "bare refs prefix", in: "ref: refs/", wantErr: true},
		{name: "dotdot", in: "ref: refs/heads/a..b", wantErr: true},
		{name: "lock suffix", in: "ref: refs/heads/x.lock", wantErr: true},
		{name: "trailing slash", in: "ref: refs/heads/x/", wantErr: true},
		{name: "space inside", in: "ref: refs/heads/a b", wantErr: true},
		{name: "escape inside", in: "ref: refs/heads/a\x1b]0;pwn\x07", wantErr: true},
		{name: "glob chars", in: "ref: refs/heads/a*b", wantErr: true},
		{name: "uppercase hash", in: strings.ToUpper(sha1Hex), wantErr: true},
		{name: "short hash", in: sha1Hex[:39], wantErr: true},
		{name: "random text", in: "root:x:0:0:root:/root:/bin/sh", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.in))
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidHead) {
					t.Fatalf("Parse(%q) = %q, %v; want ErrInvalidHead", tc.in, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// setHome points the home bound at home for the rest of the test. In
// production it is the user's home directory, which the repositories a gitdir
// pointer may name live under.
func setHome(t *testing.T, home string) {
	t.Helper()
	saved := homeDir
	homeDir = func() string { return home }
	t.Cleanup(func() { homeDir = saved })
}

// layout writes files (path -> content) under root; a content starting with
// "->" creates a symbolic link to the rest, a trailing "/" in the path creates
// a directory, and "$ROOT" in a content is replaced by root.
func layout(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		content = strings.ReplaceAll(content, "$ROOT", root)
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, "->"); ok {
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBranchOnDisk(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		start string // relative to root
		// home replaces the user's home directory: empty is the temporary
		// root, as a repository below the home directory behaves, and "none"
		// leaves the home unknown so only the repository rules apply.
		home    string
		want    string
		wantErr error // nil means success; errAny accepts any error
	}{
		{
			name:  "repository root",
			files: map[string]string{"repo/.git/HEAD": "ref: refs/heads/main\n"},
			start: "repo", want: "main",
		},
		{
			name:  "deep subdirectory",
			files: map[string]string{"repo/.git/HEAD": "ref: refs/heads/dev\n", "repo/a/b/c/": ""},
			start: "repo/a/b/c", want: "dev",
		},
		{
			name:  "detached",
			files: map[string]string{"repo/.git/HEAD": sha1Hex + "\n"},
			start: "repo", want: "0123456",
		},
		{
			name: "worktree with relative gitdir",
			files: map[string]string{
				"repo/.git/worktrees/wt/HEAD": "ref: refs/heads/wt-branch\n",
				"wt/.git":                     "gitdir: ../repo/.git/worktrees/wt\n",
			},
			start: "wt", want: "wt-branch",
		},
		{
			name: "submodule with relative gitdir from subdirectory",
			files: map[string]string{
				"super/.git/HEAD":             "ref: refs/heads/main\n",
				"super/.git/modules/sub/HEAD": sha256Hex,
				"super/sub/.git":              "gitdir: ../.git/modules/sub",
				"super/sub/pkg/":              "",
			},
			start: "super/sub/pkg", home: "none", want: "0123456",
		},
		{
			// The layout lyna-tmux creates itself: a worktree below the
			// repository whose git directory sits in the repository's .git.
			name: "worktree of an enclosing repository with an absolute gitdir",
			files: map[string]string{
				"repo/.git/HEAD":                      "ref: refs/heads/main\n",
				"repo/.git/worktrees/feature/HEAD":    "ref: refs/heads/feature\n",
				"repo/.claude/worktrees/feature/.git": "gitdir: $ROOT/repo/.git/worktrees/feature\n",
				"repo/.claude/worktrees/feature/pkg/": "",
			},
			start: "repo/.claude/worktrees/feature/pkg", home: "none", want: "feature",
		},
		{
			name: "gitdir below the home directory",
			files: map[string]string{
				"wt/.git":               "gitdir: $ROOT/elsewhere/gitdir\n",
				"elsewhere/gitdir/HEAD": sha1Hex + "\n",
			},
			start: "wt", want: "0123456",
		},
		{
			// The pointer comes from the repository, so a checkout must not be
			// able to have every hook event open a path of its choosing. The
			// target exists and would parse: it is refused before it is read.
			name: "gitdir outside the repository and the home is refused",
			files: map[string]string{
				"wt/.git":          "gitdir: $ROOT/secret/.git\n",
				"secret/.git/HEAD": "ref: refs/heads/leaked\n",
			},
			start: "wt", home: "none", wantErr: ErrGitDirOutside,
		},
		{
			name:    "gitdir climbing out of the tree is refused",
			files:   map[string]string{"wt/.git": "gitdir: ../../../../../../etc\n"},
			start:   "wt",
			home:    "none",
			wantErr: ErrGitDirOutside,
		},
		{
			name:    "gitdir at an absolute path with no git directory is refused",
			files:   map[string]string{"wt/.git": "gitdir: /etc/ssh\n"},
			start:   "wt",
			home:    "none",
			wantErr: ErrGitDirOutside,
		},
		{
			name: "nearest repository wins",
			files: map[string]string{
				"outer/.git/HEAD":       "ref: refs/heads/outer\n",
				"outer/inner/.git/HEAD": "ref: refs/heads/inner\n",
			},
			start: "outer/inner", want: "inner",
		},
		{
			name:    "git directory without HEAD stops the walk",
			files:   map[string]string{"outer/.git/HEAD": "ref: refs/heads/outer\n", "outer/inner/.git/": ""},
			start:   "outer/inner",
			wantErr: errAny,
		},
		{
			name:    "HEAD symlink refused",
			files:   map[string]string{"repo/.git/HEAD": "->../elsewhere", "repo/elsewhere": "ref: refs/heads/main\n"},
			start:   "repo",
			wantErr: errAny,
		},
		{
			name:    "invalid HEAD",
			files:   map[string]string{"repo/.git/HEAD": "not a ref\n"},
			start:   "repo",
			wantErr: ErrInvalidHead,
		},
		{
			name:    "invalid .git file",
			files:   map[string]string{"wt/.git": "hello\n"},
			start:   "wt",
			wantErr: errAny,
		},
		{
			name:    "gitdir target missing",
			files:   map[string]string{"wt/.git": "gitdir: /nonexistent/lyna-tmux/gitdir\n"},
			start:   "wt",
			wantErr: errAny,
		},
		{
			name:    "oversized HEAD",
			files:   map[string]string{"repo/.git/HEAD": "ref: refs/heads/" + strings.Repeat("x", MaxFileBytes)},
			start:   "repo",
			wantErr: errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			layout(t, root, tc.files)
			home := root
			if tc.home == "none" {
				home = ""
			}
			setHome(t, home)
			got, err := Branch(filepath.Join(root, filepath.FromSlash(tc.start)), nil)
			switch {
			case tc.wantErr == nil:
				if err != nil || got != tc.want {
					t.Fatalf("Branch = %q, %v; want %q", got, err, tc.want)
				}
			case errors.Is(tc.wantErr, errAny):
				if err == nil {
					t.Fatalf("Branch = %q; want an error", got)
				}
			default:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Branch = %q, %v; want %v", got, err, tc.wantErr)
				}
			}
		})
	}
}

var errAny = errors.New("any error")

func TestBranchWalkAndArguments(t *testing.T) {
	notExist := func(string, int64) ([]byte, error) { return nil, fs.ErrNotExist }
	cases := []struct {
		name    string
		dir     string
		read    ReadFunc
		want    string
		wantErr error
	}{
		{name: "relative directory", dir: "repo", read: notExist, wantErr: errAny},
		{name: "empty directory", dir: "", read: notExist, wantErr: errAny},
		{name: "no repository up to the root", dir: "/a/b/c", read: notExist, wantErr: ErrNoRepository},
		{
			name: "found at the filesystem root",
			dir:  "/a/b",
			read: func(path string, _ int64) ([]byte, error) {
				if path == "/.git/HEAD" {
					return []byte("ref: refs/heads/root\n"), nil
				}
				return nil, fs.ErrNotExist
			},
			want: "root",
		},
		{
			name: "limit is passed through",
			dir:  "/r",
			read: func(path string, limit int64) ([]byte, error) {
				if limit != MaxFileBytes {
					return nil, errors.New("wrong limit")
				}
				if path == "/r/.git/HEAD" {
					return []byte(sha1Hex), nil
				}
				return nil, fs.ErrNotExist
			},
			want: "0123456",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setHome(t, "/home/u")
			got, err := Branch(tc.dir, tc.read)
			switch {
			case tc.wantErr == nil:
				if err != nil || got != tc.want {
					t.Fatalf("Branch = %q, %v; want %q", got, err, tc.want)
				}
			case errors.Is(tc.wantErr, errAny):
				if err == nil {
					t.Fatalf("Branch = %q; want an error", got)
				}
			default:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Branch = %q, %v; want %v", got, err, tc.wantErr)
				}
			}
		})
	}
}

// FuzzGitDirPointer checks the bound on the one input a repository fully
// controls: whatever a .git file holds, githead opens nothing outside the
// user's home directory and the .git directories of the working tree and its
// ancestors. The walk's own probes are what the property is stated against,
// so a pointer cannot smuggle in a path of its own.
func FuzzGitDirPointer(f *testing.F) {
	for _, seed := range []string{
		"gitdir: ../repo/.git/worktrees/wt", "gitdir: /etc", "gitdir:\t/etc/ssh", "gitdir: ",
		"gitdir: ../../../../etc/passwd", "gitdir: /var/root/.ssh", "gitdir: ./.git/../../..", "hello",
	} {
		f.Add(seed)
	}
	const (
		home = "/home/u"
		repo = "/home/u/src/app"
	)
	f.Fuzz(func(t *testing.T, pointer string) {
		setHome(t, home)
		var opened []string
		read := func(path string, limit int64) ([]byte, error) {
			if limit != MaxFileBytes {
				t.Fatalf("read %q with limit %d, want %d", path, limit, MaxFileBytes)
			}
			opened = append(opened, path)
			if path == filepath.Join(repo, ".git") {
				return []byte(pointer), nil
			}
			return nil, fs.ErrNotExist
		}
		_, _ = Branch(repo, read)
		for _, p := range opened {
			if within(p, home) || underAncestorGitDir(p, repo) {
				continue
			}
			t.Fatalf("pointer %q made githead open %q", pointer, p)
		}
	})
}

// underAncestorGitDir reports whether p is inside the .git directory of dir or
// of one of its ancestors.
func underAncestorGitDir(p, dir string) bool {
	for a := dir; ; {
		if within(p, filepath.Join(a, ".git")) {
			return true
		}
		parent := filepath.Dir(a)
		if parent == a {
			return false
		}
		a = parent
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"ref: refs/heads/main\n", sha1Hex, sha256Hex, "ref: refs/", "", "ref: refs/heads/\x1b[31m"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		label, err := Parse(data)
		if err != nil {
			if label != "" {
				t.Fatalf("label %q returned with error %v", label, err)
			}
			return
		}
		if label == "" {
			t.Fatal("empty label without error")
		}
		if strings.ContainsFunc(label, func(r rune) bool { return isControl(r) || r == ' ' }) {
			t.Fatalf("label %q holds control characters or spaces", label)
		}
		if len(label) > len(data) {
			t.Fatalf("label %q longer than input", label)
		}
	})
}
