// Package fakeclaude is a stand-in for the claude executable in tests. It
// records every invocation, answers the read-only commands lyna-tmux runs,
// runs hook commands from a --settings file the way Claude Code does, opens
// the teammates of an agent team the way Claude Code's tmux backend does, and
// then behaves like an interactive session until stdin closes or it receives
// SIGTERM or SIGHUP.
//
// Tests call Build for an executable named claude and steer it with the
// FAKECLAUDE_* environment variables.
package fakeclaude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Environment variables that steer the fake.
const (
	// EnvRecord names a file that receives one JSON line per invocation and
	// per hook run.
	EnvRecord = "FAKECLAUDE_RECORD"
	// EnvVersion replaces the `--version` output line.
	EnvVersion = "FAKECLAUDE_VERSION"
	// EnvAgents names a file printed verbatim by `agents --json`.
	EnvAgents = "FAKECLAUDE_AGENTS"
	// EnvHooks is a comma-separated list of hook events to fire, in order,
	// when the fake starts as an interactive session.
	EnvHooks = "FAKECLAUDE_HOOKS"
	// EnvTool is the tool name tool events carry (default AskUserQuestion).
	EnvTool = "FAKECLAUDE_TOOL"
	// EnvSessionID is the session_id hook payloads carry.
	EnvSessionID = "FAKECLAUDE_SESSION_ID"
	// EnvExit set to "1" makes an interactive session exit after its hooks
	// and its team.
	EnvExit = "FAKECLAUDE_EXIT"
	// EnvBlock set to "1" makes every command wait for a signal before doing
	// anything but recording, for timeout tests.
	EnvBlock = "FAKECLAUDE_BLOCK"
	// EnvTeammates makes an interactive session the lead of an agent team: a
	// comma-separated list of "name:type" teammates (the type may be empty)
	// it opens once its hooks ran. Inside tmux each one gets a pane of the
	// lead's window, set up with the commands Claude Code's tmux backend runs
	// and started with CLAUDE_CODE_TEAMMATE_COMMAND, or with the fake itself
	// when that is unset; with teammateMode "in-process", or outside tmux,
	// they stay inside the lead. The team file is written under
	// CLAUDE_CONFIG_DIR, which must be set. A session started with teammate
	// arguments is a teammate and opens no team.
	EnvTeammates = "FAKECLAUDE_TEAMMATES"
	// EnvTeamGate names a tmux wait-for channel on the lead's server that the
	// lead waits on before opening each teammate, so a test decides when each
	// one opens. Unset, they open in one burst, as a model spawning several
	// teammates in one turn opens them.
	EnvTeamGate = "FAKECLAUDE_TEAM_GATE"
)

// Defaults for unset variables.
const (
	DefaultVersion   = "2.1.272 (Claude Code)"
	DefaultTool      = "AskUserQuestion"
	DefaultSessionID = "00000000-0000-4000-8000-00000000fa6e"
)

// ReadyLine is printed on stdout once an interactive session has run its
// hooks and waits for stdin EOF or a signal.
const ReadyLine = "fakeclaude: session ready"

// hookTimeout bounds one hook command when its handler sets no timeout.
const hookTimeout = 60 * time.Second

// Invocation is one recorded start of the fake.
type Invocation struct {
	Kind string `json:"kind"` // "invocation"
	// Program is argv[0]; Args are the remaining arguments.
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
	PID     int      `json:"pid"`
	// Env holds LYNA_TMUX_*, CLAUDE_CODE_*, CLAUDE_CONFIG_DIR, TMUX and TMUX_PANE.
	Env map[string]string `json:"env"`
	// SettingsPath is the --settings value when it names a file.
	SettingsPath string `json:"settingsPath,omitempty"`
	// Settings is the --settings document, from the file or inline JSON.
	Settings json.RawMessage `json:"settings,omitempty"`
	// SettingsError explains why a --settings value could not be read.
	SettingsError string `json:"settingsError,omitempty"`
}

// HookRun is one recorded hook command execution.
type HookRun struct {
	Kind     string          `json:"kind"` // "hook"
	Event    string          `json:"event"`
	Matcher  string          `json:"matcher,omitempty"`
	Command  string          `json:"command"`
	Input    json.RawMessage `json:"input"`
	ExitCode int             `json:"exitCode"`
	Stdout   string          `json:"stdout"`
	Stderr   string          `json:"stderr"`
	Error    string          `json:"error,omitempty"`
}

