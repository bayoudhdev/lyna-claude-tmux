// Package hook handles `lyna-tmux hook <Event>`: Claude Code runs it for every
// registered hook event, and it mirrors the agent's state onto the tmux pane
// the agent runs in, so the status line, pane borders and pickers update
// without polling.
//
// The handler runs on every tool call, so it is built to be cheap and inert:
// no subprocess except one tmux client exec, stdin capped at MaxStdinBytes,
// nothing from the payload ever executed, errors written to a size-capped log
// and the exit status always 0. A hook that failed or printed would show up as
// a hook error in the user's transcript, and stdout of some events is even fed
// to the model, so the handler never writes to stdout or stderr.
package hook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/hookevent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook/githead"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

const (
	// envTmux and envTmuxPane are set by tmux in every pane process. Claude
	// Code passes its environment to hooks, so they locate the agent's pane.
	envTmux     = "TMUX"
	envTmuxPane = "TMUX_PANE"

	// LogMaxBytes caps the diagnostic log before it rotates.
	LogMaxBytes = 256 << 10
	// execTimeout bounds the tmux client call. Hooks run asynchronously and
	// Claude Code does not time them out, so a wedged server must not leave
	// hook processes behind.
	execTimeout = 5 * time.Second
)

// Agent states stored in the pane option tmux.OptState.
const (
	StateBusy    = "busy"
	StateWaiting = "waiting"
	StateIdle    = "idle"
)

// Input is one hook invocation.
type Input struct {
	// Event is the Claude Code hook event name given on the command line.
	Event string
	// Plugin marks invocations from the Claude plugin's hooks.json, which are
	// skipped inside managed panes whose settings register the same hooks.
	Plugin bool
	// Stdin carries the JSON payload; nil reads as empty.
	Stdin io.Reader
	// Getenv reads the hook's environment; nil reads as empty.
	Getenv func(string) string
}

// Deps are the side effects of the handler, replaceable in tests. Zero fields
// take the production implementation.
type Deps struct {
	// Tmux returns a client for the server a hook reports to.
	Tmux func(tmux.Socket) *tmux.Client
	// Now timestamps log lines.
	Now func() time.Time
	// LogPath is the diagnostic log (xdg Paths.LogFile); empty disables logging.
	LogPath string
	// ReadFile reads git metadata without following final symbolic links.
	ReadFile githead.ReadFunc
	// Getwd is the fallback directory when a payload carries no cwd.
	Getwd func() (string, error)
	// OpenTTY opens a pane terminal for writing the bell.
	OpenTTY func(path string) (io.WriteCloser, error)
}

