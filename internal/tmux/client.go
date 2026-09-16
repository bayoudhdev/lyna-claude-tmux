// Package tmux drives a tmux server through its command-line client.
//
// Every command is executed as an argument vector, never through a shell, and
// every data argument passes through escapeArg so it cannot split a command
// batch. Strings that tmux itself parses later (configuration lines, bindings,
// formats, run-shell commands) go through ConfQuote, FormatEscape, DrawEscape and ShellQuote.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DefaultSocketName is the -L socket of the server lyna-tmux manages.
const DefaultSocketName = "lyna-tmux"

var (
	// ErrNotInstalled is returned when the tmux binary cannot be found.
	ErrNotInstalled = errors.New("tmux: not installed")
	// ErrNoServer matches failures caused by no server listening on the socket.
	ErrNoServer = errors.New("tmux: no server running")
	// ErrNotFound matches failures caused by a missing session, window or pane.
	ErrNotFound = errors.New("tmux: target not found")
	// ErrExists matches failures caused by creating a session that already exists.
	ErrExists = errors.New("tmux: session already exists")
)

// Socket selects the server a client talks to. The zero value uses tmux's own
// resolution ($TMUX, then the default socket).
type Socket struct {
	Name string // -L socket name, resolved under the tmux temporary directory
	Path string // -S absolute socket path; takes precedence over Name
}

// Args returns the global flags selecting the socket.
func (s Socket) Args() []string {
	switch {
	case s.Path != "":
		return []string{"-S", s.Path}
	case s.Name != "":
		return []string{"-L", s.Name}
	default:
		return nil
	}
}

// IsZero reports whether no socket was selected.
func (s Socket) IsZero() bool { return s.Name == "" && s.Path == "" }

// SocketFromEnv extracts the socket path from a $TMUX value ("path,pid,index").
func SocketFromEnv(value string) (Socket, bool) {
	path, _, _ := strings.Cut(value, ",")
	if path == "" || !strings.HasPrefix(path, "/") {
		return Socket{}, false
	}
	return Socket{Path: path}, true
}

// Result is the captured outcome of one tmux invocation.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Executor runs a program. The default implementation uses os/exec; tests
// substitute a recorder.
type Executor interface {
	Exec(ctx context.Context, bin string, args []string) (Result, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, bin string, args []string) (Result, error)

// Exec implements Executor.
func (f ExecutorFunc) Exec(ctx context.Context, bin string, args []string) (Result, error) {
	return f(ctx, bin, args)
}

// osExecutor runs programs with os/exec. A nil env inherits the process
// environment.
type osExecutor struct {
	env []string
}

func (o osExecutor) Exec(ctx context.Context, bin string, args []string) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	cmd.Env = o.env
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return res, ErrNotInstalled
		}
		return res, err
	}
	return res, nil
}

// Options configure a Client.
type Options struct {
	Bin      string // tmux binary; defaults to "tmux" resolved on PATH
	Socket   Socket
	Config   string // -f file, used when the command starts the server
	Executor Executor
	// Env is the environment of the tmux client process; nil inherits the
	// current one. A command that starts the server makes it the server's
	// global environment, which every pane inherits. Ignored with Executor.
	Env []string
}

// Client issues commands to one tmux server.
type Client struct {
	bin    string
	socket Socket
	config string
	exec   Executor
}

// New builds a Client. It does not contact tmux.
func New(opts Options) *Client {
	c := &Client{bin: opts.Bin, socket: opts.Socket, config: opts.Config, exec: opts.Executor}
	if c.bin == "" {
		c.bin = "tmux"
	}
	if c.exec == nil {
		c.exec = osExecutor{env: opts.Env}
	}
	return c
}

// Bin returns the tmux binary the client runs.
func (c *Client) Bin() string { return c.bin }

// Socket returns the socket the client targets.
func (c *Client) Socket() Socket { return c.socket }

// WithSocket returns a copy of c targeting another server.
func (c *Client) WithSocket(s Socket) *Client {
	clone := *c
	clone.socket = s
	return &clone
}

// Command is one tmux command and its arguments, for example
// Command{"set-option", "-p", "-t", "%1", "@lt_state", "busy"}.
type Command []string

// utf8Flag tells tmux that the bytes it exchanges with this client are UTF-8.
// Without it tmux guesses from the locale, and in the C locale it keeps no
// byte it cannot represent: a session name, an option value and even the
// separator of a reply come back as underscores or as octal escapes, so a
// value set through one command is no longer the value another command reads.
// Everything this CLI sends and reads is UTF-8 whatever the account's locale
// says, so it states that rather than leaving it to the environment.
const utf8Flag = "-u"

