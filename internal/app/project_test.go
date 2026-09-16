package app

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

func initWrite(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// initSnapshot returns every path below root with its content, for proving
// that a step wrote nothing.
func initSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[rel] = string(data)
			return nil
		}
		out[rel] = d.Type().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitProjectPlan(t *testing.T) {
	cases := []struct {
		name        string
		config      string
		setup       func(t *testing.T, root string)
		wantErr     error
		errHas      string
		wantChanged []bool
		check       func(t *testing.T, p ProjectPlan)
	}{
		{
			name: "new project", wantChanged: []bool{true, true, true},
			check: func(t *testing.T, p ProjectPlan) {
				settings, include, ignore := p.Files[0], p.Files[1], p.Files[2]
				if settings.Rel != ".claude/settings.json" || settings.Exists || !strings.Contains(string(settings.New), `"enabled": true`) || !strings.Contains(settings.Diff, `+  "sandbox": {`) {
					t.Fatalf("settings %+v\n%s", settings, settings.New)
				}
				if string(include.New) != ".env\n.env.*\n" || string(ignore.New) != ".claude/worktrees/\n.claude/settings.local.json\n" {
					t.Fatalf("include %q ignore %q", include.New, ignore.New)
				}
				if p.Profile != sandbox.Standard || strings.Contains(string(settings.New), "strictAllowlist") {
					t.Fatalf("profile %s\n%s", p.Profile, settings.New)
				}
			},
		},
		{
			name: "existing files are merged, not replaced", wantChanged: []bool{true, false, true},
			setup: func(t *testing.T, root string) {
				initWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"model": "opus"}`, 0o600)
				initWrite(t, filepath.Join(root, ".worktreeinclude"), ".env\n/.env.*\n", 0o644)
				initWrite(t, filepath.Join(root, ".gitignore"), "node_modules", 0o644)
			},
			check: func(t *testing.T, p ProjectPlan) {
				if !strings.Contains(string(p.Files[0].New), `"model": "opus"`) || !p.Files[0].Exists {
					t.Fatalf("settings lost the existing key:\n%s", p.Files[0].New)
				}
				if p.Files[1].Diff != "" {
					t.Fatalf("unchanged file has a diff: %q", p.Files[1].Diff)
				}
				if string(p.Files[2].New) != "node_modules\n.claude/worktrees/\n.claude/settings.local.json\n" {
					t.Fatalf("gitignore %q", p.Files[2].New)
				}
			},
		},
		{
			name: "strict profile with the project's ecosystems", config: "[sandbox]\nprofile = \"strict\"\nallow_write = [\"/private/build\"]\n", wantChanged: []bool{true, true, true},
			setup: func(t *testing.T, root string) { initWrite(t, filepath.Join(root, "go.mod"), "module x\n", 0o644) },
			check: func(t *testing.T, p ProjectPlan) {
				s := string(p.Files[0].New)
				if p.Profile != sandbox.Strict || !strings.Contains(s, "proxy.golang.org") || !strings.Contains(s, "blockReadsOutsideWorkingDirectories") || strings.Contains(s, "/private/build") {
					t.Fatalf("strict settings:\n%s", s)
				}
			},
		},
		{name: "off in the configuration", config: "[sandbox]\nprofile = \"off\"\n", wantErr: ErrSandboxOffInConfig, errHas: "lyna-tmux config edit"},
		{name: ".claude is a link", setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "elsewhere"))
			if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, ".claude")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: fsx.ErrSymlink},
		{name: "settings file is a link", setup: func(t *testing.T, root string) {
			initWrite(t, filepath.Join(root, "real.json"), "{}", 0o644)
			mkdir(t, filepath.Join(root, ".claude"))
			if err := os.Symlink(filepath.Join(root, "real.json"), filepath.Join(root, ".claude", "settings.json")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: fsx.ErrSymlink},
		{name: ".gitignore is a directory", setup: func(t *testing.T, root string) { mkdir(t, filepath.Join(root, ".gitignore")) }, errHas: "not a regular file"},
		{name: ".claude is a file", setup: func(t *testing.T, root string) { initWrite(t, filepath.Join(root, ".claude"), "", 0o644) }, wantErr: fsx.ErrNotDir},
		{name: "settings are not JSON", setup: func(t *testing.T, root string) {
			initWrite(t, filepath.Join(root, ".claude", "settings.json"), "{nope", 0o644)
		}, errHas: "parse project settings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, nil)
			root := devcontainerProject(t, h)
			if tc.config != "" {
				sandboxWriteConfig(t, h.Getenv("LYNA_TMUX_HOME"), tc.config)
			}
			if tc.setup != nil {
				tc.setup(t, root)
			}
			before := initSnapshot(t, root)
			p, err := InitProjectPlan(h, filepath.Join(root, "services", "web"))
			if tc.wantErr != nil || tc.errHas != "" {
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Root != root {
				t.Fatalf("root %s, want %s", p.Root, root)
			}
			var changed []bool
			for _, f := range p.Files {
				changed = append(changed, f.Changed)
				if f.Changed == (f.Diff == "") {
					t.Fatalf("%s: changed %v with diff %q", f.Rel, f.Changed, f.Diff)
				}
			}
			if !slices.Equal(changed, tc.wantChanged) || p.Changed() != slices.Contains(changed, true) {
				t.Fatalf("changed %v, want %v", changed, tc.wantChanged)
			}
			tc.check(t, p)
			if after := initSnapshot(t, root); !maps.Equal(before, after) {
				t.Fatalf("planning wrote files:\nbefore %v\nafter %v", before, after)
			}
		})
	}
}

func TestProjectPlanApply(t *testing.T) {
	t.Run("writes, backs up and is idempotent", func(t *testing.T) {
		h := isolationHost(t, nil)
		root := devcontainerProject(t, h)
		settings := filepath.Join(root, ".claude", "settings.json")
		initWrite(t, settings, `{"model": "opus"}`, 0o600)
		p, err := InitProjectPlan(h, root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Apply()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{settings, filepath.Join(root, ".worktreeinclude"), filepath.Join(root, ".gitignore")}
		if !slices.Equal(got.Written, want) || got.Backup != settings+".bak" {
			t.Fatalf("applied %+v", got)
		}
		for i, f := range p.Files {
			data, err := os.ReadFile(want[i])
			if err != nil || string(data) != string(f.New) {
				t.Fatalf("%s holds %q, want %q (%v)", want[i], data, f.New, err)
			}
		}
		if data, _ := os.ReadFile(got.Backup); string(data) != `{"model": "opus"}` {
			t.Fatalf("backup %q", data)
		}
		assertMode(t, settings, 0o600)
		assertMode(t, got.Backup, 0o600)
		assertMode(t, want[1], 0o644)
		again, err := InitProjectPlan(h, root)
		if err != nil || again.Changed() {
			t.Fatalf("second plan changed %v (%v)", again.Changed(), err)
		}
		if applied, err := again.Apply(); err != nil || len(applied.Written) != 0 {
			t.Fatalf("second apply %+v, %v", applied, err)
		}
	})
	t.Run("new settings directory", func(t *testing.T) {
		h := isolationHost(t, nil)
		root := devcontainerProject(t, h)
		p, err := InitProjectPlan(h, root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Apply()
		if err != nil || got.Backup != "" {
			t.Fatalf("applied %+v, %v", got, err)
		}
		assertMode(t, filepath.Join(root, ".claude"), 0o755)
		assertMode(t, filepath.Join(root, ".claude", "settings.json"), 0o644)
	})
	cases := []struct {
		name    string
		between func(t *testing.T, root string)
		wantErr error
	}{
		{name: "a file changed after the preview", between: func(t *testing.T, root string) {
			initWrite(t, filepath.Join(root, ".gitignore"), "dist\n", 0o644)
		}, wantErr: ErrProjectChanged},
		{name: "a file appeared after the preview", between: func(t *testing.T, root string) {
			initWrite(t, filepath.Join(root, ".claude", "settings.json"), "{}", 0o644)
		}, wantErr: ErrProjectChanged},
		{name: "a link was planted after the preview", between: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "elsewhere"))
			if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, ".claude")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: fsx.ErrSymlink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, nil)
			root := devcontainerProject(t, h)
			p, err := InitProjectPlan(h, root)
			if err != nil {
				t.Fatal(err)
			}
			tc.between(t, root)
			before := initSnapshot(t, root)
			if _, err := p.Apply(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if after := initSnapshot(t, root); !maps.Equal(before, after) {
				t.Fatalf("refused apply wrote files:\nbefore %v\nafter %v", before, after)
			}
		})
	}
}
