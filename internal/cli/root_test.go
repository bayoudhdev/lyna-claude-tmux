package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/version"
)

func runMain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Main(t.Context(), args, Streams{In: strings.NewReader(""), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

func TestMain(t *testing.T) {
	info := version.Get()
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string // exact stdout when set
		outHas     []string
		errHas     []string
		wantNoErr  bool
		checkStdio func(t *testing.T, stdout string)
	}{
		{
			name:      "version text",
			args:      []string{"version"},
			wantOut:   info.String() + "\n",
			wantNoErr: true,
		},
		{
			name:      "version json",
			args:      []string{"version", "--json"},
			wantNoErr: true,
			checkStdio: func(t *testing.T, stdout string) {
				t.Helper()
				var got version.Info
				if err := json.Unmarshal([]byte(stdout), &got); err != nil {
					t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
				}
				if got != info {
					t.Fatalf("json = %+v, want %+v", got, info)
				}
			},
		},
		{
			name:      "version flag",
			args:      []string{"--version"},
			outHas:    []string{info.Version},
			wantNoErr: true,
		},
		{
			name:      "help flag",
			args:      []string{"--help"},
			outHas:    []string{"ready-made Claude Code workspace", "USAGE", "version"},
			wantNoErr: true,
		},
		{
			name:     "unknown command",
			args:     []string{"no-such-command"},
			wantCode: 1,
			errHas:   []string{`Unknown command "no-such-command"`},
		},
		{
			name:     "unknown command after valid flag",
			args:     []string{"--help=false", "atach"},
			wantCode: 1,
			errHas:   []string{`Unknown command "atach"`},
		},
		{
			name:     "version rejects arguments",
			args:     []string{"version", "extra"},
			wantCode: 1,
			errHas:   []string{"extra"},
		},
		{
			name:     "unknown flag",
			args:     []string{"version", "--nope"},
			wantCode: 1,
			errHas:   []string{"--nope"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runMain(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			if tc.wantOut != "" && stdout != tc.wantOut {
				t.Fatalf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !strings.Contains(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if tc.wantNoErr && stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			if tc.checkStdio != nil {
				tc.checkStdio(t, stdout)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: 0},
		{name: "plain error", err: base, want: 1},
		{name: "exit error", err: &exitError{code: 3, err: base}, want: 3},
		{name: "wrapped exit error", err: fmt.Errorf("doctor: %w", &exitError{code: 2, err: base}), want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestRunPropagatesExitError proves the code carried by a command error
// survives fang and cobra, not only the mapping function.
func TestRunPropagatesExitError(t *testing.T) {
	cases := []struct {
		name     string
		runE     func(*cobra.Command, []string) error
		wantCode int
	}{
		{name: "nil", runE: func(*cobra.Command, []string) error { return nil }, wantCode: 0},
		{name: "plain", runE: func(*cobra.Command, []string) error { return errors.New("failed") }, wantCode: 1},
		{name: "carried code", runE: func(*cobra.Command, []string) error {
			return &exitError{code: 4, err: errors.New("checks failed")}
		}, wantCode: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			root := NewRoot(Streams{In: strings.NewReader(""), Out: &out, Err: &errOut})
			root.AddCommand(&cobra.Command{Use: "probe", Args: cobra.NoArgs, RunE: tc.runE})
			if got := run(context.Background(), root, []string{"probe"}); got != tc.wantCode {
				t.Fatalf("run() = %d, want %d (stderr %q)", got, tc.wantCode, errOut.String())
			}
		})
	}
}
