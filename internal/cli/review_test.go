package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// reviewLibrary is the native library the test release server serves.
const reviewLibrary = "fake codediff native library"

// reviewPluginFiles is a codediff.nvim stand-in: :CodeDiff records its
// arguments and working directory into LYNA_TMUX_TEST_RECORD, then quits.
var reviewPluginFiles = map[string]string{
	"VERSION": "4.0.6\n",
	"plugin/codediff.lua": `vim.api.nvim_create_user_command("CodeDiff", function(opts)
  local path = vim.env.LYNA_TMUX_TEST_RECORD
  local f = assert(io.open(path .. ".tmp", "wb"))
  f:write(vim.json.encode({ fargs = opts.fargs, cwd = vim.fn.getcwd() }))
  f:close()
  assert(os.rename(path .. ".tmp", path))
  vim.cmd("qall!")
end, { nargs = "*", bang = true, range = true })
`,
	"lua/codediff/init.lua": "return { setup = function() end }\n",
}

// reviewFixture is a CLI environment with a fake Neovim, a plugin repository
// served over file://, a TLS release server and a git repository to review.
type reviewFixture struct {
	e       *cliEnv
	src     app.ReviewPlugin
	nvim    string
	repo    string
	execErr error
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	e := newCLIEnv(t)
	e.setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	e.setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &reviewFixture{e: e, nvim: filepath.Join(e.host.Home, "bin", "nvim")}
	f.setNvim(t, "NVIM v0.12.5")
	e.host.LookPath = func(name string) (string, error) {
		if name == "nvim" {
			if _, err := os.Stat(f.nvim); err != nil {
				return "", exec.ErrNotFound
			}
			return f.nvim, nil
		}
		return exec.LookPath(name)
	}

	url, commit := reviewPluginRepo(t, e.host.Environ)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dl/lib.dylib" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(reviewLibrary))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte(reviewLibrary))
	f.src = app.ReviewPlugin{
		GOOS: "darwin", GOARCH: "arm64", RepoURL: url, ReleaseURL: srv.URL + "/dl", HTTPClient: srv.Client(),
		Pin: domain.Pin{Repo: "https://example.invalid/codediff.nvim", Commit: commit, Version: "4.0.6", Assets: map[string][]domain.Asset{
			"darwin/arm64": {{Name: "lib.dylib", File: domain.LibraryFile("4.0.6", "darwin"), SHA256: hex.EncodeToString(sum[:])}},
		}},
	}
	f.repo = reviewGitInit(t, e.host.Environ, filepath.Join(e.host.Home, "src", "repo"))
	e.cwd = f.repo
	return f
}

