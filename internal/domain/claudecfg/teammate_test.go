package claudecfg_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
)

func TestSettingsTeammateMode(t *testing.T) {
	cases := []struct {
		name, mode, want string
		wantErr          bool
	}{
		{name: "the workspace launcher is the tmux backend", mode: "lmux", want: "tmux"},
		{name: "no mode is the workspace launcher", mode: "", want: "tmux"},
		{name: "the choice left to the agent", mode: "auto", want: "auto"},
		{name: "teammates inside the lead", mode: "in-process", want: "in-process"},
		{name: "teammates as terminal splits", mode: "iterm2", want: "iterm2"},
		{name: "the backend spelled as the product spells it", mode: "tmux", wantErr: true},
		{name: "a mode nobody has", mode: "kitty", wantErr: true},
		{name: "a mode with the right letters in the wrong case", mode: "Lmux", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claudecfg.SettingsTeammateMode(tc.mode)
			if tc.wantErr {
				if !errors.Is(err, claudecfg.ErrTeammateMode) {
					t.Fatalf("SettingsTeammateMode(%q) = %q, %v; want the mode refused", tc.mode, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("SettingsTeammateMode(%q) = %q, %v; want %q", tc.mode, got, err, tc.want)
			}
		})
	}
}

func TestTeammateLauncherRefusals(t *testing.T) {
	cases := []struct {
		name     string
		launcher claudecfg.Launcher
		want     error
	}{
		{name: "a relative binary", launcher: claudecfg.Launcher{Lmux: "bin/lmux", Claude: "/usr/bin/claude"}, want: claudecfg.ErrLauncherPath},
		{name: "a relative agent", launcher: claudecfg.Launcher{Lmux: "/usr/bin/lmux", Claude: "claude"}, want: claudecfg.ErrLauncherPath},
		{name: "no binary at all", launcher: claudecfg.Launcher{Claude: "/usr/bin/claude"}, want: claudecfg.ErrLauncherPath},
		{name: "a path holding a newline", launcher: claudecfg.Launcher{Lmux: "/usr/bin/lmux\nrm -rf /", Claude: "/usr/bin/claude"}, want: claudecfg.ErrLauncherPath},
		{name: "a path holding a null byte", launcher: claudecfg.Launcher{Lmux: "/usr/bin/lmux", Claude: "/usr/bin/claude\x00"}, want: claudecfg.ErrLauncherPath},
		{name: "an assignment that assigns nothing", launcher: launcherWithEnv("LYNA_TMUX_THEME"), want: claudecfg.ErrLauncherEnv},
		{name: "a name no shell exports", launcher: launcherWithEnv("2THEME=dark"), want: claudecfg.ErrLauncherEnv},
		{name: "a name holding a command", launcher: launcherWithEnv("THEME;rm -rf /=dark"), want: claudecfg.ErrLauncherEnv},
		{name: "a value holding a line of its own", launcher: launcherWithEnv("THEME=dark\nrm -rf /"), want: claudecfg.ErrLauncherEnv},
		{name: "no name at all", launcher: launcherWithEnv("=dark"), want: claudecfg.ErrLauncherEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claudecfg.TeammateLauncher(tc.launcher)
			if !errors.Is(err, tc.want) {
				t.Fatalf("TeammateLauncher(%+v) = %q, %v; want %v", tc.launcher, got, err, tc.want)
			}
		})
	}
}

// launcherWithEnv is a launcher of two usable paths carrying one assignment,
// so a refusal in a case above can only come from that assignment.
func launcherWithEnv(assignment string) claudecfg.Launcher {
	return claudecfg.Launcher{Lmux: "/usr/bin/lmux", Claude: "/usr/bin/claude", Env: []string{assignment}}
}

func TestLauncherFileName(t *testing.T) {
	a, err := claudecfg.TeammateLauncher(claudecfg.Launcher{Lmux: "/usr/bin/lmux", Claude: "/usr/bin/claude"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := claudecfg.TeammateLauncher(claudecfg.Launcher{Lmux: "/usr/bin/lmux", Claude: "/opt/claude/claude"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		script string
		same   string
	}{
		{name: "the same pair of binaries names the same file", script: a, same: a},
		{name: "another agent names another file", script: a, same: b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, other := claudecfg.LauncherFileName(tc.script), claudecfg.LauncherFileName(tc.same)
			if !claudecfg.IsLauncherFileName(got) {
				t.Fatalf("%q is not a launcher file name", got)
			}
			if (got == other) != (tc.script == tc.same) {
				t.Fatalf("%q and %q", got, other)
			}
		})
	}
	if claudecfg.IsLauncherFileName("teammate.sh") || claudecfg.IsLauncherFileName("0123456789abcdef.json") {
		t.Fatal("a name that is not content addressed is accepted")
	}
}

// TestTeammateLauncherRuns runs the rendered script with /bin/sh, which is what
// Claude Code runs it with, and checks both paths: the workspace binary takes
// the teammate with its own arguments, and an unusable workspace binary still
// starts the agent with exactly the arguments it was given.
func TestTeammateLauncherRuns(t *testing.T) {
	// A directory whose name would break an unquoted command.
	dir := filepath.Join(t.TempDir(), "my tools $HOME 'x'")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Each recorder writes what it was called as, its arguments, and the one
	// value the launcher exports, so both paths are read the same way.
	record := func(name, label string) string {
		path := filepath.Join(dir, name)
		out := filepath.Join(dir, label+".args")
		script := "#!/bin/sh\nprintf '%s\\n' " + label + " \"$@\" \"$LYNA_TMUX_THEME\" > " + shquote(out) + "\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	lmux := record("lmux", "lmux")
	claude := record("claude", "claude")
	env := []string{"LYNA_TMUX_THEME=night owl", "LYNA_TMUX_ICONS=nerd"}

	cases := []struct {
		name       string
		executable bool
		want       []string
	}{
		{
			name: "the workspace binary takes the teammate", executable: true,
			want: []string{"lmux", "teammate", "--claude", claude, "--", "--agent-id", "review-api@session-1", "--agent-name", "review-api", "night owl"},
		},
		{
			name: "a workspace binary that cannot run leaves the team alone",
			want: []string{"claude", "--agent-id", "review-api@session-1", "--agent-name", "review-api", "night owl"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode := os.FileMode(0o600)
			if tc.executable {
				mode = 0o700
			}
			if err := os.Chmod(lmux, mode); err != nil {
				t.Fatal(err)
			}
			script, err := claudecfg.TeammateLauncher(claudecfg.Launcher{Lmux: lmux, Claude: claude, Env: env})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, claudecfg.LauncherFileName(script))
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", path, "--agent-id", "review-api@session-1", "--agent-name", "review-api")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("run launcher: %v %s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(dir, tc.want[0]+".args"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("arguments %q, want %q", got, tc.want)
			}
			if err := os.Remove(filepath.Join(dir, tc.want[0]+".args")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// shquote is the test's own quoting, kept separate from the one under test so
// a bug in that one cannot hide itself here.
func shquote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
