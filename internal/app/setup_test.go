package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestSaveSetup(t *testing.T) {
	edited := config.Default()
	edited.UI.Theme = "light"
	edited.Claude.Model = "opus"
	invalid := config.Default()
	invalid.UI.Theme = "neon"
	const previous = "# my notes\n[ui]\ntheme = \"ansi\"\n"
	cases := []struct {
		name       string
		setup      func(t *testing.T, h *testHost, path string)
		cfg        config.Config
		wantErr    error
		errHas     string
		wantBackup bool
		// wantFile is the configuration file content after the call; empty
		// means the saved configuration.
		wantFile string
	}{
		{name: "first file", cfg: edited},
		{name: "existing file is backed up", setup: func(t *testing.T, h *testHost, _ string) {
			t.Helper()
			h.writeConfig(t, previous)
		}, cfg: edited, wantBackup: true},
		{name: "invalid configuration writes nothing", setup: func(t *testing.T, h *testHost, _ string) {
			t.Helper()
			h.writeConfig(t, previous)
		}, cfg: invalid, errHas: "neon", wantFile: previous},
		{name: "linked configuration is refused", setup: func(t *testing.T, h *testHost, path string) {
			t.Helper()
			target := filepath.Join(h.root, "dotfiles.toml")
			if err := os.WriteFile(target, []byte(previous), 0o600); err != nil {
				t.Fatal(err)
			}
			mkdir(t, filepath.Dir(path))
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}, cfg: edited, wantErr: ErrConfigSymlink, wantFile: previous},
		{name: "linked backup is refused before the file is replaced", setup: func(t *testing.T, h *testHost, path string) {
			t.Helper()
			h.writeConfig(t, previous)
			if err := os.Symlink(filepath.Join(h.root, "elsewhere"), path+".bak"); err != nil {
				t.Fatal(err)
			}
		}, cfg: edited, wantErr: fsx.ErrSymlink, wantFile: previous},
		{name: "configuration path is a directory", setup: func(t *testing.T, _ *testHost, path string) {
			t.Helper()
			mkdir(t, path)
		}, cfg: edited, errHas: "not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHost(t)
			path := filepath.Join(h.root, "config", "config.toml")
			if tc.setup != nil {
				tc.setup(t, h, path)
			}
			res, err := SaveSetup(h.Host, tc.cfg)
			switch {
			case tc.wantErr != nil || tc.errHas != "":
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) || err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("error %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
			case err != nil:
				t.Fatal(err)
			default:
				if res.Path != path {
					t.Fatalf("path %q, want %q", res.Path, path)
				}
				got, found, err := config.Load(path)
				if err != nil || !found || !reflect.DeepEqual(got, tc.cfg) {
					t.Fatalf("saved configuration %+v (found %v, %v), want %+v", got, found, err, tc.cfg)
				}
				assertMode(t, path, 0o600)
			}
			if tc.wantFile != "" {
				data, err := fsx.ReadFileLimited(path, config.MaxFileSize)
				if err != nil || string(data) != tc.wantFile {
					t.Fatalf("configuration file %q (%v), want %q", data, err, tc.wantFile)
				}
			}
			if tc.wantBackup {
				if res.Backup != path+".bak" {
					t.Fatalf("backup %q", res.Backup)
				}
				data, err := os.ReadFile(res.Backup)
				if err != nil || string(data) != previous {
					t.Fatalf("backup %q (%v), want the previous file", data, err)
				}
				assertMode(t, res.Backup, 0o600)
			} else if res.Backup != "" {
				t.Fatalf("unexpected backup %q", res.Backup)
			}
		})
	}
}

func TestApplySetup(t *testing.T) {
	h := newTestHost(t)
	ctx := tmuxtest.Context(t)
	running, err := ApplySetup(ctx, h.Host)
	if err != nil || running {
		t.Fatalf("stopped server: running %v, %v", running, err)
	}
	s := openServer(t, h)
	if _, err := s.Client.Run(ctx, "list-sessions"); !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("applying to a stopped server started one: %v", err)
	}
	startWorkspace(t, s, "apply")
	if err := s.MarkLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Client.ShowOption(ctx, "-g", "", "status-position"); got != "bottom" {
		t.Fatalf("status-position before %q", got)
	}
	cfg := config.Default()
	cfg.UI.StatusPosition = "top"
	if _, err := SaveSetup(h.Host, cfg); err != nil {
		t.Fatal(err)
	}
	running, err = ApplySetup(ctx, h.Host)
	if err != nil || !running {
		t.Fatalf("running server: running %v, %v", running, err)
	}
	if got, _ := s.Client.ShowOption(ctx, "-g", "", "status-position"); got != "top" {
		t.Fatalf("status-position after apply %q, want top", got)
	}
	applied := openServer(t, h)
	if got, _ := s.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash); got != applied.Fingerprint || got == s.Fingerprint {
		t.Fatalf("loaded fingerprint %q, want %q (was %q)", got, applied.Fingerprint, s.Fingerprint)
	}

	missing := h.Host
	missing.TmuxBin = filepath.Join(h.root, "no-tmux")
	if _, err := ApplySetup(ctx, missing); !errors.Is(err, tmux.ErrNotInstalled) {
		t.Fatalf("apply without tmux: %v", err)
	}
}