// Argv returns the complete argument vector (binary first) for a batch of
// commands, suitable for syscall.Exec when the process should become tmux.
func (c *Client) Argv(cmds ...Command) []string {
	return append([]string{c.bin}, c.args(cmds)...)
}

func (c *Client) args(cmds []Command) []string {
	args := append(c.socket.Args(), utf8Flag)
	if c.config != "" {
		args = append(args, "-f", c.config)
	}
	emitted := false
	for _, cmd := range cmds {
		if len(cmd) == 0 {
			continue
		}
		if emitted {
			args = append(args, ";")
		}
		emitted = true
		args = append(args, cmd[0])
		for _, a := range cmd[1:] {
			args = append(args, escapeArg(a))
		}
	}
	return args
}

// Run executes a single command and returns its standard output.
func (c *Client) Run(ctx context.Context, name string, args ...string) (string, error) {
	return c.Batch(ctx, append(Command{name}, args...))
}

// Batch executes commands in one tmux invocation, separated by ";". tmux stops
// at the first failing command.
func (c *Client) Batch(ctx context.Context, cmds ...Command) (string, error) {
	argv := c.args(cmds)
	res, err := c.exec.Exec(ctx, c.bin, argv)
	if err != nil {
		return "", &Error{Args: argv, Err: err}
	}
	if res.ExitCode != 0 {
		return string(res.Stdout), &Error{Args: argv, Stderr: strings.TrimSpace(string(res.Stderr)), Code: res.ExitCode}
	}
	return string(res.Stdout), nil
}

// Version runs `tmux -V`, which needs no server.
func (c *Client) Version(ctx context.Context) (Version, error) {
	res, err := c.exec.Exec(ctx, c.bin, []string{"-V"})
	if err != nil {
		return Version{}, err
	}
	if res.ExitCode != 0 {
		return Version{}, &Error{Args: []string{"-V"}, Stderr: strings.TrimSpace(string(res.Stderr)), Code: res.ExitCode}
	}
	return ParseVersion(string(res.Stdout))
}

// Error describes a failed tmux invocation.
type Error struct {
	Args   []string
	Stderr string
	Code   int
	Err    error
}

func (e *Error) Error() string {
	cmd := "tmux"
	for i := 0; i < len(e.Args); i++ {
		a := e.Args[i]
		if globalFlagTakesValue(a) {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") && a != ";" {
			cmd = "tmux " + a
			break
		}
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", cmd, e.Err)
	}
	if e.Stderr == "" {
		return fmt.Sprintf("%s: exit status %d", cmd, e.Code)
	}
	return fmt.Sprintf("%s: %s", cmd, e.Stderr)
}

// globalFlagTakesValue reports whether a tmux server flag consumes the next
// argument, so the command name is not confused with a socket or file path.
func globalFlagTakesValue(flag string) bool {
	switch flag {
	case "-L", "-S", "-f", "-T", "-c":
		return true
	}
	return false
}

func (e *Error) Unwrap() error { return e.Err }

// Is maps tmux's diagnostic text onto ErrNoServer and ErrNotFound.
func (e *Error) Is(target error) bool {
	msg := e.Stderr
	switch target {
	case ErrNoServer:
		return strings.Contains(msg, "no server running") ||
			strings.Contains(msg, "error connecting to") ||
			strings.Contains(msg, "server exited unexpectedly")
	case ErrNotFound:
		return strings.Contains(msg, "can't find session") ||
			strings.Contains(msg, "can't find window") ||
			strings.Contains(msg, "can't find pane") ||
			strings.Contains(msg, "can't find client") ||
			strings.Contains(msg, "session not found") ||
			strings.Contains(msg, "no such pane") ||
			strings.Contains(msg, "no such session") ||
			strings.Contains(msg, "no current target")
	case ErrExists:
		return strings.Contains(msg, "duplicate session")
	}
	return false
}

// ExactSession returns a target that matches a session name exactly; a bare
// name would also match by prefix and by fnmatch pattern. The trailing colon
// makes the same string valid for commands that take a window or pane target
// (split-window, capture-pane, set-option, set-hook), which otherwise reject
// "=name" or, for display-message, silently resolve nothing.
func ExactSession(name string) string { return "=" + name + ":" }
