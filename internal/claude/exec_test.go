package claude

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
)

func TestOSExecutor(t *testing.T) {
	bin := fakeclaude.Build(t)
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, bytes.Repeat([]byte(" "), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	notExecutable := filepath.Join(dir, "plain")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		cmd        Cmd
		timeout    time.Duration
		wantStdout string
		wantExit   int
		wantErr    error
		errText    string
	}{
		{
			name:       "stdout captured",
			cmd:        Cmd{Bin: bin, Args: []string{"--version"}, StdoutLimit: 1024},
			wantStdout: fakeclaude.DefaultVersion + "\n",
		},
		{
			name:       "env added to inherited environment",
			cmd:        Cmd{Bin: bin, Args: []string{"--version"}, Env: []string{fakeclaude.EnvVersion + "=9.9.9"}, StdoutLimit: 1024},
			wantStdout: "9.9.9\n",
		},
		{
			name:     "non-zero exit is a result",
			cmd:      Cmd{Bin: bin, Args: []string{"agents"}, StdoutLimit: 1024},
			wantExit: 2,
		},
		{
			name:    "output over the limit kills the command",
			cmd:     Cmd{Bin: bin, Args: []string{"agents", "--json"}, Env: []string{fakeclaude.EnvAgents + "=" + big}, StdoutLimit: 4096},
			wantErr: ErrOutputTooLarge,
		},
		{
			name:    "timeout",
			cmd:     Cmd{Bin: bin, Args: []string{"--version"}, Env: []string{fakeclaude.EnvBlock + "=1"}, StdoutLimit: 1024},
			timeout: 100 * time.Millisecond,
			wantErr: context.DeadlineExceeded,
		},
		{
			name:    "missing executable",
			cmd:     Cmd{Bin: filepath.Join(dir, "missing"), Args: []string{"--version"}, StdoutLimit: 1024},
			wantErr: ErrNotFound,
		},
		{
			name:    "not executable",
			cmd:     Cmd{Bin: notExecutable, StdoutLimit: 1024},
			errText: "permission denied",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			res, err := OSExecutor{}.Exec(ctx, tc.cmd)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Exec error = %v, want %v", err, tc.wantErr)
				}
			case tc.errText != "":
				if err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.errText)) {
					t.Fatalf("Exec error = %v, want %q", err, tc.errText)
				}
			default:
				if err != nil || string(res.Stdout) != tc.wantStdout || res.ExitCode != tc.wantExit {
					t.Fatalf("Exec = stdout %q exit %d err %v", res.Stdout, res.ExitCode, err)
				}
				if tc.wantExit != 0 && len(res.Stderr) == 0 {
					t.Fatal("stderr of a failing command was not captured")
				}
			}
		})
	}
}

func TestCappedBuffer(t *testing.T) {
	cases := []struct {
		name         string
		limit        int64
		truncate     bool
		writes       []string
		want         string
		wantOverflow bool
	}{
		{name: "under limit", limit: 10, writes: []string{"abc", "def"}, want: "abcdef"},
		{name: "exactly at limit", limit: 6, writes: []string{"abc", "def"}, want: "abcdef"},
		{name: "overflow keeps head and flags", limit: 4, writes: []string{"abc", "def", "ghi"}, want: "abcd", wantOverflow: true},
		{name: "truncate never flags", limit: 4, truncate: true, writes: []string{"abcdef"}, want: "abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := &cappedBuffer{limit: tc.limit, truncate: tc.truncate, onOverflow: func() { calls++ }}
			for _, w := range tc.writes {
				if n, err := b.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if string(b.buf) != tc.want || b.overflow != tc.wantOverflow {
				t.Fatalf("buffer %q overflow %v", b.buf, b.overflow)
			}
			if wantCalls := map[bool]int{true: 1, false: 0}[tc.wantOverflow]; calls != wantCalls {
				t.Fatalf("onOverflow called %d times, want %d", calls, wantCalls)
			}
		})
	}
}
