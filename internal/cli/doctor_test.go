package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
)

// doctorUseSource replaces the doctor command of a tree with one whose review
// plugin fix installs from src.
func doctorUseSource(root *cobra.Command, d Deps, src app.ReviewPlugin) {
	for _, c := range root.Commands() {
		if c.Name() == "doctor" {
			root.RemoveCommand(c)
		}
	}
	root.AddCommand(doctorCommandWith(d, src))
}

// doctorWriteConfig writes the configuration file of a test environment.
func doctorWriteConfig(t *testing.T, e *infraEnv, content string) {
	t.Helper()
	dir := filepath.Join(e.host.Home, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

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

// TestDoctorFixCLI covers the walk over the fixes doctor can apply. The
// machine is ready except for a Claude Code older than the minimum, whose fix
// is a command; the terminal setting and the review plugin of the same report
// are the rows that stay with the user or need a plugin source.
func TestDoctorFixCLI(t *testing.T) {
	update := []string{"/usr/bin/claude", "update"}
	cases := []struct {
		name     string
		term     bool
		stdin    string
		args     []string
		wantCode int
		wantRuns [][]string
		outHas   []string
		errHas   []string
	}{
		{
			name: "yes applies every fix without asking", args: []string{"doctor", "--fix", "--yes"},
			wantRuns: [][]string{update},
			outHas:   []string{"Update Claude Code", "/usr/bin/claude update", "1 fix applied"},
		},
		{
			name: "an accepted fix runs", term: true, stdin: "y\n", args: []string{"doctor", "--fix"},
			wantRuns: [][]string{update},
			outHas:   []string{"apply? [y]es", "1 fix applied"},
		},
		{
			name: "a declined fix leaves the check failing", term: true, stdin: "n\n", args: []string{"doctor", "--fix"},
			wantCode: 1, errHas: []string{"left a failing check unfixed"},
		},
		{
			name: "quit stops the walk", term: true, stdin: "q\n", args: []string{"doctor", "--fix"},
			wantCode: 1, outHas: []string{"Stopped; nothing else was applied."},
		},
		{
			name: "dry run applies nothing", term: true, args: []string{"doctor", "--fix", "--dry-run"},
			wantCode: 1, outHas: []string{"Update Claude Code"},
		},
		{
			name: "without a terminal the flag is named", args: []string{"doctor", "--fix"},
			wantCode: 1, errHas: []string{"not a terminal to answer on", "--yes"},
		},
		{
			name: "the fixes left to the user are listed", args: []string{"doctor", "--fix", "--yes"},
			wantRuns: [][]string{update},
			// The terminal setting is nobody's to apply but the user's.
			outHas: []string{"Left for you to apply:", "Option as Meta", "Use Option as Meta Key"},
		},
		{
			name: "json and fix are refused together", args: []string{"doctor", "--fix", "--json"},
			wantCode: 1, errHas: []string{"run --fix without it"},
		},
		{
			name: "yes needs fix", args: []string{"doctor", "--yes"},
			wantCode: 1, errHas: []string{"--yes and --dry-run apply to --fix"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := doctorReadyEnv(t)
			// A Claude Code older than the minimum is a failing check whose
			// fix is a command, which is the one doctor can run itself.
			e.outs["/usr/bin/claude --version"] = "2.0.1 (Claude Code)\n"
			// Reviews open in the user's own Neovim here, so the report has no
			// plugin to install and the command fix is the only one to walk.
			doctorWriteConfig(t, e, "[review]\neditor = \"user\"\n")
			e.setenv("TERM_PROGRAM", "Apple_Terminal")
			e.term = Terminal{Interactive: tc.term, Width: 100}
			e.stdin = strings.NewReader(tc.stdin)
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if len(e.runs) != len(tc.wantRuns) {
				t.Fatalf("ran %q, want %q", e.runs, tc.wantRuns)
			}
			for i, want := range tc.wantRuns {
				if !slices.Equal(e.runs[i], want) {
					t.Fatalf("run %d is %q, want %q", i, e.runs[i], want)
				}
			}
		})
	}
}

// TestDoctorFixInstallsTheReviewPlugin covers the fix lyna-tmux carries out
// itself rather than by running a command.
func TestDoctorFixInstallsTheReviewPlugin(t *testing.T) {
	f := newReviewFixture(t)
	saved := doctorSystem
	// The machine checks are not what this test is about; the commands they
	// run are the fixture's own programs, so the review row reads the fake
	// Neovim and the tool rows report whatever this machine has.
	doctorSystem = func() doctor.Deps {
		return doctor.Deps{GOOS: "darwin", Run: func(ctx context.Context, argv []string) (string, error) {
			out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
			return string(out), err
		}}
	}
	t.Cleanup(func() { doctorSystem = saved })

	code, stdout, stderr := f.run(t, "doctor", "--fix", "--yes")
	if !strings.Contains(stdout, "Installed codediff.nvim 4.0.6") {
		t.Fatalf("exit %d, the review plugin was not installed:\n%s\n%s", code, stdout, stderr)
	}
	// The row the fix was offered for reports ok on the next run.
	_, after, _ := f.run(t, "doctor")
	if !strings.Contains(after, "codediff.nvim 4.0.6") || strings.Contains(after, "codediff.nvim is not installed") {
		t.Fatalf("the review row did not turn ok:\n%s", after)
	}
}
