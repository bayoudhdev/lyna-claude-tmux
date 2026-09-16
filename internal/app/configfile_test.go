package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

func TestInitConfig(t *testing.T) {
	const userFile = "[ui]\ntheme = \"light\"\n"
	cases := []struct {
		name string
		// setup prepares the lyna-tmux home before InitConfig runs.
		setup      func(t *testing.T, root, file string)
		force      bool
		noHome     bool
		wantErr    error
		wantFile   string // expected config file content; "template" for the template
		wantBackup string
	}{
		{name: "creates the template", wantFile: "template"},
		{
			name: "keeps an existing file", setup: writeUserFile(userFile),
			wantErr: ErrConfigExists, wantFile: userFile,
		},
		{
			name: "force replaces with a backup", setup: writeUserFile(userFile), force: true,
			wantFile: "template", wantBackup: userFile,
		},
		{
			name: "force overwrites an old backup", force: true,
			setup: func(t *testing.T, root, file string) {
				t.Helper()
				writeUserFile(userFile)(t, root, file)
				if err := os.WriteFile(file+".bak", []byte("old backup"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantFile: "template", wantBackup: userFile,
		},
		{
			name: "symlinked file is never replaced", force: true,
			setup: func(t *testing.T, root, file string) {
				t.Helper()
				target := filepath.Join(root, "dotfiles.toml")
				if err := os.WriteFile(target, []byte(userFile), 0o600); err != nil {
					t.Fatal(err)
				}
				mkdir(t, filepath.Dir(file))
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrConfigSymlink, wantFile: userFile,
		},
		{
			name: "symlinked backup aborts before replacing", force: true,
			setup: func(t *testing.T, root, file string) {
				t.Helper()
				writeUserFile(userFile)(t, root, file)
				if err := os.Symlink(filepath.Join(root, "elsewhere"), file+".bak"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrSymlink, wantFile: userFile,
		},
		{name: "no home", noHome: true, wantErr: xdg.ErrNoHome},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			env := map[string]string{"LYNA_TMUX_HOME": root}
			h := Host{Getenv: func(k string) string { return env[k] }, Home: root}
			if tc.noHome {
				h = Host{Getenv: func(string) string { return "" }}
			}
			file := filepath.Join(root, "config", "config.toml")
			if tc.setup != nil {
				tc.setup(t, root, file)
			}
			res, err := InitConfig(h, tc.force)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if tc.noHome {
				return
			}
			if res.Path != file {
				t.Fatalf("path %q, want %q", res.Path, file)
			}
			want := []byte(tc.wantFile)
			if tc.wantFile == "template" {
				want = config.Template()
			}
			if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("file %q (%v), want %q", got, err, want)
			}
			if tc.wantFile == "template" {
				assertMode(t, file, 0o600)
				assertMode(t, filepath.Dir(file), 0o700)
			}
			if tc.wantBackup == "" {
				if res.Backup != "" {
					t.Fatalf("backup %q, want none", res.Backup)
				}
				return
			}
			if res.Backup != file+".bak" {
				t.Fatalf("backup %q", res.Backup)
			}
			if got, err := os.ReadFile(res.Backup); err != nil || string(got) != tc.wantBackup {
				t.Fatalf("backup %q (%v), want %q", got, err, tc.wantBackup)
			}
			assertMode(t, res.Backup, 0o600)
		})
	}
}

func writeUserFile(content string) func(t *testing.T, root, file string) {
	return func(t *testing.T, _, file string) {
		t.Helper()
		mkdir(t, filepath.Dir(file))
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode %o, want %o", path, info.Mode().Perm(), want)
	}
}

func TestEditorArgv(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		wantEditor string
	}{
		{name: "visual wins", env: map[string]string{"VISUAL": "nvim", "EDITOR": "nano"}, wantEditor: "nvim"},
		{name: "editor", env: map[string]string{"EDITOR": "nano"}, wantEditor: "nano"},
		{name: "fallback", wantEditor: "vi"},
		{name: "empty visual falls through", env: map[string]string{"VISUAL": "", "EDITOR": "hx"}, wantEditor: "hx"},
		{name: "editor with arguments", env: map[string]string{"EDITOR": "code --wait"}, wantEditor: "code --wait"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/x/config.toml"
			got := EditorArgv(func(k string) string { return tc.env[k] }, path)
			want := []string{"/bin/sh", "-c", tc.wantEditor + ` "$@"`, tc.wantEditor, path}
			if !slices.Equal(got, want) {
				t.Fatalf("argv %q, want %q", got, want)
			}
		})
	}
}

// TestEditorArgvShell runs the argv through the real shell with an editor that
// takes arguments and a hostile path: the editor receives its own arguments
// and the path as one untouched argument.
func TestEditorArgvShell(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	editor := filepath.Join(dir, "editor")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + record + "\n"
	if err := os.WriteFile(editor, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, `it's "q" $(touch pwned) ;*`, "config.toml")
	argv := EditorArgv(func(k string) string {
		if k == "EDITOR" {
			return editor + " --wait"
		}
		return ""
	}, path)
	cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if want := "--wait\n" + path + "\n"; string(got) != want {
		t.Fatalf("editor args %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("path was evaluated by the shell")
	}
}