// setNvim makes the fake Neovim print version, or removes it when empty.
func (f *reviewFixture) setNvim(t *testing.T, version string) {
	t.Helper()
	if version == "" {
		if err := os.Remove(f.nvim); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(f.nvim), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.nvim, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// run runs the command tree with the fixture's plugin source.
func (f *reviewFixture) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	e := f.e
	var out, errOut bytes.Buffer
	d := Deps{
		Host: func() (app.Host, error) { return e.host, nil },
		Exec: func(path string, argv, env []string) error {
			e.execs = append(e.execs, append([]string{path}, argv...))
			e.envs = append(e.envs, env)
			return f.execErr
		},
		LookPath: func(name string) (string, error) { return name, nil },
		Getwd:    func() (string, error) { return e.cwd, nil },
		Terminal: func() Terminal { return e.term },
		Now:      time.Now,
	}
	root := NewRootWith(Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, d)
	reviewUseSource(root, d, f.src)
	code = run(t.Context(), root, args)
	return code, out.String(), errOut.String()
}

// reviewUseSource replaces the review commands of a tree with ones bound to src.
func reviewUseSource(root *cobra.Command, d Deps, src app.ReviewPlugin) {
	for _, c := range root.Commands() {
		if c.Name() == "review" || c.Name() == "watch" {
			root.RemoveCommand(c)
		}
	}
	root.AddCommand(reviewCommandsWith(d, src)...)
}

func reviewGit(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=lyna-test", "-c", "user.email=test@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func reviewGitInit(t *testing.T, env []string, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, env, dir, "init", "-q")
	return dir
}

// reviewPluginRepo publishes reviewPluginFiles as one commit in a bare
// repository and returns its file:// URL and the commit.
func reviewPluginRepo(t *testing.T, env []string) (url, commit string) {
	t.Helper()
	root := t.TempDir()
	work := reviewGitInit(t, env, filepath.Join(root, "work"))
	for name, content := range reviewPluginFiles {
		path := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reviewGit(t, env, work, "add", "-A")
	reviewGit(t, env, work, "commit", "-qm", "plugin")
	commit = reviewGit(t, env, work, "rev-parse", "HEAD")
	bare := filepath.Join(root, "plugin.git")
	reviewGit(t, env, root, "clone", "-q", "--bare", work, bare)
	return "file://" + bare, commit
}

func reviewEnvValue(env []string, name string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

func TestReviewCLI(t *testing.T) {
	f := newReviewFixture(t)
	e := f.e
	paths, err := xdg.Resolve(e.host.Getenv, e.host.Home)
	if err != nil {
		t.Fatal(err)
	}
	notRepo := filepath.Join(e.host.Home, "plain")
	if err := os.MkdirAll(notRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	statusJSON := func(check func(t *testing.T, r reviewStatusReport)) func(t *testing.T, stdout string) {
		return func(t *testing.T, stdout string) {
			t.Helper()
			var r reviewStatusReport
			if err := json.Unmarshal([]byte(stdout), &r); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, stdout)
			}
			for field, null := range map[string]bool{`"files": null`: true, `"unverified": null`: true, `"problems": null`: true, `"warnings": null`: true} {
				if null && strings.Contains(stdout, field) {
					t.Fatalf("status JSON has %s:\n%s", field, stdout)
				}
			}
			check(t, r)
		}
	}
	lastExec := func(t *testing.T) ([]string, []string) {
		t.Helper()
		if len(e.execs) == 0 {
			t.Fatal("no process replacement")
		}
		return e.execs[len(e.execs)-1], e.envs[len(e.envs)-1]
	}
	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
		errLacks []string
		check    func(t *testing.T, stdout string)
	}{
		{name: "status before install", args: []string{"review", "status"}, outHas: []string{"Editor     isolated", "Installed  no", "Neovim     " + f.nvim + " v0.12.5", "Ready      no", "Problems\n  codediff.nvim is not installed (run: lyna-tmux review install)"}},
		{name: "status json before install", args: []string{"review", "status", "--json"}, check: statusJSON(func(t *testing.T, r reviewStatusReport) {
			t.Helper()
			if r.Editor != "isolated" || r.Ready || r.Plugin.Installed || r.Plugin.PinnedCommit != f.src.Pin.Commit || r.Plugin.PinnedVersion != "4.0.6" ||
				r.Platform != "darwin/arm64" || !r.Neovim.Found || r.Neovim.Version != "v0.12.5" || len(r.Problems) != 1 || r.Plugin.Dir != review.PluginDir(paths) {
				t.Fatalf("report %+v", r)
			}
		})},
		{name: "review before install names the install command", args: []string{"review"}, wantCode: 3, errHas: []string{"the review editor is not ready", "lyna-tmux review install"}, errLacks: []string{"Press Enter"}},
		{name: "popup keeps the reason on screen", args: []string{"review", "--popup"}, wantCode: 3, errHas: []string{"lyna-tmux review: the review editor is not ready", "Press Enter or q to close"}},
		{
			name: "no terminal: no prompt", setup: func(*testing.T) { e.term.Interactive = false }, args: []string{"review", "--popup"}, wantCode: 3,
			errLacks: []string{"Press Enter"},
		},
		{name: "not a git repository", setup: func(*testing.T) { e.term.Interactive = true }, args: []string{"review", "--dir", notRepo}, wantCode: 1, errHas: []string{"not a git repository: " + notRepo}},
		{name: "missing directory", args: []string{"review", "--dir", "missing"}, wantCode: 1, errHas: []string{"no such file"}},
		{name: "staged and pr", args: []string{"review", "--staged", "--pr", "3"}, wantCode: 1, errHas: []string{"--staged and --pr select different reviews"}},
		{name: "pr and history", args: []string{"review", "--pr", "3", "--history"}, wantCode: 1, errHas: []string{"--pr and --history select different reviews"}},
		{name: "two layouts", args: []string{"review", "--inline", "--side-by-side"}, wantCode: 1, errHas: []string{"--inline and --side-by-side are different layouts"}},
		{name: "remote without pr", args: []string{"review", "--remote", "upstream"}, wantCode: 1, errHas: []string{"pass them with --pr"}},
		{name: "base without pr", args: []string{"review", "--base", "main"}, wantCode: 1, errHas: []string{"pass them with --pr"}},
		{name: "reverse without history", args: []string{"review", "--reverse"}, wantCode: 1, errHas: []string{"pass it with --history"}},
		{name: "pr with revisions", args: []string{"review", "--pr", "3", "main"}, wantCode: 1, errHas: []string{"--pr takes no revisions"}},
		{name: "history with two ranges", args: []string{"review", "--history", "a", "b"}, wantCode: 1, errHas: []string{"--history takes at most one RANGE"}},
		{name: "three revisions", args: []string{"review", "a", "b", "c"}, wantCode: 1, errHas: []string{"pass at most two revisions"}},
		{name: "invalid revision", args: []string{"review", "merge"}, wantCode: 1, errHas: []string{"invalid review request: revision \"merge\" is a :CodeDiff subcommand name"}},
		{name: "invalid pr number", args: []string{"review", "--pr", "0"}, wantCode: 1, errHas: []string{"pull request number 0"}},
		{name: "subcommands take no arguments", args: []string{"review", "status", "x"}, wantCode: 1, errHas: []string{"unknown command"}},
		{name: "install", args: []string{"review", "install"}, outHas: []string{"Installed codediff.nvim 4.0.6 (commit " + f.src.Pin.Commit[:12] + ") in " + review.PluginDir(paths)}},
		{name: "install keeps a verified installation", args: []string{"review", "install"}, outHas: []string{"codediff.nvim 4.0.6 is already installed in", "(reinstall with --force)"}},
		{name: "install --force reinstalls", args: []string{"review", "install", "--force"}, outHas: []string{"Installed codediff.nvim 4.0.6"}},
		{
			name: "install warns about an old Neovim", setup: func(t *testing.T) { f.setNvim(t, "NVIM v0.9.5") }, args: []string{"review", "install"},
			errHas: []string{"Warning: Neovim v0.9.5 works; v0.10.0 or newer is recommended"},
		},
		{name: "status after install", setup: func(t *testing.T) { f.setNvim(t, "NVIM v0.12.5") }, args: []string{"review", "status"}, outHas: []string{"Installed  yes, verified", "File       libvscode_diff_4.0.6.dylib: checksum ok", "Ready      yes"}},
		{name: "status json after install", args: []string{"review", "status", "--json"}, check: statusJSON(func(t *testing.T, r reviewStatusReport) {
			t.Helper()
			if !r.Ready || !r.Plugin.Verified || r.Plugin.Commit != f.src.Pin.Commit || len(r.Plugin.Files) != 1 || !r.Plugin.Files[0].ChecksumOK || len(r.Problems) != 0 || !r.Neovim.Recommended {
				t.Fatalf("report %+v", r)
			}
		})},
		{
			name: "review replaces the process with the isolated editor", args: []string{"review"},
			check: func(t *testing.T, _ string) {
				t.Helper()
				argv, env := lastExec(t)
				want := []string{f.nvim, f.nvim, "--clean", "-u", review.InitFile(paths), "-i", "NONE", "-n", "-c", domain.Launcher}
				if !slices.Equal(argv, want) {
					t.Fatalf("exec %q, want %q", argv, want)
				}
				if reviewEnvValue(env, domain.EnvDir) != f.repo || reviewEnvValue(env, domain.EnvArgs) != `["--exit-on-close"]` {
					t.Fatalf("exec env %q", env)
				}
			},
		},
		{
			name: "launch failure exits 4", setup: func(*testing.T) { f.execErr = errors.New("exec format error") }, args: []string{"review", "--popup"}, wantCode: 4,
			errHas: []string{"the review could not open: start " + f.nvim + ": exec format error", "Press Enter or q to close"},
		},
		{name: "old Neovim exits 3", setup: func(t *testing.T) { f.execErr = nil; f.setNvim(t, "NVIM v0.8.3") }, args: []string{"review"}, wantCode: 3, errHas: []string{"Neovim v0.8.3 is too old"}},
		{name: "missing Neovim exits 3", setup: func(t *testing.T) { f.setNvim(t, "") }, args: []string{"review"}, wantCode: 3, errHas: []string{"Neovim (nvim) was not found on PATH"}},
		{name: "status without Neovim", args: []string{"review", "status"}, outHas: []string{"Neovim     not found", "Ready      no"}},
		{name: "uninstall", setup: func(t *testing.T) { f.setNvim(t, "NVIM v0.12.5") }, args: []string{"review", "uninstall"}, outHas: []string{"Removed the review editor"}},
		{name: "uninstall again", args: []string{"review", "uninstall"}, outHas: []string{"The review editor is not installed"}},
		{name: "status after uninstall", args: []string{"review", "status"}, outHas: []string{"Installed  no"}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := f.run(t, st.args...)
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
			for _, s := range st.errLacks {
				if containsFolded(stderr, s) {
					t.Fatalf("stderr has %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t, stdout)
			}
		})
	}
}

// TestReviewRequestFlags checks what every flag and argument form hands to
// the editor: the :CodeDiff arguments, the directory and the editor mode.
func TestReviewRequestFlags(t *testing.T) {
	f := newReviewFixture(t)
	e := f.e
	if code, _, stderr := f.run(t, "review", "install"); code != 0 {
		t.Fatalf("install: %s", stderr)
	}
	sub := filepath.Join(f.repo, "pkg")
	home := reviewGitInit(t, e.host.Environ, filepath.Join(e.host.Home, "proj"))
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		config   string
		cwd      string
		args     []string
		wantArgs string
		wantDir  string
		user     bool
	}{
		{name: "working tree", args: []string{"review"}, wantArgs: `["--exit-on-close"]`},
		{name: "one revision", args: []string{"review", "main"}, wantArgs: `["--exit-on-close","main"]`},
		{name: "two revisions", args: []string{"review", "main", "feature"}, wantArgs: `["--exit-on-close","main","feature"]`},
		{name: "merge base", args: []string{"review", "main..."}, wantArgs: `["--exit-on-close","main..."]`},
		{name: "paths after --", args: []string{"review", "--", "a b.go", "x|y"}, wantArgs: `["--exit-on-close","--","a b.go","x|y"]`},
		{name: "staged", args: []string{"review", "--staged"}, wantArgs: `["--exit-on-close","--staged"]`},
		{name: "staged against a revision with paths", args: []string{"review", "--staged", "HEAD~1", "--", "src"}, wantArgs: `["--exit-on-close","--staged","HEAD~1","--","src"]`},
		{name: "pull request", args: []string{"review", "--pr", "42", "--remote", "upstream", "--base", "main"}, wantArgs: `["pr","42","--remote=upstream","--base=main","--exit-on-close"]`},
		{name: "history", args: []string{"review", "--history"}, wantArgs: `["history","--exit-on-close"]`},
		{name: "history range of one file reversed", args: []string{"review", "--history", "HEAD~5..", "--reverse", "--", "f.go"}, wantArgs: `["history","HEAD~5..","f.go","--reverse","--exit-on-close"]`},
		{name: "inline", args: []string{"review", "--inline"}, wantArgs: `["--exit-on-close","--inline"]`},
		{name: "side by side", args: []string{"review", "--side-by-side", "main"}, wantArgs: `["--exit-on-close","--side-by-side","main"]`},
		{name: "configured layout", config: "[review]\nlayout = \"inline\"\n", args: []string{"review"}, wantArgs: `["--exit-on-close","--inline"]`},
		{name: "flag overrides the configured layout", config: "[review]\nlayout = \"inline\"\n", args: []string{"review", "--side-by-side"}, wantArgs: `["--exit-on-close","--side-by-side"]`},
		{name: "user Neovim", args: []string{"review", "--user-nvim"}, wantArgs: `["--exit-on-close"]`, user: true},
		{name: "configured user editor", config: "[review]\neditor = \"user\"\n", args: []string{"review"}, wantArgs: `["--exit-on-close"]`, user: true},
		{name: "absolute dir", cwd: e.host.Home, args: []string{"review", "--dir", f.repo}, wantArgs: `["--exit-on-close"]`, wantDir: f.repo},
		{name: "relative dir", args: []string{"review", "--dir", "pkg"}, wantArgs: `["--exit-on-close"]`, wantDir: sub},
		{name: "home dir", cwd: f.repo, args: []string{"review", "--dir", "~/proj"}, wantArgs: `["--exit-on-close"]`, wantDir: home},
		{name: "popup uses the pane directory", cwd: sub, args: []string{"review", "--popup"}, wantArgs: `["--exit-on-close"]`, wantDir: sub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.cwd = f.repo
			if tc.cwd != "" {
				e.cwd = tc.cwd
			}
			configFile := filepath.Join(e.host.Home, "config", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configFile), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configFile, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			before := len(e.execs)
			code, stdout, stderr := f.run(t, tc.args...)
			if code != 0 || len(e.execs) != before+1 {
				t.Fatalf("exit %d, %d execs\nstdout: %s\nstderr: %s", code, len(e.execs)-before, stdout, stderr)
			}
			argv, env := e.execs[before], e.envs[before]
			if got := reviewEnvValue(env, domain.EnvArgs); got != tc.wantArgs {
				t.Errorf("arguments %s, want %s", got, tc.wantArgs)
			}
			wantDir := tc.wantDir
			if wantDir == "" {
				wantDir = f.repo
			}
			if got := reviewEnvValue(env, domain.EnvDir); got != wantDir {
				t.Errorf("directory %q, want %q", got, wantDir)
			}
			if isUser := slices.Equal(argv[1:], []string{f.nvim, "-c", domain.Launcher}); isUser != tc.user {
				t.Errorf("argv %q, user editor %v", argv, tc.user)
			}
		})
	}
}

