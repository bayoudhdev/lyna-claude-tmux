package review

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

func TestPrepare(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, p xdg.Paths)
		opts    domain.InitOptions
		wantErr error
		errHas  string
		check   func(t *testing.T, p xdg.Paths, path string)
	}{
		{
			name: "writes a private init file for the installed plugin",
			opts: domain.InitOptions{Icons: domain.IconsASCII, TrueColor: true},
			check: func(t *testing.T, p xdg.Paths, path string) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				want, _ := domain.RenderInit(domain.InitOptions{PluginDir: PluginDir(p), Icons: domain.IconsASCII, TrueColor: true})
				if string(data) != string(want) {
					t.Errorf("init file differs from RenderInit")
				}
				assertMode(t, path, fsx.PrivateFile)
				assertMode(t, p.ReviewDir(), fsx.PrivateDir)
			},
		},
		{
			name: "unchanged content is not rewritten",
			setup: func(t *testing.T, p xdg.Paths) {
				if _, err := Prepare(p, domain.InitOptions{}); err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(InitFile(p), old, old); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, _ xdg.Paths, path string) {
				info, err := os.Stat(path)
				if err != nil || time.Since(info.ModTime()) < 30*time.Minute {
					t.Errorf("init file was rewritten: %v %v", info.ModTime(), err)
				}
			},
		},
		{
			name: "changed content is rewritten",
			setup: func(t *testing.T, p xdg.Paths) {
				if _, err := Prepare(p, domain.InitOptions{Layout: domain.LayoutInline}); err != nil {
					t.Fatal(err)
				}
			},
			opts: domain.InitOptions{Layout: domain.LayoutSideBySide},
			check: func(t *testing.T, _ xdg.Paths, path string) {
				data, _ := os.ReadFile(path)
				if !strings.Contains(string(data), `layout = "side-by-side"`) {
					t.Errorf("init file kept the old layout:\n%s", data)
				}
			},
		},
		{
			name: "symlinked init file is refused",
			setup: func(t *testing.T, p xdg.Paths) {
				mustMkdir(t, p.ReviewDir())
				if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), InitFile(p)); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrSymlink,
		},
		{
			name:   "invalid colors",
			opts:   domain.InitOptions{Colors: domain.Colors{LineInsert: "green"}},
			errHas: "line_insert",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := testPaths(t)
			if tc.setup != nil {
				tc.setup(t, paths)
			}
			path, err := Prepare(paths, tc.opts)
			switch {
			case tc.wantErr == nil && tc.errHas == "" && err != nil:
				t.Fatalf("Prepare() error = %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("Prepare() error = %v, want %v", err, tc.wantErr)
			case tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)):
				t.Fatalf("Prepare() error = %v, want %q", err, tc.errHas)
			}
			if err == nil && path != InitFile(paths) {
				t.Errorf("Prepare() = %q, want %q", path, InitFile(paths))
			}
			if tc.check != nil {
				tc.check(t, paths, path)
			}
		})
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != want {
		t.Errorf("%s mode = %v, %v; want %v", path, info.Mode().Perm(), err, want)
	}
}

