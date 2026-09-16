package review

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

func requireNvim(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("starts Neovim; skipped with -short")
	}
	bin, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("Neovim (nvim) is not installed")
	}
	return bin
}

// nvimSandbox is a fake home for Neovim: a user configuration, user data
// and system-wide directories that each carry a plugin leaving a marker file
// when loaded, so a test sees exactly what an editor loaded.
type nvimSandbox struct {
	home, markers string
	cfg, data     string
	state, cache  string
	sysConf       string
	sysData       string
	env           []string
}

func marker(name string) string {
	return `local f = io.open(vim.env.LYNA_TMUX_TEST_MARKERS .. "/` + name + `", "w") if f then f:close() end` + "\n"
}

func newNvimSandbox(t *testing.T) *nvimSandbox {
	t.Helper()
	s := &nvimSandbox{home: t.TempDir(), markers: t.TempDir()}
	s.cfg = filepath.Join(s.home, ".config")
	s.data = filepath.Join(s.home, ".local", "share")
	s.state = filepath.Join(s.home, ".local", "state")
	s.cache = filepath.Join(s.home, ".cache")
	sys := t.TempDir()
	s.sysConf = filepath.Join(sys, "etc-xdg")
	s.sysData = filepath.Join(sys, "share")
	writeTree(t, filepath.Join(s.cfg, "nvim"), map[string]string{
		// The user's own setup loads their own plugin copy when a test says so.
		"init.lua":        marker("user-init") + "if vim.env.LYNA_TMUX_TEST_USER_PLUGIN then vim.opt.runtimepath:prepend(vim.env.LYNA_TMUX_TEST_USER_PLUGIN) end\n",
		"plugin/user.lua": marker("user-plugin"),
	})
	writeTree(t, filepath.Join(s.data, "nvim", "site"), map[string]string{
		"plugin/site.lua":             marker("user-site"),
		"pack/p/start/u/plugin/u.lua": marker("user-pack"),
	})
	writeTree(t, filepath.Join(s.sysConf, "nvim"), map[string]string{"plugin/sys.lua": marker("system-config")})
	writeTree(t, filepath.Join(s.sysData, "nvim", "site"), map[string]string{
		"plugin/sys.lua":              marker("system-site"),
		"pack/p/start/s/plugin/s.lua": marker("system-pack"),
	})
	s.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + s.home,
		"XDG_CONFIG_HOME=" + s.cfg,
		"XDG_DATA_HOME=" + s.data,
		"XDG_STATE_HOME=" + s.state,
		"XDG_CACHE_HOME=" + s.cache,
		"XDG_CONFIG_DIRS=" + s.sysConf,
		"XDG_DATA_DIRS=" + s.sysData,
		"LYNA_TMUX_TEST_MARKERS=" + s.markers,
	}
	return s
}

func (s *nvimSandbox) loadedMarkers(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(s.markers)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// forbiddenRoots are directories no isolated editor may use: the sandbox's
// user and system directories and the real user's Neovim directories.
func (s *nvimSandbox) forbiddenRoots(t *testing.T) []string {
	t.Helper()
	roots := []string{s.home, s.sysConf, s.sysData}
	if developer, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			filepath.Join(developer, ".config", "nvim"),
			filepath.Join(developer, ".local", "share", "nvim"),
			filepath.Join(developer, ".local", "state", "nvim"))
	}
	return roots
}