func TestReviewCompletion(t *testing.T) {
	e := newCLIEnv(t)
	cases := []struct {
		name          string
		args          []string
		wantDirective string
		outHas        []string
	}{
		{name: "subcommands", args: []string{"__complete", "review", ""}, wantDirective: ":4", outHas: []string{"install\t", "status\t", "uninstall\t"}},
		{name: "paths after -- complete files", args: []string{"__complete", "review", "main", "--", ""}, wantDirective: ":0"},
		{name: "status flags", args: []string{"__complete", "review", "status", "--"}, wantDirective: ":4", outHas: []string{"--json\t"}},
		{name: "install flags", args: []string{"__complete", "review", "install", "--"}, wantDirective: ":4", outHas: []string{"--force\t"}},
		{name: "pr number", args: []string{"__complete", "review", "--pr", ""}, wantDirective: ":4"},
		{name: "remote", args: []string{"__complete", "review", "--remote", ""}, wantDirective: ":4"},
		{name: "base", args: []string{"__complete", "review", "--base", ""}, wantDirective: ":4"},
		{name: "dir completes directories", args: []string{"__complete", "review", "--dir", ""}, wantDirective: ":16"},
		{name: "watch session", args: []string{"__complete", "watch", "--session", ""}, wantDirective: ":4"},
		{name: "watch dir", args: []string{"__complete", "watch", "--dir", ""}, wantDirective: ":16"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := e.run(t, tc.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			lines := strings.Split(strings.TrimSpace(stdout), "\n")
			if last := lines[len(lines)-1]; last != tc.wantDirective {
				t.Fatalf("directive %q, want %q\n%s", last, tc.wantDirective, stdout)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
		})
	}
}

// TestReviewDir pins how --dir is resolved, the bare tilde included: a home
// directory it leaves in the path would send the review to <home>/~.
func TestReviewDir(t *testing.T) {
	const home = "/home/dev"
	const cwd = "/src/work"
	cases := []struct {
		name    string
		dir     string
		getwd   func() (string, error)
		want    string
		wantErr string
	}{
		{name: "empty is the working directory", want: cwd},
		{name: "absolute", dir: "/src/api/", want: "/src/api"},
		{name: "home", dir: "~", want: home},
		{name: "under home", dir: "~/src/api", want: filepath.Join(home, "src", "api")},
		{name: "under home with a parent step", dir: "~/src/../api", want: filepath.Join(home, "api")},
		{name: "a name starting with a tilde stays relative", dir: "~notes", want: filepath.Join(cwd, "~notes")},
		{name: "relative", dir: "../repo", want: "/src/repo"},
		{name: "unknown working directory", dir: "repo", getwd: func() (string, error) { return "", errors.New("cwd gone") }, wantErr: "cwd gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getwd := tc.getwd
			if getwd == nil {
				getwd = func() (string, error) { return cwd, nil }
			}
			got, err := reviewDir(Deps{Getwd: getwd}, app.Host{Home: home}, tc.dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("reviewDir(%q) = %q, %v; want %q", tc.dir, got, err, tc.want)
			}
		})
	}
}

