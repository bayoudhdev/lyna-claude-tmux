package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
)

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{Bin: "claude"}); err == nil {
		t.Fatal("New accepted a relative executable")
	}
	c, err := New(Options{Bin: "/usr/bin/claude"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bin() != "/usr/bin/claude" || c.timeout != DefaultTimeout {
		t.Fatalf("defaults: bin %q timeout %s", c.Bin(), c.timeout)
	}
	if _, ok := c.exec.(OSExecutor); !ok {
		t.Fatalf("default executor is %T", c.exec)
	}
}

func TestClientWithInjectedExecutor(t *testing.T) {
	type call struct {
		cmd      Cmd
		deadline time.Duration
	}
	cases := []struct {
		name     string
		result   Result
		execErr  error
		run      func(c *Client) (any, error)
		want     any
		wantArgs []string
		wantCap  int64
		wantErr  string
		errIs    error
	}{
		{
			name:     "version",
			result:   Result{Stdout: []byte("2.1.272 (Claude Code)\n")},
			run:      func(c *Client) (any, error) { return c.Version(context.Background()) },
			want:     Version{2, 1, 272, ""},
			wantArgs: []string{"--version"}, wantCap: VersionOutputLimit,
		},
		{
			name:     "version garbage",
			result:   Result{Stdout: []byte("hello\n")},
			run:      func(c *Client) (any, error) { return c.Version(context.Background()) },
			wantArgs: []string{"--version"}, wantCap: VersionOutputLimit,
			errIs: ErrVersionFormat,
		},
		{
			name:     "agents",
			result:   Result{Stdout: []byte(" [{\"pid\":7,\"cwd\":\"/w\",\"kind\":\"interactive\",\"startedAt\":1,\"status\":\"busy\"},{\"cwd\":\"/x\"}]\n")},
			run:      func(c *Client) (any, error) { return c.Agents(context.Background()) },
			want:     []agent.Record{{PID: 7, CWD: "/w", Kind: "interactive", StartedAt: 1, Status: "busy"}},
			wantArgs: []string{"agents", "--json"}, wantCap: AgentsOutputLimit,
		},
		{
			name:     "agents invalid json",
			result:   Result{Stdout: []byte("not json")},
			run:      func(c *Client) (any, error) { return c.Agents(context.Background()) },
			wantArgs: []string{"agents", "--json"}, wantCap: AgentsOutputLimit,
			wantErr: "decode claude agents json",
		},
		{
			name:     "non-zero exit with sanitized stderr",
			result:   Result{ExitCode: 2, Stderr: []byte("\x1b[31merror:\x1b[0m unknown command 'agents'\nsecond line\n")},
			run:      func(c *Client) (any, error) { return c.Agents(context.Background()) },
			wantArgs: []string{"agents", "--json"}, wantCap: AgentsOutputLimit,
			errIs: ErrCommandFailed, wantErr: "claude agents exited with status 2: error: unknown command 'agents'",
		},
		{
			name:     "non-zero exit without stderr",
			result:   Result{ExitCode: 1},
			run:      func(c *Client) (any, error) { return c.Version(context.Background()) },
			wantArgs: []string{"--version"}, wantCap: VersionOutputLimit,
			errIs: ErrCommandFailed, wantErr: "no error output",
		},
		{
			name:     "executor error passes through",
			execErr:  &NotFoundError{Command: "/usr/bin/claude"},
			run:      func(c *Client) (any, error) { return c.Version(context.Background()) },
			wantArgs: []string{"--version"}, wantCap: VersionOutputLimit,
			errIs: ErrNotFound,
		},
		{
			name:     "executor ignoring the limit is still capped",
			result:   Result{Stdout: make([]byte, VersionOutputLimit+1)},
			run:      func(c *Client) (any, error) { return c.Version(context.Background()) },
			wantArgs: []string{"--version"}, wantCap: VersionOutputLimit,
			errIs: ErrOutputTooLarge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []call
			exec := ExecutorFunc(func(ctx context.Context, cmd Cmd) (Result, error) {
				dl, ok := ctx.Deadline()
				if !ok {
					t.Fatal("command context has no deadline")
				}
				calls = append(calls, call{cmd: cmd, deadline: time.Until(dl)})
				return tc.result, tc.execErr
			})
			c, err := New(Options{Bin: "/usr/bin/claude", Executor: exec, Env: []string{"CLAUDE_CONFIG_DIR=/cfg"}, Timeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.run(c)
			if len(calls) != 1 {
				t.Fatalf("executor called %d times", len(calls))
			}
			cmd := calls[0].cmd
			if cmd.Bin != "/usr/bin/claude" || !slices.Equal(cmd.Args, tc.wantArgs) || cmd.StdoutLimit != tc.wantCap || !slices.Equal(cmd.Env, []string{"CLAUDE_CONFIG_DIR=/cfg"}) {
				t.Fatalf("command = %+v", cmd)
			}
			if calls[0].deadline > 3*time.Second || calls[0].deadline <= 0 {
				t.Fatalf("deadline in %s, want within the 3s timeout", calls[0].deadline)
			}
			if tc.errIs != nil || tc.wantErr != "" {
				if err == nil || (tc.errIs != nil && !errors.Is(err, tc.errIs)) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %v containing %q", err, tc.errIs, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch want := tc.want.(type) {
			case Version:
				if got != want {
					t.Fatalf("got %+v, want %+v", got, want)
				}
			case []agent.Record:
				if !slices.Equal(got.([]agent.Record), want) {
					t.Fatalf("got %+v, want %+v", got, want)
				}
			}
		})
	}
}

func TestClientAgainstFakeClaude(t *testing.T) {
	bin := fakeclaude.Build(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record.jsonl")
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`[{"id":"bg-1","cwd":"/w","kind":"background","startedAt":9,"state":"blocked"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Bin: bin, Env: []string{
		fakeclaude.EnvRecord + "=" + record, fakeclaude.EnvAgents + "=" + agentsFile,
		fakeclaude.EnvVersion + "=2.1.300 (Claude Code)", "CLAUDE_CONFIG_DIR=" + dir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v, err := c.Version(ctx)
	if err != nil || v != (Version{2, 1, 300, ""}) || CheckVersion(v) != nil {
		t.Fatalf("Version = %+v, %v", v, err)
	}
	records, err := c.Agents(ctx)
	if err != nil || len(records) != 1 || records[0].ID != "bg-1" || records[0].State != "blocked" {
		t.Fatalf("Agents = %+v, %v", records, err)
	}
	invs := fakeclaude.ReadRecords(t, record)
	if len(invs) != 2 || !slices.Equal(invs[0].Args, []string{"--version"}) || !slices.Equal(invs[1].Args, []string{"agents", "--json"}) {
		t.Fatalf("invocations = %+v", invs)
	}
	if invs[1].Env["CLAUDE_CONFIG_DIR"] != dir {
		t.Fatalf("extra env not passed: %v", invs[1].Env)
	}
}
