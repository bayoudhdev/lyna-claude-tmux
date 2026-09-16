package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

const statusCommit = "1111111111111111111111111111111111111111"

func statusPin() domain.Pin {
	return domain.Pin{
		Repo:    "https://example.invalid/codediff.nvim",
		Commit:  statusCommit,
		Version: "4.0.6",
		Assets: map[string][]domain.Asset{
			"darwin/arm64": {{Name: "mac.dylib", File: "libvscode_diff_4.0.6.dylib", SHA256: sha(macLib)}},
			"linux/amd64": {
				{Name: "linux.so", File: "libvscode_diff_4.0.6.so", SHA256: sha(linuxLib)},
				{Name: "gomp.so.1", File: "libgomp.so.1", SHA256: sha(gompLib)},
			},
		},
	}
}

// fakeInstall lays out a verified installation without git or network.
func fakeInstall(t *testing.T, paths xdg.Paths, files map[string]string) {
	t.Helper()
	tree := map[string]string{
		".git/HEAD":                  statusCommit + "\n",
		"VERSION":                    "4.0.6\n",
		"plugin/codediff.lua":        "-- plugin\n",
		"libvscode_diff_4.0.6.dylib": macLib,
		"libvscode_diff_4.0.6.so":    linuxLib,
		"libgomp.so.1":               gompLib,
	}
	for k, v := range files {
		if v == "" {
			delete(tree, k)
			continue
		}
		tree[k] = v
	}
	writeTree(t, PluginDir(paths), tree)
}

func nvimRunner(out string, err error) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, bin string, args ...string) ([]byte, error) {
		if !filepath.IsAbs(bin) || !slices.Equal(args, []string{"--version"}) {
			return nil, errors.New("unexpected invocation " + bin + " " + strings.Join(args, " "))
		}
		return []byte(out), err
	}
}

func foundNvim(string) (string, error) { return "/opt/bin/nvim", nil }

