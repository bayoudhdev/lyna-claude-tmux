package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
)

// doctorReadyEnv is a machine where every check passes.
func doctorReadyEnv(t *testing.T) *infraEnv {
	t.Helper()
	e := newInfraEnv(t)
	for _, name := range []string{"claude", "git", "nvim", "sandbox-exec", "pbcopy"} {
		e.bins[name] = "/usr/bin/" + name
	}
	e.outs = map[string]string{
		e.bins["tmux"] + " -V":      "tmux 3.7c\n",
		"/usr/bin/claude --version": "2.4.0 (Claude Code)\n",
		"/usr/bin/git --version":    "git version 2.48.0\n",
		"/usr/bin/nvim --version":   "NVIM v0.11.2\n",
	}
	e.setenv("TERM", "xterm-256color")
	e.setenv("COLORTERM", "truecolor")
	return e
}

func TestDoctorCLI(t *testing.T) {
	t.Run("a ready machine", func(t *testing.T) {
		e := doctorReadyEnv(t)
		code, stdout, stderr := e.run(t, "doctor")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
		}
		for _, want := range []string{"ok", "tmux 3.7c", "Claude Code 2.4.0", "Configuration", "Review editor", "0 fail"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("output lacks %q:\n%s", want, stdout)
			}
		}
		if !strings.Contains(stdout, "codediff.nvim is not installed") || !strings.Contains(stdout, "fix:  lyna-tmux review install") {
			t.Fatalf("review row missing its fix:\n%s", stdout)
		}
	})
	t.Run("a failing check exits 1 and names the fix", func(t *testing.T) {
		e := doctorReadyEnv(t)
		delete(e.bins, "claude")
		code, stdout, stderr := e.run(t, "doctor")
		if code != 1 || !strings.Contains(stdout, "fail") || !strings.Contains(stdout, "claude is not on PATH") {
			t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
		}
		if !containsFolded(stderr, "doctor found a failing check") {
			t.Fatalf("stderr %q", stderr)
		}
	})
	t.Run("json", func(t *testing.T) {
		e := doctorReadyEnv(t)
		code, stdout, _ := e.run(t, "doctor", "--json")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stdout)
		}
		var r doctor.Report
		if err := json.Unmarshal([]byte(stdout), &r); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, stdout)
		}
		if len(r.Results) < 8 || r.Results[0].ID != "config" || r.Results[len(r.Results)-1].ID != "review" {
			t.Fatalf("results %+v", r.Results)
		}
		if r.Summary.Fail != 0 || r.Summary.OK+r.Summary.Warn+r.Summary.Skip != len(r.Results)-r.Summary.Fail {
			t.Fatalf("summary %+v", r.Summary)
		}
	})
	t.Run("an invalid configuration is a failing row", func(t *testing.T) {
		e := doctorReadyEnv(t)
		dir := filepath.Join(e.host.Home, "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[sandbox]\nisolation = \"process\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ := e.run(t, "doctor")
		if code != 1 || !strings.Contains(stdout, "sandbox-runtime") && !strings.Contains(stdout, "Sandbox runtime") {
			t.Fatalf("exit %d\n%s", code, stdout)
		}
		if !strings.Contains(stdout, "npm install -g @anthropic-ai/sandbox-runtime") {
			t.Fatalf("the configured isolation did not reach the checks:\n%s", stdout)
		}
	})
	t.Run("rejects arguments", func(t *testing.T) {
		e := newInfraEnv(t)
		if code, _, stderr := e.run(t, "doctor", "extra"); code != 1 || !containsFolded(stderr, "unknown command") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	})
}
