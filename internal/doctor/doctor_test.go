package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// linuxWithProblems is an Ubuntu 24.04 host inside tmux with most things
// missing or misconfigured.
func linuxWithProblems() fakeSystem {
	return fakeSystem{
		goos: "linux",
		env: map[string]string{
			"TERM": "tmux-256color", "TMUX": "/tmp/tmux-1000/default,1,0", "TERM_PROGRAM": "tmux",
			"LANG": "C.UTF-8", "DISPLAY": ":0",
		},
		bins: map[string]string{
			"tmux": "/usr/bin/tmux", "git": "/usr/bin/git", "nvim": "/usr/bin/nvim", "docker": "/usr/bin/docker",
		},
		files: map[string]string{
			osReleasePath:                        osReleaseUbuntu,
			procVersionPath:                      procLinux,
			usernsPath:                           "1\n",
			"/home/u/.claude/" + KeybindingsFile: `{"bindings": [{"context": "Chat", "bindings": {"alt+s": "chat:stash"}}]}`,
		},
		outputs: map[string]fakeOutput{
			"/usr/bin/tmux -V":                                 {out: "tmux 3.2a\n"},
			"/usr/bin/git --version":                           {out: "git version 2.43.0\n"},
			"/usr/bin/nvim --version":                          {out: "NVIM v0.7.2\n"},
			"/usr/bin/docker info --format {{.ServerVersion}}": {err: errors.New("permission denied while trying to connect to the Docker daemon socket")},
		},
	}
}

// macHealthy is a macOS host with everything in place.
func macHealthy() fakeSystem {
	return fakeSystem{
		goos: "darwin",
		env:  map[string]string{"TERM_PROGRAM": "ghostty", "TERM": "xterm-ghostty", "COLORTERM": "truecolor", "LANG": "en_US.UTF-8"},
		bins: map[string]string{
			"tmux": "/opt/homebrew/bin/tmux", "claude": "/Users/u/.local/bin/claude", "git": "/usr/bin/git",
			"sandbox-exec": "/usr/bin/sandbox-exec", "pbcopy": "/usr/bin/pbcopy", "docker": "/opt/homebrew/bin/docker",
			"nvim": "/opt/homebrew/bin/nvim",
		},
		outputs: map[string]fakeOutput{
			"/opt/homebrew/bin/tmux -V":                                 {out: "tmux 3.7c\n"},
			"/Users/u/.local/bin/claude --version":                      {out: "2.1.273 (Claude Code)\n"},
			"/usr/bin/git --version":                                    {out: "git version 2.50.1 (Apple Git-155)\n"},
			"/opt/homebrew/bin/docker info --format {{.ServerVersion}}": {out: "28.4.0\n"},
			"/opt/homebrew/bin/nvim --version":                          {out: "NVIM v0.11.4\nBuild type: Release\n"},
		},
	}
}

func TestRunGolden(t *testing.T) {
	cases := []struct {
		name   string
		sys    fakeSystem
		edit   func(*Deps)
		failed bool
	}{
		{name: "linux-problems", sys: linuxWithProblems(), failed: true},
		{name: "darwin-healthy", sys: macHealthy(), edit: func(d *Deps) {
			d.ClaudeHome = "/Users/u/.claude"
			d.Isolation = "container"
			d.ClaudeMinVersion = "2.1.0"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.sys.deps()
			if tc.edit != nil {
				tc.edit(&d)
			}
			report := NewReport(Run(t.Context(), d))
			if report.Failed() != tc.failed {
				t.Fatalf("Failed() = %v, want %v; summary %+v", report.Failed(), tc.failed, report.Summary)
			}
			var text, js bytes.Buffer
			if err := WriteText(&text, report, 0); err != nil {
				t.Fatal(err)
			}
			if err := WriteJSON(&js, report); err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.name+".txt", text.Bytes())
			golden.Assert(t, tc.name+".json", js.Bytes())

			var decoded Report
			if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
				t.Fatalf("report JSON does not decode: %v", err)
			}
			if decoded.Summary != report.Summary || len(decoded.Results) != len(report.Results) {
				t.Fatalf("JSON round trip changed the report: %+v", decoded.Summary)
			}
		})
	}
}

func TestRunOrderIsStable(t *testing.T) {
	d := macHealthy().deps()
	first := Run(t.Context(), d)
	for range 20 {
		again := Run(t.Context(), d)
		if len(again) != len(first) {
			t.Fatalf("result count changed: %d then %d", len(first), len(again))
		}
		for i := range first {
			if again[i].ID != first[i].ID {
				t.Fatalf("result %d is %s, was %s", i, again[i].ID, first[i].ID)
			}
		}
	}
	var ids []string
	for _, r := range first {
		ids = append(ids, r.ID)
	}
	want := "tmux claude claude-trust git sandbox truecolor clipboard option-meta shift-enter keybindings docker nvim"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("ids = %q, want %q", got, want)
	}
}

