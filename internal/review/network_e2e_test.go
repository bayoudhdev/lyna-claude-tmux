package review

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// e2eHook runs before the launcher in the end-to-end test. It records
// warnings and errors the plugin reports, waits for the plugin to announce an
// open view, checks the native diff library through the plugin's own module,
// writes what it saw to LYNA_TMUX_TEST_E2E_OUT and quits. A view that never
// opens is recorded as such after a bounded wait.
const e2eHook = `local result = { problems = {} }
local notify = vim.notify
vim.notify = function(msg, level, opts)
  if level and level >= vim.log.levels.WARN then
    table.insert(result.problems, tostring(msg))
  end
  return notify(msg, level, opts)
end
local done = false
local function finish(opened, data)
  if done then
    return
  end
  done = true
  result.opened = opened
  result.mode = data and data.mode or vim.NIL
  local ok, diff = pcall(require, "codediff.core.diff")
  result.native = ok
  if ok then
    result.native_version = diff.get_version()
    result.changes = #diff.compute_diff({ "a", "b" }, { "a", "c" }).changes
  else
    result.native_error = tostring(diff)
  end
  result.tabpages = #vim.api.nvim_list_tabpages()
  result.buffers = {}
  for _, buf in ipairs(vim.api.nvim_list_bufs()) do
    table.insert(result.buffers, vim.api.nvim_buf_get_name(buf))
  end
  local f = assert(io.open(vim.env.LYNA_TMUX_TEST_E2E_OUT, "wb"))
  f:write(vim.json.encode(result))
  f:close()
  vim.cmd("qall!")
end
vim.api.nvim_create_autocmd("User", {
  pattern = "CodeDiffOpen",
  once = true,
  callback = function(ev)
    vim.schedule(function()
      finish(true, ev.data)
    end)
  end,
})
vim.defer_fn(function()
  finish(false)
end, 30000)
`

type e2eResult struct {
	Problems      []string `json:"problems"`
	Opened        bool     `json:"opened"`
	Mode          any      `json:"mode"`
	Native        bool     `json:"native"`
	NativeVersion string   `json:"native_version"`
	NativeError   string   `json:"native_error"`
	Changes       int      `json:"changes"`
	Tabpages      int      `json:"tabpages"`
	Buffers       []string `json:"buffers"`
}

// TestNetworkInstallAndOpen installs the real pinned plugin from GitHub and
// opens a review with it. It needs the network, so it only runs with
// LYNA_TMUX_E2E_NETWORK=1.
func TestNetworkInstallAndOpen(t *testing.T) {
	if os.Getenv("LYNA_TMUX_E2E_NETWORK") != "1" {
		t.Skip("downloads codediff.nvim from GitHub; set LYNA_TMUX_E2E_NETWORK=1 to run")
	}
	requireNvim(t)
	requireGit(t)
	pin := domain.DefaultPin()
	paths := testPaths(t)
	gitEnv := gitTestEnv(t)

	res, err := Install(testContext(t, 10*time.Minute), InstallOptions{ReviewDir: paths.ReviewDir(), Environ: gitEnv})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if res.Reused || res.Commit != pin.Commit || res.Version != pin.Version {
		t.Fatalf("Install() = %+v", res)
	}
	lyna, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(paths, domain.InitOptions{Colors: domain.ColorsFromPalette(lyna), TrueColor: true}); err != nil {
		t.Fatal(err)
	}
	// Even "nvim --version" runs with a sandbox home.
	statusOpts := StatusOptions{Paths: paths, Run: func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = newNvimSandbox(t).env
		return cmd.Output()
	}}
	report := Status(testContext(t, 30*time.Second), statusOpts)
	if !report.PluginOK() || !report.Ready() || !report.InitFile || len(report.Problems()) != 0 || len(report.Warnings()) != 0 {
		t.Fatalf("Status() not green: problems %q, warnings %q, report %+v", report.Problems(), report.Warnings(), report)
	}
	t.Logf("installed %s at %s, Neovim %s", report.Version, report.Commit, report.NvimVersion)
	pluginFiles := dirNames(t, PluginDir(paths))

	cases := []struct {
		name string
		req  domain.Request
		// buffer names a diff buffer of the expected layout.
		buffer string
	}{
		{name: "working tree changes", req: domain.Request{}, buffer: "CodeDiff 2.2"},
		{name: "one file against HEAD side by side", req: domain.Request{Mode: domain.ModeRevision, Revisions: []string{"HEAD"}, Paths: []string{"src/app.txt"}, Layout: domain.LayoutSideBySide}, buffer: "CodeDiff 2.2"},
		{name: "staged changes inline", req: domain.Request{Mode: domain.ModeStaged, Layout: domain.LayoutInline}, buffer: "CodeDiff 2.inline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			runGit(t, gitEnv, repo, "init", "-q", "--template=")
			writeTree(t, repo, map[string]string{"src/app.txt": "one\ntwo\nthree\n", "README": "readme\n"})
			runGit(t, gitEnv, repo, "add", "-A")
			runGit(t, gitEnv, repo, "commit", "-q", "-m", "base")
			writeTree(t, repo, map[string]string{"src/app.txt": "one\n2\nthree\nfour\n", "README": "read me\n"})
			runGit(t, gitEnv, repo, "add", "README")

			s := newNvimSandbox(t)
			out := filepath.Join(t.TempDir(), "result.json")
			hook := filepath.Join(t.TempDir(), "hook.lua")
			if err := os.WriteFile(hook, []byte(e2eHook), 0o600); err != nil {
				t.Fatal(err)
			}
			env := append(slices.Clone(s.env), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
				"LYNA_TMUX_TEST_E2E_OUT="+out, "LYNA_TMUX_TEST_E2E_HOOK="+hook)
			l, err := Command(tc.req, LaunchOptions{Paths: paths, Dir: repo, Environ: env})
			if err != nil {
				t.Fatal(err)
			}
			n := len(l.Args)
			l.Args = slices.Concat(l.Args[:n-2], []string{"-c", "lua dofile(vim.env.LYNA_TMUX_TEST_E2E_HOOK)"}, l.Args[n-2:])
			var stdout, stderr bytes.Buffer
			if err := l.Run(testContext(t, 90*time.Second), nil, &stdout, &stderr); err != nil {
				t.Fatalf("Run() = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
			var got e2eResult
			readJSON(t, out, &got)
			t.Logf("result %+v", got)
			if !got.Opened || len(got.Problems) != 0 || !got.Native || got.NativeVersion != pin.Version || got.Changes != 1 {
				t.Fatalf("review did not open cleanly: %+v\nstderr: %s", got, stderr.String())
			}
			if !slices.ContainsFunc(got.Buffers, func(name string) bool { return strings.HasSuffix(name, tc.buffer) }) {
				t.Errorf("buffers %q hold no %q view", got.Buffers, tc.buffer)
			}
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				t.Errorf("editor wrote to stderr: %s", msg)
			}
			if after := dirNames(t, PluginDir(paths)); !slices.Equal(after, pluginFiles) {
				t.Errorf("plugin directory changed from %q to %q", pluginFiles, after)
			}
			if got := s.loadedMarkers(t); len(got) != 0 {
				t.Errorf("isolated editor loaded %v", got)
			}
		})
	}
	if after := Status(testContext(t, 30*time.Second), statusOpts); !after.Ready() || len(after.Unverified) != 0 {
		t.Errorf("Status() after the reviews: problems %q, unverified %q", after.Problems(), after.Unverified)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
