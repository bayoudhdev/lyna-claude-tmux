package claude

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func lookPathOf(found map[string]string) LookPathFunc {
	return func(file string) (string, error) {
		if p, ok := found[file]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
}

func existsIn(paths ...string) ExistsFunc {
	return func(p string) bool { return slices.Contains(paths, p) }
}

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestFind(t *testing.T) {
	cases := []struct {
		name     string
		path     map[string]string
		env      map[string]string
		home     string
		exists   []string
		want     string
		searched []string
	}{
		{name: "PATH wins", path: map[string]string{"claude": "/usr/local/bin/claude"}, home: "/home/u", exists: []string{"/home/u/.local/bin/claude"}, want: "/usr/local/bin/claude"},
		{name: "PATH result cleaned", path: map[string]string{"claude": "/usr/local/bin/../bin/claude"}, home: "/home/u", want: "/usr/local/bin/claude"},
		{name: "relative PATH hit ignored", path: map[string]string{"claude": "./claude"}, home: "/home/u", exists: []string{"/home/u/.local/bin/claude"}, want: "/home/u/.local/bin/claude"},
		{name: "native launcher", home: "/home/u", exists: []string{"/home/u/.local/bin/claude", "/home/u/.claude/local/claude"}, want: "/home/u/.local/bin/claude"},
		{name: "local install", home: "/home/u", exists: []string{"/home/u/.claude/local/claude"}, want: "/home/u/.claude/local/claude"},
		{name: "local install under config dir", home: "/home/u", env: map[string]string{"CLAUDE_CONFIG_DIR": "/cfg/claude"}, exists: []string{"/cfg/claude/local/claude"}, want: "/cfg/claude/local/claude"},
		{name: "nothing", home: "/home/u", searched: []string{"/home/u/.local/bin/claude", "/home/u/.claude/local/claude"}},
		{name: "no home", home: "", env: map[string]string{"CLAUDE_CONFIG_DIR": "/cfg"}, searched: []string{"/cfg/local/claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Find(lookPathOf(tc.path), envOf(tc.env), tc.home, existsIn(tc.exists...))
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("Find = %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			var nf *NotFoundError
			if !errors.As(err, &nf) || !errors.Is(err, ErrNotFound) || got != "" {
				t.Fatalf("Find = %q, %v; want NotFoundError", got, err)
			}
			if !slices.Equal(nf.Searched, tc.searched) {
				t.Fatalf("searched = %v, want %v", nf.Searched, tc.searched)
			}
			msg := err.Error()
			if !strings.Contains(msg, "https://claude.ai/install.sh") || !strings.Contains(msg, "on PATH") {
				t.Fatalf("error lacks guidance: %s", msg)
			}
			for _, p := range tc.searched {
				if !strings.Contains(msg, p) {
					t.Fatalf("error does not list %s: %s", p, msg)
				}
			}
		})
	}
}

func TestResolveCommand(t *testing.T) {
	path := map[string]string{"claude": "/usr/bin/claude", "claude-dev": "/opt/bin/claude-dev", "rel": "bin/rel"}
	cases := []struct {
		name    string
		command string
		home    string
		exists  []string
		want    string
	}{
		{name: "empty uses Find", command: "", home: "/h", want: "/usr/bin/claude"},
		{name: "claude uses Find", command: "claude", home: "/h", want: "/usr/bin/claude"},
		{name: "bare name on PATH", command: "claude-dev", home: "/h", want: "/opt/bin/claude-dev"},
		{name: "bare name missing", command: "nope", home: "/h"},
		{name: "bare name relative PATH hit", command: "rel", home: "/h"},
		{name: "absolute path", command: "/srv/claude/bin/claude", exists: []string{"/srv/claude/bin/claude"}, want: "/srv/claude/bin/claude"},
		{name: "absolute path cleaned", command: "/srv/claude//bin/./claude", exists: []string{"/srv/claude/bin/claude"}, want: "/srv/claude/bin/claude"},
		{name: "absolute path missing", command: "/srv/none"},
		{name: "home relative", command: "~/tools/claude", home: "/h", exists: []string{"/h/tools/claude"}, want: "/h/tools/claude"},
		{name: "home relative without home", command: "~/tools/claude", home: ""},
		{name: "relative path refused", command: "./claude", home: "/h", exists: []string{"./claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCommand(tc.command, lookPathOf(path), envOf(nil), tc.home, existsIn(tc.exists...))
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("ResolveCommand = %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if !errors.Is(err, ErrNotFound) || got != "" {
				t.Fatalf("ResolveCommand = %q, %v; want ErrNotFound", got, err)
			}
			if tc.command != "" && !strings.Contains(err.Error(), " at "+tc.command) && !strings.Contains(err.Error(), " at /") {
				t.Fatalf("error does not name the command: %v", err)
			}
		})
	}
}

func TestIsExecutable(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exe := write("exe", 0o755)
	plain := write("plain", 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want bool
	}{
		{name: "executable file", path: exe, want: true},
		{name: "link to executable", path: link, want: true},
		{name: "not executable", path: plain},
		{name: "directory", path: dir},
		{name: "missing", path: filepath.Join(dir, "missing")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsExecutable(tc.path); got != tc.want {
				t.Fatalf("IsExecutable(%s) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