// Process is the environment Main runs in.
type Process struct {
	Args    []string // including argv[0]
	Environ []string
	Dir     string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Signal is closed when the process receives SIGTERM or SIGHUP.
	Signal <-chan struct{}
	// Executable is the fake's own path, which a teammate is started with
	// when no CLAUDE_CODE_TEAMMATE_COMMAND replaces it; empty uses Args[0].
	Executable string
}

// OSProcess describes the running executable: its arguments, environment,
// working directory and standard streams, with Signal closed on SIGTERM or
// SIGHUP. stop releases the signal handler.
func OSProcess() (p Process, stop func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP)
	closed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
			close(closed)
		case <-done:
		}
	}()
	dir, err := os.Getwd()
	if err != nil {
		dir = ""
	}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	p = Process{Args: os.Args, Environ: os.Environ(), Dir: dir, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Signal: closed, Executable: exe}
	var once sync.Once
	return p, func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

func (p Process) getenv(key string) string {
	for i := len(p.Environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(p.Environ[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// Main runs the fake and returns its exit status.
func Main(p Process) int {
	args := p.Args[1:]
	inv := p.invocation(args)
	if err := p.record(inv); err != nil {
		fmt.Fprintf(p.Stderr, "fakeclaude: record: %v\n", err)
		return 1
	}
	if p.getenv(EnvBlock) == "1" {
		<-p.Signal
		return 143
	}
	switch {
	case hasArg(args, "--version") || hasArg(args, "-v"):
		version := p.getenv(EnvVersion)
		if version == "" {
			version = DefaultVersion
		}
		fmt.Fprintln(p.Stdout, version)
		return 0
	case len(args) > 0 && args[0] == "agents":
		return p.agents(args[1:])
	}
	if code := p.runHooks(inv); code != 0 {
		return code
	}
	if code := p.openTeam(inv); code != 0 {
		return code
	}
	if p.getenv(EnvExit) == "1" {
		return 0
	}
	// Signal handling is installed before Main runs, so a test that waits for
	// this line can signal the session without racing its startup.
	fmt.Fprintln(p.Stdout, ReadyLine)
	eof := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, p.Stdin)
		close(eof)
	}()
	select {
	case <-eof:
		return 0
	case <-p.Signal:
		return 0
	}
}

func (p Process) agents(args []string) int {
	if !hasArg(args, "--json") {
		fmt.Fprintln(p.Stderr, "fakeclaude: only `agents --json` is implemented")
		return 2
	}
	path := p.getenv(EnvAgents)
	if path == "" {
		fmt.Fprintln(p.Stdout, "[]")
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(p.Stderr, "fakeclaude: agents: %v\n", err)
		return 1
	}
	_, _ = p.Stdout.Write(data)
	return 0
}

func (p Process) invocation(args []string) Invocation {
	inv := Invocation{Kind: "invocation", Program: p.Args[0], Args: args, Cwd: p.Dir, PID: os.Getpid(), Env: map[string]string{}}
	if inv.Args == nil {
		inv.Args = []string{}
	}
	for _, kv := range p.Environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && (strings.HasPrefix(k, "LYNA_TMUX_") || strings.HasPrefix(k, "CLAUDE_CODE_") ||
			k == "CLAUDE_CONFIG_DIR" || k == "TMUX" || k == "TMUX_PANE") {
			inv.Env[k] = v
		}
	}
	value, ok := flagValue(args, "--settings")
	if !ok {
		return inv
	}
	data := []byte(value)
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		inv.SettingsPath = value
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, path)
		}
		var err error
		if data, err = os.ReadFile(path); err != nil {
			inv.SettingsError = err.Error()
			return inv
		}
	}
	if !json.Valid(data) {
		inv.SettingsError = "settings are not valid JSON"
		return inv
	}
	inv.Settings = json.RawMessage(data)
	return inv
}

// flagValue returns the value of a long option given as --name=value or as
// --name value, stopping at "--".
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == want {
			return true
		}
	}
	return false
}

