package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// reviewFakeLibrary is the native library of the test plugin build.
const reviewFakeLibrary = "fake diff library"

// reviewTestSource is a darwin/arm64 plugin build whose native file is
// reviewFakeLibrary, so tests verify installations they write themselves.
func reviewTestSource() ReviewPlugin {
	sum := sha256.Sum256([]byte(reviewFakeLibrary))
	return ReviewPlugin{
		GOOS: "darwin", GOARCH: "arm64",
		Pin: domain.Pin{
			Repo:    "https://example.invalid/codediff.nvim",
			Commit:  strings.Repeat("ab", 20),
			Version: "4.0.6",
			Assets: map[string][]domain.Asset{"darwin/arm64": {{
				Name: "lib.dylib", File: domain.LibraryFile("4.0.6", "darwin"), SHA256: hex.EncodeToString(sum[:]),
			}}},
		},
	}
}

// reviewHost is a host with its own LYNA_TMUX_HOME, a fake Neovim printing
// version and the real git.
type reviewHost struct {
	Host
	env   map[string]string
	root  string
	nvim  string
	paths xdg.Paths
}

func newReviewHost(t *testing.T, version string) *reviewHost {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &reviewHost{root: root, env: map[string]string{
		"LYNA_TMUX_HOME": root, "HOME": root, "PATH": os.Getenv("PATH"),
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": "en_US.UTF-8",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
	}}
	h.nvim = filepath.Join(root, "bin", "nvim")
	if err := os.MkdirAll(filepath.Dir(h.nvim), 0o700); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(h.nvim, []byte("#!/bin/sh\necho 'NVIM "+version+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h.Host = Host{Getenv: func(k string) string { return h.env[k] }, Home: root, LookPath: func(name string) (string, error) {
		if name == "nvim" {
			if version == "" {
				return "", exec.ErrNotFound
			}
			return h.nvim, nil
		}
		return exec.LookPath(name)
	}}
	h.refresh()
	if h.paths, err = xdg.Resolve(h.Getenv, h.Home); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *reviewHost) refresh() {
	h.Environ = h.Environ[:0]
	for k, v := range h.env {
		h.Environ = append(h.Environ, k+"="+v)
	}
}

func (h *reviewHost) config(t *testing.T, body string) {
	t.Helper()
	path := h.paths.ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// install writes a verified installation of src the way Install leaves one.
func (h *reviewHost) install(t *testing.T, src ReviewPlugin, library string) {
	t.Helper()
	dir := review.PluginDir(h.paths)
	for _, d := range []string{h.paths.Data, h.paths.ReviewDir(), dir, filepath.Join(dir, ".git")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".git/HEAD":                            src.Pin.Commit + "\n",
		"VERSION":                              src.Pin.Version + "\n",
		src.Pin.Assets["darwin/arm64"][0].File: library,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// reviewRepo creates an empty git repository.
func reviewRepo(t *testing.T, h *reviewHost) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", dir)
	cmd.Env = h.Environ
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func reviewEnvValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestReviewPluginPin(t *testing.T) {
	cases := []struct {
		name string
		src  ReviewPlugin
		want string
	}{
		{name: "zero value is the default pin", want: domain.DefaultPin().Commit},
		{name: "explicit pin is kept", src: reviewTestSource(), want: strings.Repeat("ab", 20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.src.pin().Commit; got != tc.want {
				t.Fatalf("pin commit %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReviewLaunch(t *testing.T) {
	src := reviewTestSource()
	cases := []struct {
		name    string
		config  string
		env     map[string]string
		version string // fake Neovim version; "" means no Neovim
		install string // library content to install; "" means not installed
		req     func(t *testing.T, h *reviewHost, repo string) ReviewRequest
		wantErr error
		errHas  string
		check   func(t *testing.T, h *reviewHost, repo string, l review.Launch)
	}{
		{
			name: "isolated editor with the configured theme", version: "v0.12.5", install: reviewFakeLibrary,
			check: func(t *testing.T, h *reviewHost, repo string, l review.Launch) {
				want := []string{h.nvim, "--clean", "-u", review.InitFile(h.paths), "-i", "NONE", "-n", "-c", domain.Launcher}
				if l.Path != h.nvim || !slices.Equal(l.Args, want) {
					t.Fatalf("launch %q %q, want %q", l.Path, l.Args, want)
				}
				reviewAssertEnv(t, l.Env, map[string]string{domain.EnvDir: repo, domain.EnvArgs: `["--exit-on-close"]`, domain.EnvNoAutoInstall: "1", "HOME": h.root})
				reviewAssertInit(t, h, []string{`line_insert = "#163b24"`, "vim.o.termguicolors = true", `vim.o.background = "dark"`, `filler_text = "╱"`}, []string{"layout ="})
			},
		},
		{
			name: "configured layout reaches the plugin and the arguments", config: "[review]\nlayout = \"inline\"\n", version: "v0.12.5", install: reviewFakeLibrary,
			check: func(t *testing.T, h *reviewHost, _ string, l review.Launch) {
				reviewAssertEnv(t, l.Env, map[string]string{domain.EnvArgs: `["--exit-on-close","--inline"]`})
				reviewAssertInit(t, h, []string{`layout = "inline"`}, nil)
			},
		},
		{
			name: "requested layout overrides the configured one", config: "[review]\nlayout = \"inline\"\n", version: "v0.12.5", install: reviewFakeLibrary,
			req: func(_ *testing.T, _ *reviewHost, repo string) ReviewRequest {
				return ReviewRequest{Request: domain.Request{Layout: domain.LayoutSideBySide, Mode: domain.ModeRevision, Revisions: []string{"main..."}}, Dir: repo}
			},
			check: func(t *testing.T, _ *reviewHost, _ string, l review.Launch) {
				reviewAssertEnv(t, l.Env, map[string]string{domain.EnvArgs: `["--exit-on-close","--side-by-side","main..."]`})
			},
		},
		{
			name: "light theme with ascii icons and 256 colors", config: "[ui]\ntheme = \"light\"\n", env: map[string]string{"COLORTERM": "", "LANG": "C"},
			version: "v0.12.5", install: reviewFakeLibrary,
			check: func(t *testing.T, h *reviewHost, _ string, _ review.Launch) {
				reviewAssertInit(t, h, []string{"vim.o.termguicolors = false", `vim.o.background = "light"`, `filler_text = "/"`}, nil)
			},
		},
		{
			name: "configured user editor needs no installed plugin", config: "[review]\neditor = \"user\"\n", version: "v0.12.5",
			check: func(t *testing.T, h *reviewHost, repo string, l review.Launch) {
				if !slices.Equal(l.Args, []string{h.nvim, "-c", domain.Launcher}) {
					t.Fatalf("args %q", l.Args)
				}
				reviewAssertEnv(t, l.Env, map[string]string{domain.EnvDir: repo})
				if _, ok := reviewEnvValue(l.Env, domain.EnvNoAutoInstall); ok {
					t.Fatalf("user editor env sets %s", domain.EnvNoAutoInstall)
				}
				if _, err := os.Stat(review.InitFile(h.paths)); err == nil {
					t.Fatal("user editor wrote the isolated init file")
				}
			},
		},
		{
			name: "requested user editor overrides the configured isolated one", version: "v0.12.5",
			req: func(_ *testing.T, _ *reviewHost, repo string) ReviewRequest {
				return ReviewRequest{Editor: domain.EditorUser, Dir: repo}
			},
			check: func(t *testing.T, h *reviewHost, _ string, l review.Launch) {
				if !slices.Equal(l.Args, []string{h.nvim, "-c", domain.Launcher}) {
					t.Fatalf("args %q", l.Args)
				}
			},
		},
		{
			name: "invalid request", version: "v0.12.5", install: reviewFakeLibrary, wantErr: domain.ErrInvalidRequest,
			req: func(_ *testing.T, _ *reviewHost, repo string) ReviewRequest {
				return ReviewRequest{Request: domain.Request{Mode: domain.ModeRevision, Revisions: []string{"-x"}}, Dir: repo}
			},
		},
		{
			name: "not a git repository", version: "v0.12.5", install: reviewFakeLibrary, wantErr: ErrReviewNotRepository, errHas: "run it inside a repository",
			req: func(t *testing.T, _ *reviewHost, _ string) ReviewRequest { return ReviewRequest{Dir: t.TempDir()} },
		},
		{
			name: "relative directory", version: "v0.12.5", errHas: "is not absolute",
			req: func(*testing.T, *reviewHost, string) ReviewRequest { return ReviewRequest{Dir: "repo"} },
		},
		{
			name: "missing directory", version: "v0.12.5", errHas: "no such file",
			req: func(t *testing.T, _ *reviewHost, _ string) ReviewRequest {
				return ReviewRequest{Dir: filepath.Join(t.TempDir(), "gone")}
			},
		},
		{
			name: "file instead of a directory", version: "v0.12.5", errHas: "is not a directory",
			req: func(_ *testing.T, h *reviewHost, _ string) ReviewRequest { return ReviewRequest{Dir: h.nvim} },
		},
		{name: "Neovim missing", install: reviewFakeLibrary, wantErr: ErrReviewNeovim, errHas: "was not found on PATH"},
		{name: "Neovim too old", version: "v0.8.3", install: reviewFakeLibrary, wantErr: ErrReviewNeovim, errHas: "is too old"},
		{name: "Neovim too old for the user editor too", config: "[review]\neditor = \"user\"\n", version: "v0.8.3", wantErr: ErrReviewNeovim},
		{name: "plugin not installed", version: "v0.12.5", wantErr: ErrReviewPlugin, errHas: "run: lmux review install"},
		{name: "plugin modified", version: "v0.12.5", install: "tampered library", wantErr: ErrReviewPlugin, errHas: "lmux review install --force"},
		{name: "invalid configuration", config: "[review]\neditor = \"nope\"\n", version: "v0.12.5", errHas: "review.editor"},
		{
			name: "init file cannot be written", version: "v0.12.5", install: reviewFakeLibrary, wantErr: ErrReviewLaunch,
			req: func(t *testing.T, h *reviewHost, repo string) ReviewRequest {
				if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.lua"), review.InitFile(h.paths)); err != nil {
					t.Fatal(err)
				}
				return ReviewRequest{Dir: repo}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHost(t, tc.version)
			for k, v := range tc.env {
				h.env[k] = v
			}
			h.refresh()
			if tc.config != "" {
				h.config(t, tc.config)
			}
			if tc.install != "" {
				h.install(t, src, tc.install)
			}
			repo := reviewRepo(t, h)
			req := ReviewRequest{Dir: repo}
			if tc.req != nil {
				req = tc.req(t, h, repo)
			}
			l, err := ReviewLaunch(t.Context(), h.Host, src, req)
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			case tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.errHas)
			case tc.wantErr == nil && tc.errHas == "" && err != nil:
				t.Fatalf("err = %v", err)
			}
			if tc.check != nil {
				tc.check(t, h, repo, l)
			}
		})
	}
}

func reviewAssertEnv(t *testing.T, env []string, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got, ok := reviewEnvValue(env, name); !ok || got != value {
			t.Errorf("%s = %q (set %v), want %q", name, got, ok, value)
		}
	}
}

func reviewAssertInit(t *testing.T, h *reviewHost, has, lacks []string) {
	t.Helper()
	data, err := os.ReadFile(review.InitFile(h.paths))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range has {
		if !strings.Contains(string(data), s) {
			t.Errorf("init file lacks %q:\n%s", s, data)
		}
	}
	for _, s := range lacks {
		if strings.Contains(string(data), s) {
			t.Errorf("init file has %q:\n%s", s, data)
		}
	}
}

func TestReviewStatus(t *testing.T) {
	src := reviewTestSource()
	cases := []struct {
		name         string
		config       string
		version      string
		install      bool
		src          ReviewPlugin
		wantReady    bool
		wantProblems []string
		wantEditor   domain.EditorMode
		wantPin      string
	}{
		{name: "installed and verified", version: "v0.12.5", install: true, src: src, wantReady: true, wantEditor: domain.EditorIsolated, wantPin: src.Pin.Commit},
		{name: "not installed", version: "v0.12.5", src: src, wantProblems: []string{"lmux review install"}, wantEditor: domain.EditorIsolated, wantPin: src.Pin.Commit},
		{name: "user editor ignores the plugin", config: "[review]\neditor = \"user\"\n", version: "v0.12.5", src: src, wantReady: true, wantEditor: domain.EditorUser, wantPin: src.Pin.Commit},
		{name: "user editor still needs Neovim", config: "[review]\neditor = \"user\"\n", src: src, wantProblems: []string{"Neovim (nvim) was not found"}, wantEditor: domain.EditorUser, wantPin: src.Pin.Commit},
		{name: "default pin", version: "v0.12.5", wantProblems: []string{"lmux review install"}, wantEditor: domain.EditorIsolated, wantPin: domain.DefaultPin().Commit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHost(t, tc.version)
			if tc.config != "" {
				h.config(t, tc.config)
			}
			if tc.install {
				h.install(t, src, reviewFakeLibrary)
			}
			state, err := ReviewStatus(t.Context(), h.Host, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if state.Ready() != tc.wantReady || state.Editor != tc.wantEditor || state.Pin.Commit != tc.wantPin {
				t.Fatalf("ready %v editor %q pin %q (problems %q)", state.Ready(), state.Editor, state.Pin.Commit, state.Problems())
			}
			problems := state.Problems()
			if len(problems) < len(tc.wantProblems) {
				t.Fatalf("problems %q, want %q", problems, tc.wantProblems)
			}
			for i, want := range tc.wantProblems {
				if !strings.Contains(problems[i], want) {
					t.Fatalf("problems %q, want %q", problems, tc.wantProblems)
				}
			}
			if tc.wantReady && len(problems) != 0 {
				t.Fatalf("ready with problems %q", problems)
			}
		})
	}
	t.Run("invalid configuration", func(t *testing.T) {
		h := newReviewHost(t, "v0.12.5")
		h.config(t, "[review]\nlayout = \"diagonal\"\n")
		if _, err := ReviewStatus(t.Context(), h.Host, src); err == nil || !strings.Contains(err.Error(), "review.layout") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestReviewInstallErrors(t *testing.T) {
	cases := []struct {
		name     string
		src      ReviewPlugin
		lookPath func(string) (string, error)
		wantErr  error
	}{
		{name: "git missing", src: reviewTestSource(), lookPath: func(string) (string, error) { return "", exec.ErrNotFound }, wantErr: review.ErrGitMissing},
		{name: "unsupported platform before any download", src: ReviewPlugin{GOOS: "plan9", GOARCH: "386"}, wantErr: domain.ErrUnsupportedPlatform},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHost(t, "v0.12.5")
			if tc.lookPath != nil {
				h.LookPath = tc.lookPath
			}
			if _, err := ReviewInstall(t.Context(), h.Host, tc.src, false); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if _, err := os.Stat(review.PluginDir(h.paths)); err == nil {
				t.Fatal("a failed install left a plugin directory")
			}
		})
	}
}

func TestReviewUninstall(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(t *testing.T, h *reviewHost)
		wantRemoved bool
		wantErr     error
	}{
		{name: "nothing installed"},
		{name: "installed", setup: func(t *testing.T, h *reviewHost) { h.install(t, reviewTestSource(), reviewFakeLibrary) }, wantRemoved: true},
		{
			name: "symlinked review directory is refused",
			setup: func(t *testing.T, h *reviewHost) {
				if err := os.MkdirAll(h.paths.Data, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), h.paths.ReviewDir()); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrSymlink,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHost(t, "")
			if tc.setup != nil {
				tc.setup(t, h)
			}
			removed, err := ReviewUninstall(h.Host)
			if !errors.Is(err, tc.wantErr) || removed != tc.wantRemoved {
				t.Fatalf("removed %v err %v, want %v %v", removed, err, tc.wantRemoved, tc.wantErr)
			}
			if tc.wantRemoved {
				if _, err := os.Lstat(h.paths.ReviewDir()); err == nil {
					t.Fatal("review directory still exists")
				}
			}
		})
	}
}

func TestReviewOwnsPane(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	info, err := srv.Client.Display(ctx, "=base:", "#{pane_id} #{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	pane, pidText, _ := strings.Cut(info, " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	tmuxEnv := tmuxtest.SocketPath(srv.Name) + ",1,0"
	cases := []struct {
		name string
		tmux string
		pane string
		pid  int
		want bool
	}{
		{name: "the pane's own program", tmux: tmuxEnv, pane: pane, pid: pid, want: true},
		{name: "another process in the pane", tmux: tmuxEnv, pane: pane, pid: pid + 100000},
		{name: "no pane", tmux: tmuxEnv, pid: pid},
		{name: "not inside tmux", pane: pane, pid: pid},
		{name: "unknown pane", tmux: tmuxEnv, pane: "%9999", pid: pid},
		{name: "server gone", tmux: filepath.Join(t.TempDir(), "no-server") + ",1,0", pane: pane, pid: pid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"TMUX": tc.tmux, "TMUX_PANE": tc.pane}
			h := Host{Getenv: func(k string) string { return env[k] }, TmuxBin: srv.Bin}
			if got := ReviewOwnsPane(context.Background(), h, tc.pid); got != tc.want {
				t.Fatalf("ReviewOwnsPane = %v, want %v", got, tc.want)
			}
		})
	}
}