func TestRunWithEmptyDeps(t *testing.T) {
	results := Run(t.Context(), Deps{})
	byID := map[string]Result{}
	for _, r := range results {
		byID[r.ID] = r
	}
	cases := []struct {
		id     string
		status Status
	}{
		{"tmux", StatusFail},
		{"claude", StatusFail},
		{"git", StatusFail},
		{"sandbox", StatusFail},
		{"truecolor", StatusWarn},
		{"clipboard", StatusWarn},
		{"option-meta", StatusSkip},
		{"keybindings", StatusSkip},
		{"docker", StatusSkip},
		{"nvim", StatusWarn},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			if got := byID[tc.id].Status; got != tc.status {
				t.Fatalf("status = %q, want %q (%+v)", got, tc.status, byID[tc.id])
			}
		})
	}
}

func TestWithDefaults(t *testing.T) {
	d := Deps{}.withDefaults()
	if d.Prefix != "C-b" || d.Timeout != DefaultTimeout {
		t.Fatalf("defaults: prefix %q timeout %v", d.Prefix, d.Timeout)
	}
	if _, err := d.LookPath("tmux"); err == nil {
		t.Fatal("inert LookPath must fail")
	}
	if _, err := d.ReadFile("/etc/os-release", 1); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("inert ReadFile error = %v", err)
	}
	if _, err := d.Run(t.Context(), []string{"true"}); err == nil {
		t.Fatal("inert Run must fail")
	}
	if d.Exists("/") || d.Getenv("HOME") != "" {
		t.Fatal("inert Exists and Getenv must report nothing")
	}
	kept := Deps{Prefix: "C-a", Timeout: time.Minute}.withDefaults()
	if kept.Prefix != "C-a" || kept.Timeout != time.Minute {
		t.Fatalf("explicit values replaced: %+v", kept)
	}
}

func TestOutputAppliesTimeout(t *testing.T) {
	var deadline time.Time
	d := Deps{
		Timeout: 250 * time.Millisecond,
		Run: func(ctx context.Context, _ []string) (string, error) {
			deadline, _ = ctx.Deadline()
			return "", nil
		},
	}
	start := time.Now()
	if _, err := d.output(t.Context(), "x"); err != nil {
		t.Fatal(err)
	}
	if deadline.IsZero() || deadline.Sub(start) > 250*time.Millisecond+50*time.Millisecond {
		t.Fatalf("deadline %v not bounded by the 250ms timeout from %v", deadline, start)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell scripts")
	}
	cases := []struct {
		name    string
		body    string
		timeout time.Duration
		wantOut string
		wantErr string
	}{
		{name: "stdout only", body: "echo 'tmux 3.7c'\necho ignored >&2\n", wantOut: "tmux 3.7c\n"},
		{name: "failure carries stderr", body: "echo partial\necho 'no daemon' >&2\nexit 3\n", wantOut: "partial\n", wantErr: "exit status 3: no daemon"},
		{name: "failure without stderr", body: "exit 4\n", wantErr: "exit status 4"},
		{name: "timeout", body: "exec sleep 30\n", timeout: 100 * time.Millisecond, wantErr: "signal: killed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			out, err := runCommand(ctx, []string{writeScript(t, tc.body)})
			if out != tc.wantOut {
				t.Fatalf("out = %q, want %q", out, tc.wantOut)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunCommandEdges(t *testing.T) {
	if _, err := runCommand(t.Context(), nil); err == nil {
		t.Fatal("empty argv must fail")
	}
	if _, err := runCommand(t.Context(), []string{filepath.Join(t.TempDir(), "missing")}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing binary error = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	script := writeScript(t, "i=0\nwhile [ $i -lt 2000 ]; do printf '%0100d\\n' 0; i=$((i+1)); done\n")
	out, err := runCommand(t.Context(), []string{script})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != maxOutputBytes {
		t.Fatalf("output length %d, want the %d byte cap", len(out), maxOutputBytes)
	}
}

func TestSystem(t *testing.T) {
	d := System()
	if d.GOOS != runtime.GOOS || d.Prefix != "C-b" || d.Timeout != DefaultTimeout {
		t.Fatalf("System() = %+v", d)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !d.Exists(file) || d.Exists(filepath.Join(dir, "missing")) {
		t.Fatal("Exists is wrong")
	}
	if got, err := d.ReadFile(file, 5); err != nil || string(got) != "12345" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if _, err := d.ReadFile(file, 4); !errors.Is(err, fsx.ErrTooLarge) {
		t.Fatalf("ReadFile past the cap = %v, want ErrTooLarge", err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	if got, err := d.LookPath("sh"); err != nil || got != sh {
		t.Fatalf("LookPath(sh) = %q, %v", got, err)
	}
	if d.Getenv("PATH") != os.Getenv("PATH") {
		t.Fatal("Getenv does not read the process environment")
	}
	if out, err := d.Run(t.Context(), []string{sh, "-c", "echo ok"}); err != nil || out != "ok\n" {
		t.Fatalf("Run = %q, %v", out, err)
	}
}
