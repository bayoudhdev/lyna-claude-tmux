package devcontainer

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Command is one docker invocation.
type Command struct {
	Argv []string
	// Interactive attaches the terminal: stdin, stdout and stderr go straight
	// to the user and nothing is captured.
	Interactive bool
	// Stdin, when not nil, is fed to the command instead of the user's
	// standard input. It carries the firewall script, which must come from
	// this binary rather than from the image.
	Stdin []byte
}

// Executor runs docker commands. Tests record them; production uses Exec.
type Executor interface {
	Run(ctx context.Context, c Command) (string, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, c Command) (string, error)

// Run calls f.
func (f ExecutorFunc) Run(ctx context.Context, c Command) (string, error) { return f(ctx, c) }

// Exec runs commands as argument vectors without a shell.
type Exec struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Run executes c. Captured commands return standard output and fold standard
// error into the error; interactive commands stream to the configured writers.
func (e Exec) Run(ctx context.Context, c Command) (string, error) {
	if len(c.Argv) == 0 {
		return "", errors.New("devcontainer: empty command")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	if c.Interactive {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = e.Stdin, e.Stdout, e.Stderr
		if c.Stdin != nil {
			cmd.Stdin = bytes.NewReader(c.Stdin)
		}
		return "", cmd.Run()
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%s: %w: %s", strings.Join(c.Argv[:min(2, len(c.Argv))], " "), err, msg)
		}
		return stdout.String(), fmt.Errorf("%s: %w", strings.Join(c.Argv[:min(2, len(c.Argv))], " "), err)
	}
	return stdout.String(), nil
}

// Target is one project's container.
type Target struct {
	// Dir is the absolute project directory, bind mounted at /workspace.
	Dir string
	// Project names the image, container and volume (ValidateProject).
	Project string
	// User is the container account (ValidateUser).
	User string
}

// Validate checks the target before any docker command is built from it.
func (t Target) Validate() error {
	if !filepath.IsAbs(t.Dir) {
		return fmt.Errorf("devcontainer: project directory %q is not absolute", t.Dir)
	}
	if strings.ContainsAny(t.Dir, "\x00\n\r") {
		return fmt.Errorf("devcontainer: project directory %q contains control characters", t.Dir)
	}
	if err := ValidateProject(t.Project); err != nil {
		return err
	}
	return ValidateUser(t.User)
}

// Image is the image tag built for the project.
func (t Target) Image() string { return "lyna-tmux-" + t.Project }

// Container is the container name.
func (t Target) Container() string { return "lyna-tmux-" + t.Project }

// Volume is the named volume holding the Claude configuration.
func (t Target) Volume() string { return Volume(t.Project) }

// MountField quotes one --mount key=value pair. docker reads --mount as a CSV
// record, so a value containing a comma or a quote must be CSV-quoted or it
// would inject extra mount options (a path ",readonly=false,source=/").
func MountField(key, value string) string {
	field := key + "=" + value
	if !strings.ContainsAny(field, ",\"\r\n") {
		return field
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	// Writing a single field cannot fail on a strings.Builder.
	_ = w.Write([]string{field})
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// Argument vectors for each docker step. bin is the docker binary.

// BuildArgv builds the image from the rendered .devcontainer directory.
func BuildArgv(bin string, t Target) []string {
	ctxDir := filepath.Join(t.Dir, filepath.FromSlash(Dir))
	return []string{bin, "build", "--tag", t.Image(), "--file", filepath.Join(ctxDir, "Dockerfile"), ctxDir}
}

// StateArgv lists the container's state: running, exited, created, or
// nothing when it does not exist.
func StateArgv(bin string, t Target) []string {
	return []string{bin, "container", "ls", "--all", "--filter", "name=^/" + regexp.QuoteMeta(t.Container()) + "$", "--format", "{{.State}}"}
}

// RunArgv creates and starts the container. The Docker socket is never
// mounted; NET_ADMIN is added for the firewall, and no-new-privileges stops
// the container user from gaining root through setuid programs such as sudo
// (docker exec --user root still starts the firewall).
func RunArgv(bin string, t Target) []string {
	home := "/home/" + t.User
	return []string{
		bin, "run", "--detach", "--init",
		"--name", t.Container(),
		"--cap-add", "NET_ADMIN",
		"--security-opt", "no-new-privileges",
		"--user", t.User,
		"--mount", "type=bind," + MountField("source", t.Dir) + ",target=" + WorkspacePath,
		"--mount", "type=volume,source=" + t.Volume() + ",target=" + home + "/.claude",
		"--env", "CLAUDE_CONFIG_DIR=" + home + "/.claude",
		"--workdir", WorkspacePath,
		t.Image(),
	}
}

// StartArgv starts a stopped container.
func StartArgv(bin string, t Target) []string {
	return []string{bin, "start", t.Container()}
}

// FirewallArgv applies the egress firewall as root. bash reads the script
// from standard input (FirewallScript) and the allowlist from an environment
// value, so neither the script at FirewallPath nor the allowlist the image
// carries decides what the firewall does: a container started from an older
// or foreign image gets the current rules all the same.
func FirewallArgv(bin string, t Target, allowlist []byte) []string {
	return []string{
		bin, "exec", "--interactive", "--user", "root",
		"--env", FirewallAllowlistEnv + "=" + string(allowlist),
		t.Container(), "/bin/bash", "-s",
	}
}

// execArgv opens an interactive process as the container user, forwarding the
// terminal type and color support so the workspace renders as it does outside.
func execArgv(bin string, t Target, command ...string) []string {
	argv := []string{
		bin, "exec", "--interactive", "--tty",
		"--user", t.User,
		"--workdir", WorkspacePath,
		"--env", "TERM", "--env", "COLORTERM", "--env", "LANG",
		t.Container(),
	}
	return append(argv, command...)
}

// ShellArgv opens a login shell in the container.
func ShellArgv(bin string, t Target) []string {
	return execArgv(bin, t, "bash", "-l")
}

// CreateArgv runs a workspace command inside the container, so
// tmux, hooks and the status line all live there. args start with the
// subcommand (create or team) and are passed through as argv elements.
func CreateArgv(bin string, t Target, args []string) []string {
	return execArgv(bin, t, append([]string{xdg.Command}, args...)...)
}

// CreateDetachedArgv runs the same command without a terminal: docker refuses
// --tty when standard input is not one, and a detached workspace needs none.
func CreateDetachedArgv(bin string, t Target, args []string) []string {
	argv := []string{bin, "exec", "--user", t.User, "--workdir", WorkspacePath, "--env", "TERM", "--env", "COLORTERM", "--env", "LANG", t.Container(), xdg.Command}
	return append(argv, args...)
}

// StopArgv stops the container.
func StopArgv(bin string, t Target) []string {
	return []string{bin, "stop", t.Container()}
}

// RemoveArgv removes the stopped container; the Claude volume is kept.
func RemoveArgv(bin string, t Target) []string {
	return []string{bin, "rm", t.Container()}
}

// State is a container lifecycle state.
type State string

// Container states as reported by docker.
const (
	StateMissing State = ""
	StateRunning State = "running"
)

// Docker drives the container lifecycle.
type Docker struct {
	// Bin is the docker binary, "docker" when empty.
	Bin  string
	Exec Executor
	// Options are what the project's dev container files must render to. Up
	// re-renders them and refuses to build anything else, so a repository
	// cannot ship the image that is supposed to confine it. The project name
	// and the container user come from the Target, and the binary source
	// from the project's Dockerfile (DetectSource), so Source is ignored.
	Options Options
}

func (d Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

func (d Docker) capture(ctx context.Context, argv []string) (string, error) {
	return d.Exec.Run(ctx, Command{Argv: argv})
}

func (d Docker) attach(ctx context.Context, argv []string) error {
	_, err := d.Exec.Run(ctx, Command{Argv: argv, Interactive: true})
	return err
}

// feed runs an attached command with script on its standard input.
func (d Docker) feed(ctx context.Context, argv []string, script []byte) error {
	_, err := d.Exec.Run(ctx, Command{Argv: argv, Interactive: true, Stdin: script})
	return err
}

// files renders the dev container of t from the options of d and the binary
// source init recorded in the project.
func (d Docker) files(t Target) (map[string][]byte, error) {
	src, err := DetectSource(t.Dir)
	if err != nil {
		return nil, err
	}
	o := d.Options
	o.Project, o.User, o.Source = t.Project, t.User, src
	return Render(o)
}

// State reports the container state.
func (d Docker) State(ctx context.Context, t Target) (State, error) {
	if err := t.Validate(); err != nil {
		return StateMissing, err
	}
	out, err := d.capture(ctx, StateArgv(d.bin(), t))
	if err != nil {
		return StateMissing, err
	}
	return State(strings.TrimSpace(out)), nil
}

// Up verifies the rendered files, builds the image, creates or starts the
// container and applies the firewall. The build runs every time: the layer
// cache makes it cheap and a new container picks up edits to the rendered
// files. A running container keeps the image it started from until Down
// removes it, which is why the firewall step carries its own script.
//
// The verification comes first and nothing runs without it: docker build
// would otherwise execute whatever .devcontainer the project directory holds,
// with the project as its context, and the image it produced would then be
// what container isolation calls a boundary.
func (d Docker) Up(ctx context.Context, t Target) error {
	if err := t.Validate(); err != nil {
		return err
	}
	files, err := d.files(t)
	if err != nil {
		return err
	}
	if err := Verify(t.Dir, files); err != nil {
		return err
	}
	script, err := FirewallScript()
	if err != nil {
		return err
	}
	if err := d.attach(ctx, BuildArgv(d.bin(), t)); err != nil {
		return fmt.Errorf("%w: %s: %w; the step it stopped at is the last one above", ErrBuildFailed, t.Image(), err)
	}
	state, err := d.State(ctx, t)
	if err != nil {
		return err
	}
	switch state {
	case StateMissing:
		_, err = d.capture(ctx, RunArgv(d.bin(), t))
	case StateRunning:
	default:
		_, err = d.capture(ctx, StartArgv(d.bin(), t))
	}
	if err != nil {
		return err
	}
	return d.feed(ctx, FirewallArgv(d.bin(), t, files[FileAllowedDomain]), script)
}

// Shell opens an interactive shell in a running container.
func (d Docker) Shell(ctx context.Context, t Target) error {
	if err := d.requireRunning(ctx, t); err != nil {
		return err
	}
	return d.attach(ctx, ShellArgv(d.bin(), t))
}

// Create runs lmux create in a running container.
func (d Docker) Create(ctx context.Context, t Target, args []string) error {
	if err := d.requireRunning(ctx, t); err != nil {
		return err
	}
	return d.attach(ctx, CreateArgv(d.bin(), t, args))
}

// ErrNotRunning is returned when a command needs a running container.
var ErrNotRunning = errors.New("devcontainer: container is not running (run lmux sandbox devcontainer up)")

// ErrBuildFailed is returned when docker build does not produce the image. The
// build streams to the terminal, so the reason is on screen above this error
// and is not repeated inside it.
var ErrBuildFailed = errors.New("devcontainer: the image build failed")

// requireRunning checks that the container is running and that the tree it was
// built from is still the one lyna-tmux renders. A shell or a workspace opened
// against a rewritten .devcontainer would claim an isolation boundary nobody
// can check any more, so the files are verified here as they are before a
// build. Down does not verify: stopping a container must always work.
func (d Docker) requireRunning(ctx context.Context, t Target) error {
	files, err := d.files(t)
	if err != nil {
		return err
	}
	if err := Verify(t.Dir, files); err != nil {
		return err
	}
	state, err := d.State(ctx, t)
	if err != nil {
		return err
	}
	if state != StateRunning {
		return fmt.Errorf("%s: %w", t.Container(), ErrNotRunning)
	}
	return nil
}

// Down stops and removes the container, keeping the image and the Claude
// volume. A missing container is not an error.
func (d Docker) Down(ctx context.Context, t Target) error {
	state, err := d.State(ctx, t)
	if err != nil {
		return err
	}
	switch state {
	case StateMissing:
		return nil
	case StateRunning:
		if _, err := d.capture(ctx, StopArgv(d.bin(), t)); err != nil {
			return err
		}
	}
	_, err = d.capture(ctx, RemoveArgv(d.bin(), t))
	return err
}