func TestStatus(t *testing.T) {
	cases := []struct {
		name         string
		setup        func(t *testing.T, paths xdg.Paths)
		goos         string
		lookPath     func(string) (string, error)
		run          func(context.Context, string, ...string) ([]byte, error)
		wantReady    bool
		wantPluginOK bool
		wantProblems []string // substrings, in order
		wantWarnings []string
		check        func(t *testing.T, r Report)
	}{
		{
			name:         "verified darwin install with current Neovim",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			wantReady:    true,
			wantPluginOK: true,
			check: func(t *testing.T, r Report) {
				if r.Nvim != "/opt/bin/nvim" || r.NvimVersion != (domain.NvimVersion{Major: 0, Minor: 12, Patch: 5}) || !r.NvimRecommends {
					t.Errorf("nvim fields = %q %v %v", r.Nvim, r.NvimVersion, r.NvimRecommends)
				}
				if len(r.Assets) != 1 || !r.Assets[0].Present || !r.Assets[0].ChecksumOK {
					t.Errorf("assets = %+v", r.Assets)
				}
				if r.Commit != statusCommit || r.Version != "4.0.6" || r.Platform != "darwin/arm64" {
					t.Errorf("report = %+v", r)
				}
			},
		},
		{
			name:         "linux checks libgomp",
			goos:         "linux",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, map[string]string{"libgomp.so.1": "tampered"}) },
			wantProblems: []string{"libgomp.so.1 does not match its pinned checksum"},
			check: func(t *testing.T, r Report) {
				if len(r.Assets) != 2 || !r.Assets[0].ChecksumOK || r.Assets[1].ChecksumOK || !r.Assets[1].Present {
					t.Errorf("assets = %+v", r.Assets)
				}
			},
		},
		{
			name:         "not installed",
			wantProblems: []string{"codediff.nvim is not installed (run: lyna-tmux review install)"},
			check: func(t *testing.T, r Report) {
				if r.Installed || r.InitFile {
					t.Errorf("report = %+v", r)
				}
			},
		},
		{
			name: "init file is reported",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, nil)
				writeTree(t, p.ReviewDir(), map[string]string{InitFileName: "-- init"})
			},
			wantReady:    true,
			wantPluginOK: true,
			check: func(t *testing.T, r Report) {
				if !r.InitFile {
					t.Error("InitFile = false")
				}
			},
		},
		{
			name: "wrong commit and version",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, map[string]string{".git/HEAD": "ref: refs/heads/main\n", "VERSION": "4.0.5\n"})
			},
			wantProblems: []string{`at commit "ref: refs/heads/main"`, `VERSION is "4.0.5"`},
		},
		{
			name: "missing library",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, map[string]string{"libvscode_diff_4.0.6.dylib": ""})
			},
			wantProblems: []string{"libvscode_diff_4.0.6.dylib is missing"},
		},
		{
			name: "library replaced by a symlink",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, map[string]string{"libvscode_diff_4.0.6.dylib": ""})
				other := filepath.Join(t.TempDir(), "lib")
				writeTree(t, filepath.Dir(other), map[string]string{"lib": macLib})
				if err := os.Symlink(other, filepath.Join(PluginDir(p), "libvscode_diff_4.0.6.dylib")); err != nil {
					t.Fatal(err)
				}
			},
			wantProblems: []string{"libvscode_diff_4.0.6.dylib cannot be verified"},
		},
		{
			name: "unverified manual build and watcher",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, map[string]string{"libvscode_diff.dylib": "manual", "codediff-watcher": "#!/bin/sh"})
			},
			wantProblems: []string{`unverified file "codediff-watcher"`, `unverified file "libvscode_diff.dylib"`},
		},
		{
			name: "plugin directory is a symlink",
			setup: func(t *testing.T, p xdg.Paths) {
				mustMkdir(t, p.ReviewDir())
				if err := os.Symlink(t.TempDir(), PluginDir(p)); err != nil {
					t.Fatal(err)
				}
			},
			wantProblems: []string{"refusing to follow symbolic link"},
		},
		{
			name:         "unsupported platform",
			goos:         "freebsd",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			wantProblems: []string{"no codediff.nvim build for this platform"},
		},
		{
			name:         "nvim missing",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			lookPath:     func(string) (string, error) { return "", errors.New("not found") },
			wantPluginOK: true,
			wantProblems: []string{"Neovim (nvim) was not found on PATH"},
		},
		{
			name:         "nvim too old",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			run:          nvimRunner("NVIM v0.8.3\n", nil),
			wantPluginOK: true,
			wantProblems: []string{"Neovim v0.8.3 is too old; v0.9.0 or newer is required"},
		},
		{
			name:         "nvim supported but not recommended",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			run:          nvimRunner("NVIM v0.9.5\n", nil),
			wantReady:    true,
			wantPluginOK: true,
			wantWarnings: []string{"Neovim v0.9.5 works; v0.10.0 or newer is recommended"},
		},
		{
			name:         "nvim version fails",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			run:          nvimRunner("", errors.New("exit status 1")),
			wantPluginOK: true,
			wantProblems: []string{"/opt/bin/nvim --version failed: exit status 1"},
		},
		{
			name:         "nvim version unreadable",
			setup:        func(t *testing.T, p xdg.Paths) { fakeInstall(t, p, nil) },
			run:          nvimRunner("VIM 9.1\n", nil),
			wantPluginOK: true,
			wantProblems: []string{"unrecognized Neovim version"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := testPaths(t)
			if tc.setup != nil {
				tc.setup(t, paths)
			}
			opts := StatusOptions{Paths: paths, Pin: statusPin(), GOOS: "darwin", GOARCH: "arm64", LookPath: foundNvim, Run: nvimRunner("NVIM v0.12.5\nBuild type: Release\n", nil)}
			if tc.goos != "" {
				opts.GOOS, opts.GOARCH = tc.goos, "amd64"
			}
			if tc.lookPath != nil {
				opts.LookPath = tc.lookPath
			}
			if tc.run != nil {
				opts.Run = tc.run
			}
			r := Status(context.Background(), opts)
			if r.Ready() != tc.wantReady || r.PluginOK() != tc.wantPluginOK {
				t.Errorf("Ready() = %v, PluginOK() = %v; want %v, %v (problems %q)", r.Ready(), r.PluginOK(), tc.wantReady, tc.wantPluginOK, r.Problems())
			}
			assertMessages(t, "Problems", r.Problems(), tc.wantProblems)
			// Problems is exactly the plugin problems followed by the Neovim ones.
			if split := append(r.PluginProblems(), r.NvimProblems()...); !slices.Equal(split, r.Problems()) {
				t.Errorf("PluginProblems() + NvimProblems() = %q, Problems() = %q", split, r.Problems())
			}
			assertMessages(t, "Warnings", r.Warnings(), tc.wantWarnings)
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

func assertMessages(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s() = %q, want %d messages containing %q", what, got, len(want), want)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("%s()[%d] = %q, want it to contain %q", what, i, got[i], want[i])
		}
	}
}

