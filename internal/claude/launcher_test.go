package claude

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

func TestWriteLauncher(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state", "launchers")
	script, err := claudecfg.TeammateLauncher("/opt/lmux", "/opt/claude")
	if err != nil {
		t.Fatal(err)
	}

	path, err := WriteLauncher(dir, script)
	if err != nil {
		t.Fatalf("WriteLauncher: %v", err)
	}
	if want := filepath.Join(dir, claudecfg.LauncherFileName(script)); path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	assertMode(t, dir, fsx.PrivateDir)
	assertMode(t, path, 0o700)
	if got, _ := os.ReadFile(path); string(got) != script {
		t.Fatalf("content = %q", got)
	}

	again, err := WriteLauncher(dir, script)
	if err != nil || again != path {
		t.Fatalf("second WriteLauncher = %s, %v", again, err)
	}
	other, err := claudecfg.TeammateLauncher("/opt/lmux", "/usr/bin/claude")
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteLauncher(dir, other)
	if err != nil || second == path {
		t.Fatalf("another agent = %s, %v", second, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("directory has %d entries, want one per pair of binaries", len(entries))
	}
}

// TestWriteLauncherRuns starts the written file the way Claude Code starts it,
// as a program of its own, which only works if the file carries the execute
// bit and the interpreter line it was rendered with.
func TestWriteLauncherRuns(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	lmux := filepath.Join(dir, "lmux")
	recorder := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + out + "'\n"
	if err := os.WriteFile(lmux, []byte(recorder), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := claudecfg.TeammateLauncher(lmux, filepath.Join(dir, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := WriteLauncher(filepath.Join(dir, "launchers"), script)
	if err != nil {
		t.Fatalf("WriteLauncher: %v", err)
	}
	if out, err := exec.Command(path, "--agent-name", "review-api").CombinedOutput(); err != nil {
		t.Fatalf("run %s: %v %s", path, err, out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{"teammate", "--claude", filepath.Join(dir, "claude"), "--", "--agent-name", "review-api"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("arguments %q, want %q", got, want)
	}
}

func TestWriteLauncherRefuses(t *testing.T) {
	root := t.TempDir()
	script, err := claudecfg.TeammateLauncher("/opt/lmux", "/opt/claude")
	if err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked")
	if err := os.Symlink(t.TempDir(), linkedDir); err != nil {
		t.Fatal(err)
	}
	fileDir := filepath.Join(root, "file")
	if err := os.WriteFile(fileDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(root, "victim")
	if err := os.WriteFile(linkTarget, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	plantedDir := filepath.Join(root, "planted")
	if err := os.Mkdir(plantedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, filepath.Join(plantedDir, claudecfg.LauncherFileName(script))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		dir    string
		script string
		errIs  error
		text   string
	}{
		{name: "relative dir", dir: "launchers", script: script, text: "must be absolute"},
		{name: "a script nobody rendered", dir: filepath.Join(root, "ok"), script: "rm -rf /\n", text: "not a rendered launcher"},
		{name: "an interpreter of its own", dir: filepath.Join(root, "ok"), script: "#!/bin/bash\n" + script, text: "not a rendered launcher"},
		{name: "nothing at all", dir: filepath.Join(root, "ok"), script: "", text: "not a rendered launcher"},
		{name: "dir is a link", dir: linkedDir, script: script, errIs: fsx.ErrSymlink},
		{name: "dir is a file", dir: fileDir, script: script, errIs: fsx.ErrNotDir},
		{name: "launcher is a planted link", dir: plantedDir, script: script, errIs: fsx.ErrSymlink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WriteLauncher(tc.dir, tc.script)
			if err == nil || (tc.errIs != nil && !errors.Is(err, tc.errIs)) || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("WriteLauncher error = %v, want %v containing %q", err, tc.errIs, tc.text)
			}
		})
	}
	if got, _ := os.ReadFile(linkTarget); string(got) != "keep" {
		t.Fatalf("link target was written: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "ok")); err == nil {
		t.Fatal("a refused launcher created its directory")
	}
}
