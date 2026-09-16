package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ErrOutputTooLarge is returned when a command writes more than its limit.
var ErrOutputTooLarge = errors.New("claude: command output exceeds its size limit")

// waitDelay bounds how long a finished command may keep its output pipes open
// through a process it left behind.
const waitDelay = 2 * time.Second

// stderrLimit caps the diagnostic output kept from a failed command.
const stderrLimit = 64 << 10

// Cmd is one claude invocation.
type Cmd struct {
	Bin  string
	Args []string
	// Env entries are added to the inherited environment.
	Env []string
	// StdoutLimit is the most stdout bytes accepted; past it the command is
	// killed and Exec returns ErrOutputTooLarge.
	StdoutLimit int64
}

// Result is the captured outcome of a command that ran to completion.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Executor runs a command without a shell. Tests substitute a fake.
type Executor interface {
	Exec(ctx context.Context, cmd Cmd) (Result, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, cmd Cmd) (Result, error)

// Exec implements Executor.
func (f ExecutorFunc) Exec(ctx context.Context, cmd Cmd) (Result, error) { return f(ctx, cmd) }

// OSExecutor runs commands with os/exec, stdin connected to the null device.
type OSExecutor struct{}

// Exec implements Executor. A non-zero exit is reported in Result.ExitCode
// with a nil error; a command that cannot start, is killed by ctx or exceeds
// its output limit returns an error.
func (OSExecutor) Exec(ctx context.Context, c Cmd) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	stdout := &cappedBuffer{limit: c.StdoutLimit, onOverflow: cancel}
	stderr := &cappedBuffer{limit: stderrLimit, truncate: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	res := Result{Stdout: stdout.buf, Stderr: stderr.buf}
	if stdout.overflow {
		return Result{}, fmt.Errorf("%s: %w (%d bytes)", c.Bin, ErrOutputTooLarge, c.StdoutLimit)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, fmt.Errorf("%s: %w", c.Bin, context.Cause(ctx))
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Result{}, &NotFoundError{Command: c.Bin}
		}
		return Result{}, fmt.Errorf("run %s: %w", c.Bin, err)
	}
	return res, nil
}

// cappedBuffer collects output up to limit bytes. Past the limit it either
// keeps the head (truncate) or records the overflow and calls onOverflow so
// the producer is stopped instead of filling memory. It keeps accepting
// writes so the producer never blocks on a full pipe before it is killed.
type cappedBuffer struct {
	buf        []byte
	limit      int64
	truncate   bool
	overflow   bool
	onOverflow func()
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - int64(len(b.buf))
	if int64(len(p)) <= room {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	if room > 0 {
		b.buf = append(b.buf, p[:room]...)
	}
	if !b.truncate && !b.overflow {
		b.overflow = true
		if b.onOverflow != nil {
			b.onOverflow()
		}
	}
	return len(p), nil
}