func TestReviewWaitKey(t *testing.T) {
	cases := []struct {
		name, in, rest string
	}{
		{name: "enter", in: "\rafter", rest: "after"},
		{name: "newline", in: "\nafter", rest: "after"},
		{name: "q after other keys", in: "abq rest", rest: " rest"},
		{name: "capital q", in: "Qx", rest: "x"},
		{name: "ctrl+c", in: "\x03x", rest: "x"},
		{name: "ctrl+d", in: "\x04x", rest: "x"},
		{name: "end of input", in: "abc"},
		{name: "empty input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.in)
			reviewWaitKey(r)
			rest := make([]byte, r.Len())
			_, _ = r.Read(rest)
			if string(rest) != tc.rest {
				t.Fatalf("unread %q, want %q", rest, tc.rest)
			}
		})
	}
}

func TestReviewExit(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "Neovim missing", err: fmt.Errorf("%w: gone", app.ErrReviewNeovim), wantCode: 3},
		{name: "plugin not ready", err: fmt.Errorf("%w: not installed", app.ErrReviewPlugin), wantCode: 3},
		{name: "launch failure", err: fmt.Errorf("%w: boom", app.ErrReviewLaunch), wantCode: 4},
		{name: "exit code already set", err: &exitError{code: 7, err: app.ErrReviewNeovim}, wantCode: 7},
		{name: "other errors", err: errors.New("bad flag"), wantCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewExit(tc.err)
			if code := exitCode(got); code != tc.wantCode || !errors.Is(got, tc.err) {
				t.Fatalf("exit %d (%v), want %d", code, got, tc.wantCode)
			}
		})
	}
}