// launchPaths returns paths with a fake installed plugin and an init file.
func launchPaths(t *testing.T) xdg.Paths {
	t.Helper()
	paths := testPaths(t)
	mustMkdir(t, PluginDir(paths))
	if _, err := Prepare(paths, domain.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestCommand(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		req      domain.Request
		mode     domain.EditorMode
		setup    func(t *testing.T) xdg.Paths
		dir      string
		lookPath func(string) (string, error)
		environ  []string
		wantErr  error
		errHas   string
		check    func(t *testing.T, p xdg.Paths, l Launch)
	}{
		{
			name:    "isolated merges the environment",
			req:     domain.Request{Paths: []string{"a b"}},
			environ: []string{"HOME=/h", "CODEDIFF_WATCHER_PATH=/tmp/evil", "LYNA_TMUX_REVIEW_ARGS=stale", "VSCODE_DIFF_NO_AUTO_INSTALL="},
			check: func(t *testing.T, p xdg.Paths, l Launch) {
				plan, _ := domain.NewPlan(domain.Request{Paths: []string{"a b"}}, domain.EditorIsolated, domain.PlanOptions{Nvim: "/opt/bin/nvim", InitFile: InitFile(p), LogFile: LogFile(p), Dir: dir})
				if l.Path != "/opt/bin/nvim" || !slices.Equal(l.Args, plan.Argv) || !slices.Equal(l.Set, plan.Env) || l.Dir != dir {
					t.Errorf("launch = %+v, want argv %q set %q", l, plan.Argv, plan.Env)
				}
				if want := append([]string{"HOME=/h"}, plan.Env...); !slices.Equal(l.Env, want) {
					t.Errorf("Env = %q, want %q", l.Env, want)
				}
				cmd := l.Cmd(t.Context())
				if cmd.Path != l.Path || !slices.Equal(cmd.Args, l.Args) || cmd.Dir != dir || !slices.Equal(cmd.Env, l.Env) {
					t.Errorf("Cmd() = %v %q in %s", cmd.Path, cmd.Args, cmd.Dir)
				}
			},
		},
		{
			name:  "user mode needs no installation",
			req:   domain.Request{Mode: domain.ModeStaged},
			mode:  domain.EditorUser,
			setup: testPaths,
			check: func(t *testing.T, _ xdg.Paths, l Launch) {
				if !slices.Equal(l.Args, []string{"/opt/bin/nvim", "-c", domain.Launcher}) || !slices.Equal(l.Set[:1], []string{domain.EnvDir + "=" + dir}) || len(l.Set) != 2 {
					t.Errorf("launch = %+v", l)
				}
			},
		},
		{
			name:     "relative nvim from PATH is made absolute",
			mode:     domain.EditorUser,
			setup:    testPaths,
			lookPath: func(string) (string, error) { return "bin/nvim", nil },
			check: func(t *testing.T, _ xdg.Paths, l Launch) {
				if !filepath.IsAbs(l.Path) || l.Path != l.Args[0] {
					t.Errorf("Path = %q, Args[0] = %q", l.Path, l.Args[0])
				}
			},
		},
		{name: "invalid request", req: domain.Request{Revisions: []string{"x"}}, wantErr: domain.ErrInvalidRequest},
		{name: "relative directory", dir: "repo", errHas: "is not absolute"},
		{name: "missing directory", dir: filepath.Join(dir, "missing"), errHas: "no such file"},
		{name: "directory is a file", dir: "/dev/null", wantErr: fsx.ErrNotDir},
		{name: "plugin not installed", setup: testPaths, wantErr: ErrNotInstalled},
		{
			name: "init file not written",
			setup: func(t *testing.T) xdg.Paths {
				p := testPaths(t)
				mustMkdir(t, PluginDir(p))
				return p
			},
			wantErr: ErrNotPrepared,
		},
		{name: "nvim missing", lookPath: func(string) (string, error) { return "", exec.ErrNotFound }, wantErr: ErrNvimMissing},
		{name: "unknown mode", mode: "system", wantErr: domain.ErrInvalidPlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := launchPaths(t)
			if tc.setup != nil {
				paths = tc.setup(t)
			}
			opts := LaunchOptions{Paths: paths, Mode: tc.mode, Dir: dir, LookPath: func(string) (string, error) { return "/opt/bin/nvim", nil }, Environ: tc.environ}
			if tc.dir != "" {
				opts.Dir = tc.dir
			}
			if tc.lookPath != nil {
				opts.LookPath = tc.lookPath
			}
			l, err := Command(tc.req, opts)
			switch {
			case tc.wantErr == nil && tc.errHas == "" && err != nil:
				t.Fatalf("Command() error = %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("Command() error = %v, want %v", err, tc.wantErr)
			case tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)):
				t.Fatalf("Command() error = %v, want %q", err, tc.errHas)
			}
			if tc.check != nil {
				tc.check(t, paths, l)
			}
		})
	}
}