// reviewPaths installs the fake plugin under a lyna-tmux home whose path
// needs Lua escaping and holds characters Neovim can load plugins from.
func reviewPaths(t *testing.T, withPlugin bool) xdg.Paths {
	t.Helper()
	root := filepath.Join(t.TempDir(), `lyna "home" (é) #1`)
	paths, err := xdg.Resolve(func(k string) string {
		if k == "LYNA_TMUX_HOME" {
			return root
		}
		return ""
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, PluginDir(paths))
	if withPlugin {
		copyDir(t, filepath.Join("testdata", "fakeplugin"), PluginDir(paths))
	}
	return paths
}

type editorState struct {
	Setup struct {
		Highlights map[string]string `json:"highlights"`
		Diff       map[string]string `json:"diff"`
		Explorer   struct {
			AutoRefresh   bool              `json:"auto_refresh"`
			IndentMarkers *bool             `json:"indent_markers"`
			Ellipsis      string            `json:"ellipsis"`
			Icons         map[string]string `json:"icons"`
		} `json:"explorer"`
	} `json:"setup"`
	RuntimePath      []string `json:"runtimepath"`
	PackPath         []string `json:"packpath"`
	TermGUIColors    bool     `json:"termguicolors"`
	Background       string   `json:"background"`
	Modeline         bool     `json:"modeline"`
	Exrc             bool     `json:"exrc"`
	Swapfile         bool     `json:"swapfile"`
	Shadafile        string   `json:"shadafile"`
	NoAutoInstall    *string  `json:"no_auto_install"`
	NoWatcherInstall *string  `json:"no_watcher_install"`
	WatcherPath      *string  `json:"watcher_path"`
	Cwd              string   `json:"cwd"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, data)
	}
}

func TestNeovimLaunch(t *testing.T) {
	requireNvim(t)
	lyna, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		req        domain.Request
		mode       domain.EditorMode
		init       domain.InitOptions
		env        []string
		userPlugin bool
		// elsewhere starts Neovim outside the review directory, as a command
		// that replaces its own process does from another working directory.
		elsewhere bool
		check     func(t *testing.T, s *nvimSandbox, paths xdg.Paths, dir string, st editorState)
	}{
		{
			name: "isolated changes with paths that need quoting",
			req: domain.Request{Layout: domain.LayoutInline, Paths: []string{
				"dir with spaces/a b.go", "x|echo 1", `q"uote`, `back\slash`, "!touch pwned", "%self", "it's", "日本.go",
			}},
			init: domain.InitOptions{Colors: domain.ColorsFromPalette(lyna), Icons: domain.IconsASCII, TrueColor: true, Layout: domain.LayoutSideBySide},
			check: func(t *testing.T, s *nvimSandbox, paths xdg.Paths, dir string, st editorState) {
				if len(st.RuntimePath) == 0 || st.RuntimePath[0] != PluginDir(paths) {
					t.Errorf("runtimepath starts with %q, want the plugin directory", st.RuntimePath)
				}
				for _, entry := range slices.Concat(st.RuntimePath, st.PackPath) {
					for _, root := range s.forbiddenRoots(t) {
						if strings.HasPrefix(entry, root) {
							t.Errorf("isolated editor path %q is under %q", entry, root)
						}
					}
				}
				if got := s.loadedMarkers(t); len(got) != 0 {
					t.Errorf("isolated editor loaded %v", got)
				}
				for _, dir := range []string{filepath.Join(s.state, "nvim"), filepath.Join(s.cache, "nvim")} {
					if _, err := os.Stat(dir); err == nil {
						t.Errorf("isolated editor created %s", dir)
					}
				}
				if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
					t.Error("a path argument ran a command")
				}
				if st.Setup.Highlights["line_insert"] != "#163b24" || st.Setup.Highlights["conflict_sign_rejected"] != "#ff5f56" {
					t.Errorf("highlights = %v", st.Setup.Highlights)
				}
				if st.Setup.Diff["layout"] != "side-by-side" || st.Setup.Diff["filler_text"] != "/" {
					t.Errorf("diff options = %v", st.Setup.Diff)
				}
				ex := st.Setup.Explorer
				if !ex.AutoRefresh || ex.IndentMarkers == nil || *ex.IndentMarkers || ex.Ellipsis != "..." || ex.Icons["folder_closed"] != "+" || ex.Icons["folder_open"] != "-" {
					t.Errorf("explorer options = %+v", ex)
				}
				if !st.TermGUIColors || st.Background != "dark" || st.Modeline || st.Exrc || st.Swapfile || st.Shadafile != "NONE" {
					t.Errorf("editor options = %+v", st)
				}
				// Neovim reads an empty variable as unset, which is what the
				// plugin checks before running a watcher from that path.
				if st.NoAutoInstall == nil || *st.NoAutoInstall != "1" || st.NoWatcherInstall == nil || *st.NoWatcherInstall != "1" || st.WatcherPath != nil {
					t.Errorf("auto install guards = %v %v %v", st.NoAutoInstall, st.NoWatcherInstall, st.WatcherPath)
				}
			},
		},
		{
			name: "isolated unicode light 256 colors",
			req:  domain.Request{Mode: domain.ModeRevision, Revisions: []string{"main...HEAD"}},
			init: domain.InitOptions{Icons: domain.IconsUnicode, Light: true},
			env:  []string{"CODEDIFF_WATCHER_PATH=/usr/local/bin/codediff-watcher"},
			check: func(t *testing.T, _ *nvimSandbox, _ xdg.Paths, _ string, st editorState) {
				if st.Setup.Diff["filler_text"] != "╱" || st.Setup.Explorer.Icons["folder_open"] != "▾" || st.Setup.Highlights != nil {
					t.Errorf("setup = %+v", st.Setup)
				}
				if st.TermGUIColors || st.Background != "light" {
					t.Errorf("termguicolors = %v, background = %q", st.TermGUIColors, st.Background)
				}
				if st.WatcherPath != nil {
					t.Errorf("inherited watcher path survived: %v", st.WatcherPath)
				}
			},
		},
		{name: "started in another directory opens in the review directory", req: domain.Request{Mode: domain.ModeStaged}, elsewhere: true},
		{name: "user mode started elsewhere", req: domain.Request{}, mode: domain.EditorUser, userPlugin: true, elsewhere: true},
		{name: "isolated pull request", req: domain.Request{Mode: domain.ModePR, PR: domain.PR{Number: 42, Remote: "upstream", Base: "main"}, Paths: []string{"src"}}},
		{name: "isolated history of one file", req: domain.Request{Mode: domain.ModeHistory, History: domain.History{Range: "HEAD~3..HEAD", Reverse: true}, Paths: []string{"my file.go"}}},
		{
			name:       "user mode runs the user's own setup",
			req:        domain.Request{Mode: domain.ModeStaged},
			mode:       domain.EditorUser,
			userPlugin: true,
			check: func(t *testing.T, s *nvimSandbox, _ xdg.Paths, _ string, st editorState) {
				if got := s.loadedMarkers(t); !slices.Contains(got, "user-init") {
					t.Errorf("user configuration was not loaded: %v", got)
				}
				if st.NoAutoInstall != nil {
					t.Errorf("user mode set %s", domain.EnvNoAutoInstall)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newNvimSandbox(t)
			paths := reviewPaths(t, !tc.userPlugin)
			if _, err := Prepare(paths, tc.init); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			out := t.TempDir()
			env := append(slices.Clone(s.env),
				"LYNA_TMUX_TEST_FARGS="+filepath.Join(out, "fargs.json"),
				"LYNA_TMUX_TEST_STATE="+filepath.Join(out, "state.json"))
			if tc.userPlugin {
				plugin := filepath.Join(t.TempDir(), "codediff.nvim")
				copyDir(t, filepath.Join("testdata", "fakeplugin"), plugin)
				env = append(env, "LYNA_TMUX_TEST_USER_PLUGIN="+plugin)
			}
			env = append(env, tc.env...)
			l, err := Command(tc.req, LaunchOptions{Paths: paths, Mode: tc.mode, Dir: dir, Environ: env})
			if err != nil {
				t.Fatal(err)
			}
			if tc.elsewhere {
				l.Dir = t.TempDir()
			}
			var stdout, stderr bytes.Buffer
			if err := l.Run(testContext(t, 30*time.Second), nil, &stdout, &stderr); err != nil {
				t.Fatalf("Run() = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
			var fargs []string
			readJSON(t, filepath.Join(out, "fargs.json"), &fargs)
			want, _ := tc.req.Fargs()
			if !slices.Equal(fargs, want) {
				t.Fatalf(":CodeDiff received %q, want %q", fargs, want)
			}
			var st editorState
			readJSON(t, filepath.Join(out, "state.json"), &st)
			if resolved, _ := filepath.EvalSymlinks(dir); st.Cwd != dir && st.Cwd != resolved {
				t.Errorf("editor ran in %q, want %q", st.Cwd, dir)
			}
			if tc.check != nil {
				tc.check(t, s, paths, dir, st)
			}
		})
	}
}

func TestNeovimLaunchFailures(t *testing.T) {
	requireNvim(t)
	cases := []struct {
		name       string
		withPlugin bool
		env        []string
		wantErr    error
		stderrHas  string
	}{
		{name: "no :CodeDiff command", wantErr: ErrUnavailable, stderrHas: "the :CodeDiff command is not available. Install it with: lyna-tmux review install"},
		{name: "plugin setup fails", withPlugin: true, env: []string{"LYNA_TMUX_TEST_SETUP_FAIL=1"}, wantErr: ErrLaunchFailed, stderrHas: "codediff.nvim setup failed: "},
		{name: ":CodeDiff raises", withPlugin: true, env: []string{"LYNA_TMUX_TEST_CODEDIFF_FAIL=1"}, wantErr: ErrLaunchFailed, stderrHas: "fake CodeDiff failure"},
		{name: "arguments are not an array", withPlugin: true, env: []string{`LYNA_TMUX_REVIEW_ARGS={"a":"b"}`}, wantErr: ErrLaunchFailed, stderrHas: "LYNA_TMUX_REVIEW_ARGS is not a JSON array"},
		{name: "arguments are not JSON", withPlugin: true, env: []string{"LYNA_TMUX_REVIEW_ARGS=--staged"}, wantErr: ErrLaunchFailed, stderrHas: "LYNA_TMUX_REVIEW_ARGS is not a JSON array"},
		{name: "arguments are not strings", withPlugin: true, env: []string{"LYNA_TMUX_REVIEW_ARGS=[1]"}, wantErr: ErrLaunchFailed, stderrHas: "LYNA_TMUX_REVIEW_ARGS must hold strings only"},
		{name: "review directory cannot be entered", withPlugin: true, env: []string{"LYNA_TMUX_REVIEW_DIR=/nonexistent/lyna-tmux-review"}, wantErr: ErrLaunchFailed, stderrHas: "cannot enter /nonexistent/lyna-tmux-review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newNvimSandbox(t)
			paths := reviewPaths(t, tc.withPlugin)
			if _, err := Prepare(paths, domain.InitOptions{}); err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			env := append(slices.Clone(s.env), "LYNA_TMUX_TEST_FARGS="+filepath.Join(out, "fargs.json"))
			l, err := Command(domain.Request{}, LaunchOptions{Paths: paths, Dir: t.TempDir(), Environ: env})
			if err != nil {
				t.Fatal(err)
			}
			// The launcher contract is tested against the environment the
			// editor sees, including values Command itself never produces.
			l.Env = mergeEnv(l.Env, tc.env)
			var stderr bytes.Buffer
			err = l.Run(testContext(t, 30*time.Second), nil, nil, &stderr)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Run() = %v, want %v\nstderr: %s", err, tc.wantErr, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.stderrHas) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), tc.stderrHas)
			}
			if _, err := os.Stat(filepath.Join(out, "fargs.json")); err == nil {
				t.Fatal(":CodeDiff ran although the launch failed")
			}
		})
	}
}