// Environment of the helper process that plays the lyna-tmux binary in a
// tmux pane.
const (
	reviewHelperEnv       = "LYNA_TMUX_TEST_REVIEW_HELPER"
	reviewHelperArgsEnv   = "LYNA_TMUX_TEST_REVIEW_ARGS"
	reviewHelperSourceEnv = "LYNA_TMUX_TEST_REVIEW_SOURCE"
	// reviewHelperStatusEnv names the file the pane test reads the exit
	// status from. tmux has a format for it, but that format is empty on some
	// supported versions (3.3a for this binary, 3.4 for Neovim), so the
	// status is recorded by whoever sees the process end: the helper itself
	// when it is the pane's own program, or the shell it runs under
	// otherwise, which is the only one still running once the review has
	// become the editor.
	reviewHelperStatusEnv = "LYNA_TMUX_TEST_REVIEW_STATUS"
)

// reviewHelperWrapper runs the helper as the child of a shell, the way a
// review typed at a prompt runs, and records its exit status the way the
// helper does when it is the pane's own program. The shell takes the status
// file for itself so the helper never writes it as well. Nothing after the
// review depends on tmux: the pane stays on screen through remain-on-exit.
const reviewHelperWrapper = `f=$` + reviewHelperStatusEnv + `
unset ` + reviewHelperStatusEnv + `
"$@"
s=$?
echo "helper exited $s"
printf '%s' "$s" > "$f.tmp" && mv "$f.tmp" "$f"
exit "$s"
`

