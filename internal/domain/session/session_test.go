package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "api", "api"},
		{"keeps case digits underscore dash", "My_App-2", "My_App-2"},
		{"dot becomes dash", "my.app", "my-app"},
		{"colon becomes dash", "a:b", "a-b"},
		{"spaces collapse", "my  cool   app", "my-cool-app"},
		{"leading separators trimmed", "--.hidden", "hidden"},
		{"trailing separators trimmed", "app__--", "app"},
		{"unicode replaced", "café 漢字", "caf"},
		{"shell metachars", "$(rm -rf ~);`x`", "rm-rf-x"},
		{"empty", "", DefaultName},
		{"only symbols", "#%!", DefaultName},
		{"long truncated", strings.Repeat("a", 100), strings.Repeat("a", MaxNameLen)},
		{"truncation trims separator", strings.Repeat("a", MaxNameLen-1) + "-bbb", strings.Repeat("a", MaxNameLen-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if err := Validate(got); err != nil {
				t.Fatalf("Sanitize output invalid: %v", err)
			}
		})
	}
}

func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"", "a.b", "-x", "é", strings.Repeat("-a", 60)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := Sanitize(in)
		if err := Validate(got); err != nil {
			t.Fatalf("Sanitize(%q) = %q: %v", in, got, err)
		}
		if again := Sanitize(got); again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, got, again)
		}
	})
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"simple", "api", true},
		{"dash underscore digits", "a_b-3", true},
		{"max length", strings.Repeat("x", MaxNameLen), true},
		{"empty", "", false},
		{"too long", strings.Repeat("x", MaxNameLen+1), false},
		{"leading dash", "-a", false},
		{"dot", "a.b", false},
		{"colon", "a:b", false},
		{"space", "a b", false},
		{"unicode", "é", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("Validate(%q) = %v, ok %v", tc.in, err, tc.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalidName) {
				t.Fatalf("error does not wrap ErrInvalidName: %v", err)
			}
		})
	}
}

func TestUnique(t *testing.T) {
	long := strings.Repeat("a", MaxNameLen)
	cases := []struct {
		name  string
		base  string
		taken []string
		want  string
	}{
		{"free", "api", nil, "api"},
		{"first suffix", "api", []string{"api"}, "api-2"},
		{"skips taken suffixes", "api", []string{"api", "api-2", "api-3"}, "api-4"},
		{"long base keeps limit", long, []string{long}, long[:MaxNameLen-2] + "-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := map[string]bool{}
			for _, n := range tc.taken {
				set[n] = true
			}
			got := Unique(tc.base, func(n string) bool { return set[n] })
			if got != tc.want {
				t.Fatalf("Unique = %q, want %q", got, tc.want)
			}
			if err := Validate(got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPathHash(t *testing.T) {
	cases := []struct{ path, want string }{
		// Reference values from `printf '%s\n' "$path" | md5sum | cut -c1-8`.
		{"/Users/dev/project", "ae91e29a"},
		{"/", "a5582242"},
		{"/tmp/with space", "bc849e9b"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := PathHash(tc.path); got != tc.want {
				t.Fatalf("PathHash(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestPathHashMatchesShell checks the digest against the real shell pipeline
// the upstream plugin runs, so the reference table cannot drift.
func TestPathHashMatchesShell(t *testing.T) {
	tool, args := "md5sum", []string{}
	if _, err := exec.LookPath(tool); err != nil {
		tool, args = "md5", []string{"-q"}
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no md5 tool available")
		}
	}
	for _, p := range []string{"/Users/dev/project", "/", "/tmp/with space", "/tmp/été"} {
		t.Run(p, func(t *testing.T) {
			cmd := exec.Command(tool, args...)
			cmd.Stdin = strings.NewReader(p + "\n")
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if want := string(out[:8]); PathHash(p) != want {
				t.Fatalf("PathHash(%q) = %q, shell %q", p, PathHash(p), want)
			}
		})
	}
}

func TestPopupName(t *testing.T) {
	cases := []struct{ name, prefix, path, want string }{
		{"default prefix", "", "/", "claude-a5582242"},
		{"custom prefix", "ai-", "/", "ai-a5582242"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PopupName(tc.prefix, tc.path)
			if got != tc.want {
				t.Fatalf("PopupName = %q, want %q", got, tc.want)
			}
			if !IsPopup(tc.prefix, got) {
				t.Fatal("IsPopup false for its own name")
			}
		})
	}
}

// TestIsPopup pins the strict predicate: a workspace whose name happens to
// start with the popup prefix, such as the project directory claude-api, is
// not a popup session.
func TestIsPopup(t *testing.T) {
	hash := PathHash("/src/api")
	cases := []struct {
		name, prefix, session string
		want                  bool
	}{
		{name: "popup session", prefix: "claude-", session: "claude-" + hash, want: true},
		{name: "empty prefix defaults", prefix: "", session: "claude-" + hash, want: true},
		{name: "custom prefix", prefix: "agent_", session: "agent_" + hash, want: true},
		{name: "project named after the prefix", prefix: "claude-", session: "claude-api"},
		{name: "project name with a suffix", prefix: "claude-", session: "claude-api-2"},
		{name: "eight letters that are not hex", prefix: "claude-", session: "claude-frontend"},
		{name: "hash too short", prefix: "claude-", session: "claude-" + hash[:PopupHashLen-1]},
		{name: "hash too long", prefix: "claude-", session: "claude-" + hash + "0"},
		{name: "uppercase hex", prefix: "claude-", session: "claude-DEADBEEF"},
		{name: "prefix alone", prefix: "claude-", session: "claude-"},
		{name: "other prefix", prefix: "agent-", session: "claude-" + hash},
		{name: "empty prefix keeps other names out", prefix: "", session: "api"},
		{name: "workspace", prefix: "claude-", session: "api"},
		{name: "empty name", prefix: "claude-", session: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPopup(tc.prefix, tc.session); got != tc.want {
				t.Fatalf("IsPopup(%q, %q) = %v, want %v", tc.prefix, tc.session, got, tc.want)
			}
		})
	}
}

// FuzzIsPopup checks the round trip both ways: the popup name of a directory
// is always recognized, and a recognized name is always a prefix followed by
// the hash, never a longer session name that starts the same way.
func FuzzIsPopup(f *testing.F) {
	for _, prefix := range []string{"", "claude-", "ai_", "x"} {
		for _, path := range []string{"", "/", "/src/api", "/tmp/été"} {
			f.Add(prefix, path)
		}
	}
	f.Fuzz(func(t *testing.T, prefix, path string) {
		name := PopupName(prefix, path)
		if !IsPopup(prefix, name) {
			t.Fatalf("IsPopup(%q, %q) = false for its own popup name", prefix, name)
		}
		if IsPopup(prefix, name+"x") || IsPopup(prefix, name[:len(name)-1]) {
			t.Fatalf("IsPopup(%q, ...) matches a name around %q", prefix, name)
		}
	})
}

func TestProjectRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "wt")
	plain := filepath.Join(root, "plain", "deep")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "pkg", "sub"), worktree, plain} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		dir     string
		want    string
		wantErr bool
	}{
		{"repo root", repo, repo, false},
		{"nested in repo", filepath.Join(repo, "pkg", "sub"), repo, false},
		{"git file worktree", worktree, worktree, false},
		{"no repo returns dir", plain, plain, false},
		{"unclean path", filepath.Join(repo, "pkg", "..", "pkg", "sub") + "/", repo, false},
		{"missing", filepath.Join(root, "missing"), "", true},
		{"file", file, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectRoot(tc.dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ProjectRoot(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}
