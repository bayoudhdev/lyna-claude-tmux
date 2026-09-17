package cli

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
)

func TestConfigCommandsCLI(t *testing.T) {
	e := newOfflineEnv(t)
	e.env["EDITOR"] = "nano -w"
	file := e.configFile()
	const invalid = "[ui]\ntheme = \"nope\"\n"
	editTo := func(content string) func(t *testing.T) {
		return func(t *testing.T) {
			t.Helper()
			e.onRun = func(argv []string) error {
				if want := app.EditorArgv(func(k string) string { return e.env[k] }, file); !slices.Equal(argv, want) {
					t.Errorf("editor argv %q, want %q", argv, want)
				}
				before, err := os.ReadFile(argv[len(argv)-1])
				if err != nil {
					return err
				}
				if len(before) == 0 {
					t.Error("editor opened an empty file")
				}
				return os.WriteFile(argv[len(argv)-1], []byte(content), 0o600)
			}
		}
	}
	fileIs := func(want []byte) func(t *testing.T, stdout string) {
		return func(t *testing.T, _ string) {
			t.Helper()
			if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("config file %q (%v), want %q", got, err, want)
			}
		}
	}
	showDecodes := func(theme string) func(t *testing.T, stdout string) {
		return func(t *testing.T, stdout string) {
			t.Helper()
			cfg, err := config.Decode([]byte(stdout))
			if err != nil {
				t.Fatalf("show output does not decode: %v\n%s", err, stdout)
			}
			if cfg.UI.Theme != theme {
				t.Fatalf("theme %q, want %q", cfg.UI.Theme, theme)
			}
		}
	}
	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
		check    func(t *testing.T, stdout string)
	}{
		{name: "group prints help", args: []string{"config"}, outHas: []string{"path", "init", "show", "validate", "edit"}},
		{name: "unknown subcommand", args: []string{"config", "nope"}, wantCode: 1, errHas: []string{"unknown command"}},
		{name: "path", args: []string{"config", "path"}, check: func(t *testing.T, stdout string) {
			t.Helper()
			if stdout != file+"\n" {
				t.Fatalf("stdout %q, want %q", stdout, file)
			}
		}},
		{name: "validate without file", args: []string{"config", "validate"}, outHas: []string{"No file at " + file, "lmux config init"}},
		{name: "show defaults", args: []string{"config", "show"}, outHas: []string{"# Defaults: there is no file at " + file}, check: showDecodes(config.Default().UI.Theme)},
		{name: "init", args: []string{"config", "init"}, outHas: []string{"Wrote " + file}, check: fileIs(config.Template())},
		{name: "init keeps existing", args: []string{"config", "init"}, wantCode: 1, errHas: []string{"already exists", "--force"}, check: fileIs(config.Template())},
		{name: "validate template", args: []string{"config", "validate"}, outHas: []string{file + " is valid"}},
		{name: "edit", setup: editTo("[ui]\ntheme = \"light\"\n"), args: []string{"config", "edit"}, outHas: []string{file + " is valid"}},
		{name: "show edited", args: []string{"config", "show"}, outHas: []string{"# Effective configuration from " + file}, check: showDecodes("light")},
		{name: "edit to invalid keeps the edit", setup: editTo(invalid), args: []string{"config", "edit"}, wantCode: 1, errHas: []string{"theme"}, check: fileIs([]byte(invalid))},
		{name: "validate invalid", args: []string{"config", "validate"}, wantCode: 1, errHas: []string{file, "theme"}},
		{name: "show invalid", args: []string{"config", "show"}, wantCode: 1, errHas: []string{"theme"}},
		{name: "init force keeps a backup", args: []string{"config", "init", "--force"}, outHas: []string{"Saved the previous file to " + file + ".bak", "Wrote " + file}, check: func(t *testing.T, stdout string) {
			t.Helper()
			fileIs(config.Template())(t, stdout)
			if got, err := os.ReadFile(file + ".bak"); err != nil || string(got) != invalid {
				t.Fatalf("backup %q (%v)", got, err)
			}
		}},
		{
			name: "editor failure", args: []string{"config", "edit"}, wantCode: 1, errHas: []string{"editor: exit status 3"},
			setup: func(t *testing.T) { t.Helper(); e.onRun = func([]string) error { return errors.New("exit status 3") } },
		},
		{
			name: "edit creates the file first",
			setup: func(t *testing.T) {
				t.Helper()
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				editTo("[workspace]\nlayout = \"trio\"\n")(t)
			},
			args: []string{"config", "edit"}, outHas: []string{file + " is valid"},
		},
		{
			name: "edit follows a symlinked file",
			setup: func(t *testing.T) {
				t.Helper()
				target := e.root + "/dotfiles.toml"
				if err := os.WriteFile(target, []byte("[ui]\nclock = false\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
				editTo("[ui]\ntheme = \"ansi\"\n")(t)
			},
			args: []string{"config", "edit"}, outHas: []string{file + " is valid"},
			check: func(t *testing.T, _ string) {
				t.Helper()
				if info, err := os.Lstat(file); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink replaced: %v", err)
				}
				if got, _ := os.ReadFile(e.root + "/dotfiles.toml"); !strings.Contains(string(got), "ansi") {
					t.Fatalf("target %q", got)
				}
			},
		},
		{name: "init refuses a symlinked file", args: []string{"config", "init", "--force"}, wantCode: 1, errHas: []string{"symbolic link"}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			e.onRun = nil
			runsBefore := len(e.runs)
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := e.run(t, st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			for _, s := range st.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t, stdout)
			}
			if ran := len(e.runs) - runsBefore; (st.args[len(st.args)-1] == "edit") != (ran == 1) {
				t.Fatalf("%d program runs for %q", ran, st.args)
			}
		})
	}
}