func (d Deps) withDefaults() Deps {
	if d.Tmux == nil {
		d.Tmux = func(s tmux.Socket) *tmux.Client { return tmux.New(tmux.Options{Socket: s}) }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.ReadFile == nil {
		d.ReadFile = githead.ReadFile
	}
	if d.Getwd == nil {
		d.Getwd = os.Getwd
	}
	if d.OpenTTY == nil {
		d.OpenTTY = OpenTTY
	}
	return d
}

// Run handles one hook event and returns the process exit status, which is
// always 0.
func Run(ctx context.Context, in Input, deps Deps) (status int) {
	deps = deps.withDefaults()
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	defer func() {
		if r := recover(); r != nil {
			deps.logf("hook", "internal error: %v", r)
			status = 0
		}
	}()

	ev, ok := hookevent.Parse(in.Event)
	if !ok {
		deps.logf("hook", "unknown event %q", in.Event)
		return 0
	}
	if in.Plugin && getenv(session.EnvManaged) == "1" {
		return 0
	}
	sock, inTmux := tmux.SocketFromEnv(getenv(envTmux))
	pane := getenv(envTmuxPane)
	if !inTmux || pane == "" {
		return 0
	}
	name := "hook " + string(ev)
	if !validID(pane, '%') {
		deps.logf(name, "invalid %s %q", envTmuxPane, pane)
		return 0
	}

	payload, known := deps.readPayload(name, in.Stdin)
	var branch *string
	if ev == hookevent.SessionStart || ev == hookevent.Stop {
		branch = deps.branch(name, payload.Cwd)
	}

	cmds, ring := Commands(ev, payload, known, pane, branch, getenv(session.EnvBell) != "0")
	if len(cmds) == 0 {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	out, err := deps.Tmux(sock).Batch(ctx, cmds...)
	if err != nil {
		// A session ending with its pane or server already gone is the normal
		// teardown order, not a fault.
		if ev != hookevent.SessionEnd || !targetGone(err) {
			deps.logf(name, "%v", err)
		}
		return 0
	}
	if ring {
		if err := ringTTY(deps.OpenTTY, lastLine(out)); err != nil {
			deps.logf(name, "bell: %v", err)
		}
	}
	return 0
}

// readPayload reads and decodes stdin. known is false when the payload was
// missing, oversized or malformed; the caller then falls back to what the
// event registration alone guarantees.
func (d Deps) readPayload(name string, r io.Reader) (Payload, bool) {
	if r == nil {
		d.logf(name, "no payload on stdin")
		return Payload{}, false
	}
	data, err := fsx.ReadLimited(r, MaxStdinBytes)
	if err != nil {
		d.logf(name, "read payload: %v", err)
		return Payload{}, false
	}
	p, err := DecodePayload(data)
	if err != nil {
		d.logf(name, "%v", err)
		return Payload{}, false
	}
	return p, true
}

// branch returns the label for @lt_branch, "" to clear it outside a
// repository, or nil to leave the option alone when the lookup failed.
func (d Deps) branch(name, cwd string) *string {
	dir := cwd
	if dir == "" || !filepath.IsAbs(dir) {
		wd, err := d.Getwd()
		if err != nil {
			d.logf(name, "working directory: %v", err)
			return nil
		}
		dir = wd
	}
	label, err := githead.Branch(dir, d.ReadFile)
	switch {
	case err == nil:
		return &label
	case errors.Is(err, githead.ErrNoRepository):
		empty := ""
		return &empty
	default:
		d.logf(name, "branch: %v", err)
		return nil
	}
}

// Formats evaluated by tmux while it executes the batch. Arithmetic runs
// inside one set-option -F, which the server applies atomically, so hooks
// fired concurrently for parallel subagents never lose an update.
const (
	fmtSubagentsInc = "#{e|+:#{" + tmux.OptSubagents + "},1}"
	fmtSubagentsDec = "#{?#{e|>:#{" + tmux.OptSubagents + "},0},#{e|-:#{" + tmux.OptSubagents + "},1},0}"
)

// fmtChangesSignal signals the changes channel of the session holding the
// pane. The session id is only known to tmux, so the command is built by
// run-shell -C from a format. The id goes into a single-quoted token, where
// the tmux parser gives no character a meaning except the quote itself, and
// an id is "$" plus digits, so no session needs to be guarded against or
// skipped: every one of them is signaled, whatever its name holds.
var fmtChangesSignal = "wait-for -S '" + tmux.ChangesChannel("#{session_id}") + "'"

// Commands returns the tmux batch for an event on pane and whether the pane
// bell rings after it. known reports that payload was decoded; without it the
// handler relies on the matcher each event is registered with. branch is the
// new @lt_branch value ("" clears it) or nil to leave it unchanged. The last
// command of a ringing batch prints the pane's terminal path.
func Commands(ev hookevent.Event, p Payload, known bool, pane string, branch *string, bell bool) ([]tmux.Command, bool) {
	var cmds []tmux.Command
	state := func(s string) { cmds = append(cmds, tmux.Command{"set-option", "-p", "-t", pane, tmux.OptState, s}) }
	ring := false

	switch ev {
	case hookevent.SessionStart:
		// Compaction restarts the session in the middle of a turn: the agent
		// is still working.
		if p.Source != "compact" {
			state(StateIdle)
		}
	case hookevent.UserPromptSubmit:
		state(StateBusy)
	case hookevent.PreToolUse:
		if known && p.ToolName != hookevent.MatchAskUser {
			return nil, false
		}
		state(StateWaiting)
		ring = bell
	case hookevent.PermissionRequest:
		state(StateWaiting)
		ring = bell
	case hookevent.Notification:
		if known && p.NotificationType != hookevent.MatchPermissionPrompt {
			return nil, false
		}
		state(StateWaiting)
		ring = bell
	case hookevent.PostToolUse:
		// Background subagents keep using tools after the main agent stopped
		// or while it waits on the user; only the main agent's own tool calls
		// mean it is busy again.
		if p.AgentID == "" {
			state(StateBusy)
		}
		// An unreadable payload is most often an oversized file write:
		// signaling costs the changes pane one refresh, missing it leaves the
		// pane stale.
		if !known || changesFiles(p.ToolName) {
			cmds = append(cmds, tmux.Command{"run-shell", "-C", "-t", pane, fmtChangesSignal})
		}
	case hookevent.SubagentStart:
		cmds = append(cmds, tmux.Command{"set-option", "-p", "-t", pane, "-F", tmux.OptSubagents, fmtSubagentsInc})
	case hookevent.SubagentStop:
		cmds = append(cmds, tmux.Command{"set-option", "-p", "-t", pane, "-F", tmux.OptSubagents, fmtSubagentsDec})
	case hookevent.Stop:
		state(StateIdle)
		ring = bell
	case hookevent.SessionEnd:
		cmds = append(cmds,
			tmux.Command{"set-option", "-p", "-u", "-t", pane, tmux.OptState},
			tmux.Command{"set-option", "-p", "-u", "-t", pane, tmux.OptSubagents},
		)
	}

	if branch != nil && (ev == hookevent.SessionStart || ev == hookevent.Stop) {
		// A pane target resolves to the session holding the pane.
		if *branch == "" {
			cmds = append(cmds, tmux.Command{"set-option", "-u", "-t", pane, tmux.OptBranch})
		} else {
			cmds = append(cmds, tmux.Command{"set-option", "-t", pane, tmux.OptBranch, tmux.BranchOption(*branch)})
		}
	}
	if ring {
		cmds = append(cmds, tmux.Command{"display-message", "-p", "-t", pane, "#{pane_tty}"})
	}
	return cmds, ring
}

// targetGone reports whether a tmux failure means the server, or the pane the
// batch targets, no longer exists. Option commands report a missing pane or
// session as "no such pane" or "no such session" rather than the "can't find"
// wording of other commands.
func targetGone(err error) bool {
	if errors.Is(err, tmux.ErrNoServer) || errors.Is(err, tmux.ErrNotFound) {
		return true
	}
	var te *tmux.Error
	return errors.As(err, &te) &&
		(strings.Contains(te.Stderr, "no such pane") || strings.Contains(te.Stderr, "no such session"))
}

// changesFiles reports whether a tool is one of the file-editing tools the
// changes pane refreshes for.
func changesFiles(tool string) bool {
	if tool == "" {
		return false
	}
	for name := range strings.SplitSeq(hookevent.MatchFileEdits, "|") {
		if name == tool {
			return true
		}
	}
	return false
}

// validID reports whether s is a tmux object ID: the sigil followed by digits.
func validID(s string, sigil byte) bool {
	if len(s) < 2 || s[0] != sigil {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func lastLine(out string) string {
	out = strings.TrimRight(out, "\n")
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	return out
}

var errNotTTY = errors.New("not a terminal device")

// OpenTTY opens a terminal device for writing. The path comes from tmux
// (#{pane_tty}); it must name a character device under /dev, is opened without
// becoming the controlling terminal (hooks run without one, and acquiring a
// pane's would tie the hook to it), and in non-blocking mode so a pane whose
// output is stopped cannot hang the hook.
func OpenTTY(path string) (io.WriteCloser, error) {
	if !strings.HasPrefix(path, "/dev/") || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%q: %w", path, errNotTTY)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%q: %w", path, errNotTTY)
	}
	return f, nil
}

// ringTTY writes BEL to a pane's terminal. tmux treats it like the program in
// the pane ringing: with monitor-bell on it flags the window
// (#{window_bell_flag}) and runs the alert-bell hook. Hooks have no
// controlling terminal, so /dev/tty is not an option.
func ringTTY(open func(string) (io.WriteCloser, error), path string) error {
	w, err := open(path)
	if err != nil {
		return err
	}
	_, werr := w.Write([]byte{'\a'})
	return errors.Join(werr, w.Close())
}

// logf appends one line to the diagnostic log. Logging failures are dropped:
// there is nowhere left to report them without disturbing Claude Code.
func (d Deps) logf(name, format string, args ...any) {
	if d.LogPath == "" {
		return
	}
	line := d.Now().UTC().Format(time.RFC3339) + " " + name + ": " + sanitize.Line(fmt.Sprintf(format, args...))
	err := fsx.AppendCapped(d.LogPath, []byte(line), LogMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		if fsx.EnsurePrivateDir(filepath.Dir(d.LogPath)) == nil {
			_ = fsx.AppendCapped(d.LogPath, []byte(line), LogMaxBytes)
		}
	}
}