func TestCommandDefaults(t *testing.T) {
	// Default mode is isolated, default environment is the process's own and
	// the default lookup searches PATH for Neovim.
	paths := launchPaths(t)
	t.Setenv("LYNA_TMUX_TEST_MARKER", "inherited")
	l, err := Command(domain.Request{}, LaunchOptions{Paths: paths, Dir: t.TempDir(), Nvim: "lyna-tmux-test-no-such-nvim"})
	if !errors.Is(err, ErrNvimMissing) {
		t.Fatalf("Command() = %+v, %v; want ErrNvimMissing", l, err)
	}
	if _, err := exec.LookPath("nvim"); err != nil {
		return
	}
	l, err = Command(domain.Request{}, LaunchOptions{Paths: paths, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if l.Args[1] != "--clean" || !slices.Contains(l.Env, "LYNA_TMUX_TEST_MARKER=inherited") {
		t.Fatalf("launch = %+v", l)
	}
}

func TestMergeEnv(t *testing.T) {
	cases := []struct {
		name      string
		base, set []string
		want      []string
	}{
		{name: "empty", want: []string{}},
		{name: "adds", base: []string{"A=1"}, set: []string{"B=2"}, want: []string{"A=1", "B=2"}},
		{name: "replaces every duplicate", base: []string{"B=0", "A=1", "B=9"}, set: []string{"B=2"}, want: []string{"A=1", "B=2"}},
		{name: "empty value replaces", base: []string{"W=/x"}, set: []string{"W="}, want: []string{"W="}},
		{name: "similar names kept", base: []string{"BB=1", "B_=2"}, set: []string{"B=3"}, want: []string{"BB=1", "B_=2", "B=3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeEnv(tc.base, tc.set); !slices.Equal(got, tc.want) {
				t.Fatalf("mergeEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExitError(t *testing.T) {
	exitWith := func(code int) error {
		return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	}
	cases := []struct {
		name    string
		err     error
		wantErr error
		errHas  string
	}{
		{name: "success", err: nil},
		{name: "unavailable", err: exitWith(domain.ExitUnavailable), wantErr: ErrUnavailable},
		{name: "failed", err: exitWith(domain.ExitFailed), wantErr: ErrLaunchFailed},
		{name: "other exit", err: exitWith(1), errHas: "Neovim exited: exit status 1"},
		{name: "start failure passes through", err: exec.ErrNotFound, wantErr: exec.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exitError(tc.err)
			switch {
			case tc.wantErr == nil && tc.errHas == "" && got != nil:
				t.Fatalf("exitError() = %v, want nil", got)
			case tc.wantErr != nil && !errors.Is(got, tc.wantErr):
				t.Fatalf("exitError() = %v, want %v", got, tc.wantErr)
			case tc.errHas != "" && (got == nil || !strings.Contains(got.Error(), tc.errHas)):
				t.Fatalf("exitError() = %v, want %q", got, tc.errHas)
			}
		})
	}
}

// shellWordCases are values tmux commands carry: JSON with quotes and
// backslashes, spaces, pipes, expansions and fish escapes.
var shellWordCases = []string{
	"plain",
	"",
	"a b",
	"x|echo 1",
	"it's",
	`LYNA_TMUX_REVIEW_ARGS=["--exit-on-close","--","q\"uote","back\\slash"]`,
	`\`,
	`\\`,
	`\'`,
	`'\''`,
	`a\`,
	"$HOME `id` $(id) ~ * ? [a] {a,b} ; & > < #",
	"%self",
	`%self\%self`,
	"résumé 日本",
}

// TestShellWordShells runs every value quoted by tmux.ShellQuote through the
// real shells tmux may hand commands to and checks that each yields exactly
// its value.
func TestShellWordShells(t *testing.T) {
	shells := []struct {
		name string
		argv func(script string) []string
	}{
		{name: "sh", argv: func(s string) []string { return []string{"sh", "-c", s} }},
		{name: "bash", argv: func(s string) []string { return []string{"bash", "--norc", "--noprofile", "-c", s} }},
		{name: "zsh", argv: func(s string) []string { return []string{"zsh", "-f", "-c", s} }},
		{name: "fish", argv: func(s string) []string { return []string{"fish", "--no-config", "-c", s} }},
	}
	words := make([]string, len(shellWordCases))
	for i, v := range shellWordCases {
		words[i] = tmux.ShellQuote(v)
	}
	script := "printf '%s\\000' " + strings.Join(words, " ")
	for _, sh := range shells {
		t.Run(sh.name, func(t *testing.T) {
			argv := sh.argv(script)
			if _, err := exec.LookPath(argv[0]); err != nil {
				t.Skipf("%s is not installed", argv[0])
			}
			cmd := exec.CommandContext(testContext(t, 10*time.Second), argv[0], argv[1:]...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: %v", sh.name, err)
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if !slices.Equal(got, shellWordCases) {
				t.Fatalf("%s read %q, want %q", sh.name, got, shellWordCases)
			}
		})
	}
}