// chromeResult is what the isolated editor reports about the lines it draws.
type chromeResult struct {
	Labels []string `json:"labels"`
	// TabsAfterNew is the number of tab pages left once the review opened its
	// own, which the empty one Neovim starts with must not be part of.
	TabsAfterNew int    `json:"tabs_after_new"`
	Statusline   string `json:"statusline"`
	Modified     string `json:"modified"`
	Tabline      string `json:"tabline"`
	Tabs         int    `json:"tabs"`
}

// TestNeovimReviewChrome checks the status and tab lines of the isolated
// editor against Neovim's own renderer: a diff buffer is a codediff:// URL,
// and the default lines shorten one to a row of single letters.
func TestNeovimReviewChrome(t *testing.T) {
	nvim := requireNvim(t)
	s := newNvimSandbox(t)
	paths := reviewPaths(t, true)
	initFile, err := Prepare(paths, domain.InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Neovim reports the directory it entered, which on macOS is the resolved
	// one, and only a name under that is relative to the working directory.
	cwd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, buffer, want string
	}{
		{"a revision of a file", "codediff:////src/api///8a1b2c3d4e5f6a7b/internal/api/server.go", "internal/api/server.go  @ 8a1b2c3d"},
		{"a symbolic revision", "codediff:////src/api///HEAD/README.md", "README.md  @ HEAD"},
		{"a parent revision", "codediff:////src/api///abc1234^/README.md", "README.md  @ abc1234^"},
		{"the staged index", "codediff:////src/api///:0:/README.md", "README.md  @ index"},
		{"a revision short enough to read", "codediff:////src/api///12345678/README.md", "README.md  @ 12345678"},
		{"a per cent sign in the file name", "codediff:////src/api///8a1b2c3d4e5f6a7b/100%.md", "100%.md  @ 8a1b2c3d"},
		{"the explorer panel", "CodeDiff Explorer [2]", "Explorer"},
		{"the history panel", "CodeDiff History [17]", "History"},
		{"a file in the working directory", filepath.Join(cwd, "internal", "api", "server.go"), filepath.Join("internal", "api", "server.go")},
		{"a file under the home directory", filepath.Join(s.home, "notes.md"), filepath.Join("~", "notes.md")},
		{"no file at all", "", "[no name]"},
	}
	out := filepath.Join(t.TempDir(), "chrome.json")
	var script strings.Builder
	script.WriteString("local result = { labels = {} }\n")
	for i, tc := range cases {
		fmt.Fprintf(&script, "result.labels[%d] = _G.%s(%s)\n", i+1, domain.LabelFunc, domain.LuaString(tc.buffer))
	}
	// The editor starts on an empty buffer and codediff.nvim opens its review
	// in a tab of its own, which is what a new tab page stands for here.
	script.WriteString(`vim.cmd("tabnew")
vim.wait(5000, function() return #vim.api.nvim_list_tabpages() == 1 end)
result.tabs_after_new = #vim.api.nvim_list_tabpages()
local function show(name)
  local buf = vim.api.nvim_create_buf(true, true)
  vim.api.nvim_buf_set_name(buf, name)
  vim.api.nvim_win_set_buf(0, buf)
end
show("codediff:////src/api///8a1b2c3d4e5f6a7b/100%.md")
result.statusline = vim.api.nvim_eval_statusline(vim.o.statusline, { winid = vim.api.nvim_get_current_win(), maxwidth = 80 }).str
vim.cmd("tabnew")
show("CodeDiff Explorer [2]")
result.tabline = vim.api.nvim_eval_statusline(vim.o.tabline, { use_tabline = true, maxwidth = 80 }).str
result.tabs = #vim.api.nvim_list_tabpages()
-- A buffer of the file itself, the one side of a review that can be edited.
local edited = vim.api.nvim_create_buf(true, false)
vim.api.nvim_buf_set_name(edited, "notes.md")
vim.api.nvim_win_set_buf(0, edited)
vim.api.nvim_buf_set_lines(0, 0, -1, false, { "edited" })
result.modified = vim.api.nvim_eval_statusline(vim.o.statusline, { winid = vim.api.nvim_get_current_win(), maxwidth = 80 }).str
local f = assert(io.open(` + domain.LuaString(out) + `, "w"))
f:write(vim.json.encode(result))
f:close()
`)
	path := filepath.Join(t.TempDir(), "chrome.lua")
	if err := os.WriteFile(path, []byte(script.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(testContext(t, 30*time.Second), nvim,
		"--clean", "-u", initFile, "-i", "NONE", "-n", "--headless", "-c", "source "+path, "-c", "qa!")
	cmd.Dir = dir
	cmd.Env = s.env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nvim: %v\n%s", err, output)
	}
	var got chromeResult
	readJSON(t, out, &got)
	if len(got.Labels) != len(cases) {
		t.Fatalf("%d labels, want %d", len(got.Labels), len(cases))
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got.Labels[i] != tc.want {
				t.Errorf("label of %q = %q, want %q", tc.buffer, got.Labels[i], tc.want)
			}
		})
	}
	t.Run("the empty first tab is closed", func(t *testing.T) {
		if got.TabsAfterNew != 1 {
			t.Errorf("%d tab pages after the review opened one, want 1", got.TabsAfterNew)
		}
	})
	t.Run("the status line names the file", func(t *testing.T) {
		// The status line takes the name as text, so a per cent sign in it
		// stays one and is never read back as a status line item.
		if !strings.HasPrefix(got.Statusline, " 100%.md  @ 8a1b2c3d ") || strings.Contains(got.Statusline, "codediff") {
			t.Errorf("status line = %q", got.Statusline)
		}
		// Every diff buffer is read only, so only unsaved work is marked.
		if strings.Contains(got.Statusline, "[") {
			t.Errorf("status line of an unchanged buffer = %q", got.Statusline)
		}
		if !strings.HasPrefix(got.Modified, " notes.md [+] ") {
			t.Errorf("status line of a changed buffer = %q", got.Modified)
		}
	})
	t.Run("the tab line names every tab", func(t *testing.T) {
		if got.Tabs != 2 {
			t.Fatalf("%d tab pages, want 2", got.Tabs)
		}
		for _, want := range []string{"100%.md  @ 8a1b2c3d", "Explorer"} {
			if !strings.Contains(got.Tabline, want) {
				t.Errorf("tab line = %q, want it to name %q", got.Tabline, want)
			}
		}
		if strings.Contains(got.Tabline, "codediff") {
			t.Errorf("tab line shows a URL: %q", got.Tabline)
		}
	})
}