// settingsHooks is the hooks part of a settings document.
type settingsHooks struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string  `json:"type"`
			Command string  `json:"command"`
			Timeout float64 `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func (p Process) runHooks(inv Invocation) int {
	events := p.getenv(EnvHooks)
	if events == "" {
		return 0
	}
	var doc settingsHooks
	if len(inv.Settings) > 0 {
		if err := json.Unmarshal(inv.Settings, &doc); err != nil {
			fmt.Fprintf(p.Stderr, "fakeclaude: settings hooks: %v\n", err)
			return 1
		}
	}
	for _, event := range strings.Split(events, ",") {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		input, subject := p.hookInput(event, inv)
		for _, group := range doc.Hooks[event] {
			if subject != nil && !MatcherMatches(group.Matcher, *subject) {
				continue
			}
			for _, h := range group.Hooks {
				if h.Type != "command" {
					continue
				}
				timeout := hookTimeout
				if h.Timeout > 0 {
					timeout = time.Duration(h.Timeout * float64(time.Second))
				}
				run := p.runHook(event, group.Matcher, h.Command, input, timeout)
				if err := p.record(run); err != nil {
					fmt.Fprintf(p.Stderr, "fakeclaude: record: %v\n", err)
					return 1
				}
			}
		}
	}
	return 0
}

// hookInput builds the documented stdin payload for event and returns the
// value its matcher filters on, or nil when the event has no matcher.
func (p Process) hookInput(event string, inv Invocation) (json.RawMessage, *string) {
	sessionID := p.sessionID()
	tool := p.getenv(EnvTool)
	if tool == "" {
		tool = DefaultTool
	}
	permissionMode, ok := flagValue(inv.Args, "--permission-mode")
	if !ok {
		permissionMode = "default"
	}
	configDir := p.getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		configDir = filepath.Join(p.getenv("HOME"), ".claude")
	}
	in := map[string]any{
		"session_id":      sessionID,
		"transcript_path": filepath.Join(configDir, "projects", "fake", sessionID+".jsonl"),
		"cwd":             p.Dir,
		"permission_mode": permissionMode,
		"hook_event_name": event,
	}
	var subject *string
	set := func(s string) { subject = &s }
	switch event {
	case "SessionStart":
		in["source"] = "startup"
		in["model"] = "claude-opus-5"
		set("startup")
	case "UserPromptSubmit":
		in["prompt"] = "fake prompt"
	case "PreToolUse", "PermissionRequest":
		in["tool_name"] = tool
		in["tool_input"] = map[string]any{}
		in["tool_use_id"] = "toolu_fake"
		set(tool)
	case "PostToolUse":
		in["tool_name"] = tool
		in["tool_input"] = map[string]any{}
		in["tool_response"] = map[string]any{}
		in["tool_use_id"] = "toolu_fake"
		set(tool)
	case "Notification":
		in["message"] = "Claude needs your permission"
		in["title"] = "Permission needed"
		in["notification_type"] = "permission_prompt"
		set("permission_prompt")
	case "SubagentStart":
		in["agent_id"] = "agent-fake"
		in["agent_type"] = "Explore"
		set("Explore")
	case "SubagentStop":
		in["agent_id"] = "agent-fake"
		in["agent_type"] = "Explore"
		in["stop_hook_active"] = false
		in["agent_transcript_path"] = filepath.Join(configDir, "projects", "fake", sessionID, "subagents", "agent-fake.jsonl")
		in["last_assistant_message"] = "done"
		set("Explore")
	case "Stop":
		in["stop_hook_active"] = false
		in["last_assistant_message"] = "done"
	case "SessionEnd":
		in["reason"] = "other"
		set("other")
	}
	data, _ := json.Marshal(in)
	return data, subject
}

// runHook runs a hook command through `sh -c`, as Claude Code does, with the
// payload on stdin. The command comes from the settings file under test.
func (p Process) runHook(event, matcher, command string, input json.RawMessage, timeout time.Duration) HookRun {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = p.Dir
	cmd.Env = append(append([]string(nil), p.Environ...), "CLAUDE_PROJECT_DIR="+p.Dir)
	cmd.Stdin = strings.NewReader(string(input))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	run := HookRun{Kind: "hook", Event: event, Matcher: matcher, Command: command, Input: input, Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		run.ExitCode = exitErr.ExitCode()
	case err != nil:
		run.ExitCode = -1
		run.Error = err.Error()
	}
	return run
}

var exactMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]+$`)

// MatcherMatches applies a hook matcher to a value the way the hooks
// reference describes: "", "*" match everything; a matcher of letters,
// digits, '_', '-', spaces, ',' and '|' is a list of exact names; anything
// else is an unanchored regular expression.
func MatcherMatches(matcher, value string) bool {
	switch {
	case matcher == "" || matcher == "*":
		return true
	case exactMatcher.MatchString(matcher):
		for _, name := range strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' }) {
			if strings.TrimSpace(name) == value {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	return err == nil && re.MatchString(value)
}

// record appends v as one JSON line to the record file, under an exclusive
// lock so concurrent fakes never interleave lines.
func (p Process) record(v any) error {
	path := p.getenv(EnvRecord)
	if path == "" {
		return nil
	}
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	return errors.Join(werr, f.Close())
}
