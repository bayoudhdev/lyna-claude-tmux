package fakeclaude

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBinary builds the real executable once and drives every mode through
// exec, the way lyna-tmux and tmux start it.
func TestBinary(t *testing.T) {
	bin := Build(t)
	if filepath.Base(bin) != "claude" || !filepath.IsAbs(bin) {
		t.Fatalf("Build = %q, want an absolute path named claude", bin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	command := func(t *testing.T, env []string, args ...string) (*exec.Cmd, string) {
		t.Helper()
		dir := t.TempDir()
		record := filepath.Join(dir, "record.jsonl")
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + dir, EnvRecord + "=" + record}, env...)
		return cmd, record
	}

	t.Run("version", func(t *testing.T) {
		cmd, record := command(t, []string{"LYNA_TMUX_MANAGED=1"}, "--version")
		out, err := cmd.Output()
		if err != nil || string(out) != DefaultVersion+"\n" {
			t.Fatalf("--version = %q, %v", out, err)
		}
		invs := ReadRecords(t, record)
		if len(invs) != 1 || invs[0].Program != bin || invs[0].Env["LYNA_TMUX_MANAGED"] != "1" {
			t.Fatalf("records = %+v", invs)
		}
		if wd, _ := filepath.EvalSymlinks(cmd.Dir); invs[0].Cwd != cmd.Dir && invs[0].Cwd != wd {
			t.Fatalf("cwd = %q, want %q", invs[0].Cwd, cmd.Dir)
		}
	})

	t.Run("agents json", func(t *testing.T) {
		agents := filepath.Join(t.TempDir(), "agents.json")
		if err := os.WriteFile(agents, []byte(`[{"id":"job-1","cwd":"/w","kind":"background","startedAt":5,"state":"working"}]`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd, _ := command(t, []string{EnvAgents + "=" + agents}, "agents", "--json")
		out, err := cmd.Output()
		if err != nil || !strings.Contains(string(out), `"job-1"`) {
			t.Fatalf("agents --json = %q, %v", out, err)
		}
	})

	t.Run("hooks write a marker then exit", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "marker")
		settings := hookSettings(t, dir, marker, nil)
		cmd, record := command(t, []string{EnvHooks + "=SessionStart,UserPromptSubmit,Stop", EnvExit + "=1"}, "--settings="+settings, "--name=api")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run: %v\n%s", err, out)
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("marker: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 3 || !strings.HasPrefix(lines[0], `SessionStart {`) || !strings.HasPrefix(lines[2], `Stop {`) {
			t.Fatalf("marker = %q", data)
		}
		invs := ReadRecords(t, record)
		if len(invs) != 1 || invs[0].SettingsPath != settings || len(invs[0].Settings) == 0 {
			t.Fatalf("records = %+v", invs)
		}
		if runs := ReadHookRuns(t, record); len(runs) != 4 {
			t.Fatalf("hook runs = %+v", runs)
		}
	})

	t.Run("interactive session lives until stdin closes", func(t *testing.T) {
		cmd, record := command(t, nil, "--name=api")
		stdin := startSession(t, cmd)
		if _, err := stdin.Write([]byte("hello\n")); err != nil {
			t.Fatalf("session exited before stdin closed: %v", err)
		}
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("exit after stdin EOF: %v", err)
		}
		if len(ReadRecords(t, record)) != 1 {
			t.Fatal("session start not recorded")
		}
	})

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run("interactive session exits on "+sig.String(), func(t *testing.T) {
			cmd, _ := command(t, nil)
			startSession(t, cmd)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("exit after %s: %v", sig, err)
			}
		})
	}

	t.Run("block holds until killed", func(t *testing.T) {
		short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stop()
		cmd := exec.CommandContext(short, bin, "--version")
		cmd.Env = []string{EnvBlock + "=1"}
		out, err := cmd.Output()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || len(out) != 0 {
			t.Fatalf("blocked --version = %q, %v; want killed with no output", out, err)
		}
	})
}

// startSession starts an interactive fake and returns once it printed
// ReadyLine, so signals and stdin writes after it reach a waiting session.
func startSession(t *testing.T, cmd *exec.Cmd) io.WriteCloser {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != ReadyLine+"\n" {
		t.Fatalf("first stdout line = %q, %v; want the ready line", line, err)
	}
	return stdin
}