// writeExitStatus records code for the pane test: the file appears with its
// final content, so a reader never sees a partial write.
func writeExitStatus(path string, code int) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(code)), 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}

// reviewHelperSource is the plugin pin a helper process reviews with: the
// pane tests install before starting it, so it needs no download locations.
type reviewHelperSource struct {
	Pin          domain.Pin
	GOOS, GOARCH string
}

// TestReviewHelperProcess is not a test of its own: the pane tests run the
// test binary with only it selected, and it then behaves as `lyna-tmux` with
// the real process dependencies (terminal, exec) and a test plugin source.
func TestReviewHelperProcess(_ *testing.T) {
	if os.Getenv(reviewHelperEnv) != "1" {
		return
	}
	var args []string
	var src reviewHelperSource
	if err := json.Unmarshal([]byte(os.Getenv(reviewHelperArgsEnv)), &args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	if err := json.Unmarshal([]byte(os.Getenv(reviewHelperSourceEnv)), &src); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	d := ProcessDeps()
	root := NewRootWith(Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, d)
	reviewUseSource(root, d, app.ReviewPlugin{Pin: src.Pin, GOOS: src.GOOS, GOARCH: src.GOARCH})
	code := run(context.Background(), root, args)
	if path := os.Getenv(reviewHelperStatusEnv); path != "" {
		writeExitStatus(path, code)
	}
	os.Exit(code)
}

// TestReviewInTmuxPane runs the real binary's `review` inside panes of an
// isolated tmux server: a failure stays on screen until a single q in a pane
// the review owns or in a popup, a review typed at a shell prompt returns at
// once, and a successful review becomes Neovim in the requested directory.
func TestReviewInTmuxPane(t *testing.T) {
	f := newReviewFixture(t)
	if code, _, stderr := f.run(t, "review", "install"); code != 0 {
		t.Fatalf("install: %s", stderr)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sourceJSON, err := json.Marshal(reviewHelperSource{Pin: f.src.Pin, GOOS: f.src.GOOS, GOARCH: f.src.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	gitPath := mustLookPath(t, "git")
	nvimPath, nvimErr := exec.LookPath("nvim")
	emptyHome := t.TempDir()
	notInstalled := []string{"lyna-tmux review install", "Press Enter or q to close."}

	cases := []struct {
		name string
		args []string
		// shell runs the review from a shell (reviewHelperWrapper), which
		// records and prints its status, instead of as the pane's own
		// program. The pane's own program is what a layout's review pane
		// runs, and the review holds its reason on screen only there: the
		// hold is decided by comparing its pid with the pane's, so the case
		// that asserts the hold cannot run under a wrapper.
		shell     bool
		installed bool
		realNvim  bool
		// waiting is the screen while the review waits for a key; empty when
		// it must not wait.
		waiting []string
		// after is the screen once a shell review returned.
		after      []string
		wantStatus string
		check      func(t *testing.T, record string)
	}{
		{name: "a review pane keeps the reason until q", args: []string{"review", "--dir", f.repo}, waiting: notInstalled, wantStatus: "3"},
		{name: "a popup keeps the reason until q", args: []string{"review", "--popup"}, shell: true, waiting: notInstalled, after: []string{"helper exited 3"}, wantStatus: "3"},
		{
			name: "a review typed at a shell prompt returns at once", args: []string{"review", "--dir", f.repo}, shell: true,
			after: []string{"lyna-tmux review install", "helper exited 3"}, wantStatus: "3",
		},
		{
			// The review replaces its own process with the editor, so no code
			// of ours runs after it: the status is the editor's, seen by the
			// shell the review runs under.
			name: "a review becomes Neovim in the requested directory", args: []string{"review", "--dir", f.repo, "main..."},
			installed: true, realNvim: true, shell: true, wantStatus: "0",
			check: func(t *testing.T, record string) {
				t.Helper()
				var got struct {
					Fargs []string `json:"fargs"`
					Cwd   string   `json:"cwd"`
				}
				data, err := os.ReadFile(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got.Fargs, []string{"--exit-on-close", "main..."}) || got.Cwd != f.repo {
					t.Fatalf(":CodeDiff got %q in %q, want the request in %q", got.Fargs, got.Cwd, f.repo)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.realNvim && nvimErr != nil {
				t.Skip("Neovim (nvim) is not installed")
			}
			srv := tmuxtest.Start(t)
			ctx := tmuxtest.Context(t)
			// A pane that closes with its program takes the screen with it,
			// and the test reads the screen after the review returned. The
			// panes of this test therefore stay until the test ends, the way
			// the panes of a workspace do.
			if _, err := srv.Client.Run(ctx, "set-option", "-wg", "remain-on-exit", "on"); err != nil {
				t.Fatal(err)
			}
			home := emptyHome
			if tc.installed {
				home = f.e.host.Home
			}
			binDir := filepath.Dir(f.nvim)
			if tc.realNvim {
				binDir = filepath.Dir(nvimPath)
			}
			sandbox := t.TempDir()
			record := filepath.Join(t.TempDir(), "record.json")
			status := filepath.Join(t.TempDir(), "status")
			argsJSON, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				reviewHelperEnv: "1", reviewHelperArgsEnv: string(argsJSON), reviewHelperSourceEnv: string(sourceJSON),
				"LYNA_TMUX_HOME": home, "HOME": sandbox, "LANG": "en_US.UTF-8", "LYNA_TMUX_TEST_RECORD": record,
				reviewHelperStatusEnv: status,
				"XDG_CONFIG_HOME":     filepath.Join(sandbox, "config"), "XDG_DATA_HOME": filepath.Join(sandbox, "data"),
				"XDG_STATE_HOME": filepath.Join(sandbox, "state"), "XDG_CACHE_HOME": filepath.Join(sandbox, "cache"),
				"XDG_CONFIG_DIRS": filepath.Join(sandbox, "etc"), "XDG_DATA_DIRS": filepath.Join(sandbox, "share"),
				"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
			}
			args := []string{"-d", "-P", "-F", "#{pane_id}", "-t", tmux.ExactSession("base"), "-c", tmux.FormatEscape(f.repo)}
			for k, v := range env {
				args = append(args, "-e", k+"="+v)
			}
			// The pane takes its PATH from the server rather than from -e on
			// tmux 3.3, so the one the review needs (the Neovim of the
			// fixture, git and the binary under test) is given to the program
			// itself. Every other variable arrives through -e on all versions.
			path := strings.Join([]string{binDir, filepath.Dir(gitPath), filepath.Dir(srv.Bin), "/usr/bin", "/bin"}, ":")
			program := []string{mustLookPath(t, "env"), "PATH=" + path, exe, "-test.run=^TestReviewHelperProcess$"}
			if tc.shell {
				program = append([]string{"/bin/sh", "-c", reviewHelperWrapper, "sh"}, program...)
			}
			out, err := srv.Client.Run(ctx, "new-window", append(args, program...)...)
			if err != nil {
				t.Fatal(err)
			}
			pane := strings.TrimSpace(out)
			// A capture that fails answers with no screen, which reads like a
			// pane that has drawn nothing: the last error is kept so a failure
			// says which of the two happened.
			var captureErr error
			screen := func() string {
				s, err := srv.Client.Run(ctx, "capture-pane", "-p", "-J", "-t", pane)
				captureErr = err
				return s
			}
			shows := func(want []string) func() bool {
				return func() bool {
					s := screen()
					for _, w := range want {
						if !strings.Contains(s, w) {
							return false
						}
					}
					return true
				}
			}
			// exited reports the status once the review, or the editor it
			// became, has ended; the file appears only then.
			exited := func() (string, bool) {
				data, err := os.ReadFile(status)
				return string(data), err == nil
			}
			t.Cleanup(func() {
				if t.Failed() {
					s := screen()
					t.Logf("screen (capture error %v):\n%s", captureErr, s)
				}
			})

			if len(tc.waiting) > 0 {
				tmuxtest.WaitFor(t, "the reason and the prompt", shows(tc.waiting))
				if got, ok := exited(); ok {
					t.Fatalf("the review exited %s before a key was pressed", got)
				}
				if s := screen(); strings.Contains(s, "helper exited") {
					t.Fatalf("the review returned before a key was pressed:\n%s", s)
				}
				// A single key: the prompt reads the terminal in raw mode.
				if _, err := srv.Client.Run(ctx, "send-keys", "-t", pane, "q"); err != nil {
					t.Fatal(err)
				}
			}
			if len(tc.after) > 0 {
				tmuxtest.WaitFor(t, "the shell to report the exit", shows(tc.after))
				if len(tc.waiting) == 0 && strings.Contains(screen(), "Press Enter") {
					t.Fatal("a review started from a shell waited for a key")
				}
			}
			var got string
			tmuxtest.WaitFor(t, "the pane program to exit", func() bool {
				var ok bool
				got, ok = exited()
				return ok
			})
			if got != tc.wantStatus {
				t.Fatalf("exit status %s, want %s", got, tc.wantStatus)
			}
			if tc.check != nil {
				tc.check(t, record)
			}
		})
	}
}

func mustLookPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return path
}