func TestStatusDefaults(t *testing.T) {
	// Real platform, default pin, PATH lookup of a missing binary: nothing is
	// executed and the report still names the problems.
	paths := testPaths(t)
	r := Status(context.Background(), StatusOptions{Paths: paths, Nvim: "lyna-tmux-test-no-such-nvim"})
	if r.Installed || r.PluginDir != PluginDir(paths) || r.NvimFound {
		t.Fatalf("report = %+v", r)
	}
	if got := r.Problems(); len(got) != 2 {
		t.Fatalf("Problems() = %q, want not installed and Neovim missing", got)
	}
	cases := []struct {
		name string
		got  []string
		want string
	}{
		{name: "plugin problems name the install command", got: r.PluginProblems(), want: "codediff.nvim is not installed (run: lyna-tmux review install)"},
		{name: "Neovim problems name the binary", got: r.NvimProblems(), want: "Neovim (lyna-tmux-test-no-such-nvim) was not found on PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.got) != 1 || !strings.Contains(tc.got[0], tc.want) {
				t.Fatalf("got %q, want one message containing %q", tc.got, tc.want)
			}
		})
	}
}

func TestPathsHelpers(t *testing.T) {
	paths := xdg.Paths{Data: "/d/lyna-tmux"}
	cases := []struct{ name, got, want string }{
		{"plugin dir", PluginDir(paths), "/d/lyna-tmux/review/codediff.nvim"},
		{"init file", InitFile(paths), "/d/lyna-tmux/review/init.lua"},
		{"log file", LogFile(paths), "/d/lyna-tmux/review/nvim.log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestUninstall(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, p xdg.Paths)
		paths   func(p xdg.Paths) xdg.Paths
		wantErr error
		errHas  string
		check   func(t *testing.T, p xdg.Paths)
	}{
		{
			name: "removes plugin, init file and log",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, nil)
				writeTree(t, p.ReviewDir(), map[string]string{InitFileName: "x", LogFileName: "y"})
			},
			check: func(t *testing.T, p xdg.Paths) {
				if _, err := os.Lstat(p.ReviewDir()); !os.IsNotExist(err) {
					t.Errorf("review directory still exists: %v", err)
				}
				if _, err := os.Stat(p.Data); err != nil {
					t.Errorf("data directory removed too: %v", err)
				}
			},
		},
		{name: "nothing installed"},
		{
			name: "symlinked review directory is refused",
			setup: func(t *testing.T, p xdg.Paths) {
				target := t.TempDir()
				writeTree(t, target, map[string]string{"keep": "me"})
				mustMkdir(t, p.Data)
				if err := os.Symlink(target, p.ReviewDir()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
						t.Errorf("symlink target was modified: %v", err)
					}
				})
			},
			wantErr: fsx.ErrSymlink,
		},
		{
			name:   "relative data directory is refused",
			paths:  func(xdg.Paths) xdg.Paths { return xdg.Paths{Data: "data"} },
			errHas: "refusing to remove",
		},
		{
			name: "busy install blocks uninstall",
			setup: func(t *testing.T, p xdg.Paths) {
				fakeInstall(t, p, nil)
				unlock, err := lockDir(p.ReviewDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(unlock)
			},
			wantErr: ErrBusy,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := testPaths(t)
			if tc.setup != nil {
				tc.setup(t, paths)
			}
			if tc.paths != nil {
				paths = tc.paths(paths)
			}
			err := Uninstall(paths)
			switch {
			case tc.wantErr == nil && tc.errHas == "" && err != nil:
				t.Fatalf("Uninstall() error = %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("Uninstall() error = %v, want %v", err, tc.wantErr)
			case tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)):
				t.Fatalf("Uninstall() error = %v, want %q", err, tc.errHas)
			}
			if tc.check != nil {
				tc.check(t, paths)
			}
		})
	}
}