// TestLuaStringInNeovim evaluates LuaString literals with Neovim's real Lua
// and compares the resulting bytes.
func TestLuaStringInNeovim(t *testing.T) {
	nvim := requireNvim(t)
	cases := []string{"", "plain", `"\`, "\x00123", "a\nb\r\t", "\xff\xfe\xc3", "╱ ▸ ▾ 日本", "\u0085\u2028", "]]==]", "it's \"q\" \\ \\\\"}
	for b := range 256 {
		cases = append(cases, string([]byte{byte(b)}), "x"+string([]byte{byte(b)})+"9")
	}
	s := newNvimSandbox(t)
	out := filepath.Join(t.TempDir(), "out.txt")
	var script strings.Builder
	script.WriteString("local f = assert(io.open(" + domain.LuaString(out) + ", \"wb\"))\n")
	script.WriteString("local function emit(s) f:write((s:gsub(\".\", function(c) return string.format(\"%02x\", c:byte()) end)), \"\\n\") end\n")
	for _, c := range cases {
		script.WriteString("emit(" + domain.LuaString(c) + ")\n")
	}
	script.WriteString("f:close()\n")
	path := filepath.Join(t.TempDir(), "literals.lua")
	if err := os.WriteFile(path, []byte(script.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(testContext(t, 30*time.Second), nvim, "--clean", "--headless", "-l", path)
	cmd.Env = s.env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nvim -l: %v\n%s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("Neovim evaluated %d literals, want %d", len(lines), len(cases))
	}
	for i, c := range cases {
		if lines[i] != hex.EncodeToString([]byte(c)) {
			t.Errorf("LuaString(%q) evaluated to %s", c, lines[i])
		}
	}
}
