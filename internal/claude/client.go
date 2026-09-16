package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Limits and timeouts for the read-only commands the client runs.
const (
	// DefaultTimeout bounds one invocation. A cold start of a script-based
	// install on a busy machine takes a few seconds; nothing here is interactive.
	DefaultTimeout = 15 * time.Second
	// VersionOutputLimit caps `claude --version` output.
	VersionOutputLimit = 64 << 10
	// AgentsOutputLimit caps `claude agents --json` output.
	AgentsOutputLimit = 4 << 20
)

// ErrCommandFailed matches a claude command that exited non-zero.
var ErrCommandFailed = errors.New("claude: command failed")

// Options configure a Client.
type Options struct {
	// Bin is the absolute path of the claude executable, usually from Find.
	Bin string
	// Executor runs commands; nil uses OSExecutor.
	Executor Executor
	// Env entries are added to the inherited environment of every command.
	Env []string
	// Timeout bounds each command; zero uses DefaultTimeout.
	Timeout time.Duration
}

// Client runs read-only claude commands.
type Client struct {
	bin     string
	exec    Executor
	env     []string
	timeout time.Duration
}

// New returns a Client. Bin must be absolute: commands never go through a
// PATH lookup that the working directory could influence.
func New(opts Options) (*Client, error) {
	if !filepath.IsAbs(opts.Bin) {
		return nil, fmt.Errorf("claude: executable path must be absolute (got %q)", opts.Bin)
	}
	c := &Client{bin: opts.Bin, exec: opts.Executor, env: append([]string(nil), opts.Env...), timeout: opts.Timeout}
	if c.exec == nil {
		c.exec = OSExecutor{}
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	return c, nil
}

// Bin returns the executable path.
func (c *Client) Bin() string { return c.bin }

// Version runs `claude --version` and parses its output.
func (c *Client) Version(ctx context.Context) (Version, error) {
	out, err := c.run(ctx, VersionOutputLimit, "--version")
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(string(out))
}

// Agents runs `claude agents --json` and decodes its records.
func (c *Client) Agents(ctx context.Context) ([]agent.Record, error) {
	out, err := c.run(ctx, AgentsOutputLimit, "agents", "--json")
	if err != nil {
		return nil, err
	}
	return agent.ParseRecords(bytes.TrimSpace(out))
}

func (c *Client) run(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout, fmt.Errorf("claude %s: timed out after %s: %w", args[0], c.timeout, context.DeadlineExceeded))
	defer cancel()
	res, err := c.exec.Exec(ctx, Cmd{Bin: c.bin, Args: args, Env: c.env, StdoutLimit: limit})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		detail := sanitize.Line(string(firstLine(res.Stderr)))
		if detail == "" {
			detail = "no error output"
		}
		return nil, fmt.Errorf("%w: claude %s exited with status %d: %s", ErrCommandFailed, args[0], res.ExitCode, clip(detail))
	}
	if int64(len(res.Stdout)) > limit {
		return nil, fmt.Errorf("claude %s: %w (%d bytes)", args[0], ErrOutputTooLarge, limit)
	}
	return res.Stdout, nil
}

func firstLine(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i]
	}
	return b
}
