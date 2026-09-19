package fakeclaude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Team mode. A lead started with EnvTeammates opens its teammates once its
// hooks ran, with the sequence the tmux backend of Claude Code 2.1.276 runs
// (read from the installed binary): the same tmux commands in the same order,
// the same command line in each pane, and the same team file. A test that
// passes here is a test of what lyna-tmux does with a real team.
//
// Where the product would build a tmux server of its own (a lead outside
// tmux), the fake keeps its teammates in process instead: it never creates,
// reads or signals a server it was not started in.

// Where the teammates of a team run, as the team file records it.
const (
	backendTmux      = "tmux"
	backendInProcess = "in-process"
)

// Values Claude Code writes for the lead of a team.
const (
	leadName = "team-lead"
	leadPane = "leader"
)

// teamPrompt is the prompt every teammate is recorded with.
const teamPrompt = "Work on the part of the task the lead gave you."

// paneCommand is what a teammate's pane runs until the teammate is started
// in it, as in the product: a program that holds the pane and prints nothing.
const paneCommand = "cat"

// palette is the order Claude Code hands out agent colors in. The lead takes
// the first one, so teammates start at the second.
var palette = []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"}

// tmuxColors maps an agent color to the color tmux draws it with.
var tmuxColors = map[string]string{
	"red": "red", "blue": "blue", "green": "green", "yellow": "yellow",
	"purple": "magenta", "orange": "colour208", "pink": "colour205", "cyan": "cyan",
}

// teammateName is the name Claude Code accepts for a teammate.
var teammateName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// forwarded are the variables a teammate's command line sets, beyond the two
// Claude Code always sets: the one it forwards that the fake reads, and the
// fake's own steering, so a teammate records where its lead does. The lead's
// own lifetime (EnvExit, EnvBlock) and its team are not a teammate's.
var forwarded = []string{"CLAUDE_CONFIG_DIR", EnvRecord, EnvHooks, EnvTool, EnvVersion, EnvAgents}

// TmuxRun is one tmux command the lead ran to open its team.
type TmuxRun struct {
	Kind string `json:"kind"` // "tmux"
	// Args is the whole argument vector after the tmux binary, the socket
	// option and its path first.
	Args     []string `json:"args"`
	ExitCode int      `json:"exitCode"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	Error    string   `json:"error,omitempty"`
}

// TeammateSpawn is one teammate the lead opened.
type TeammateSpawn struct {
	Kind      string `json:"kind"` // "teammate"
	Name      string `json:"name"`
	AgentType string `json:"agentType,omitempty"`
	Team      string `json:"team"`
	// Backend is "tmux" for a teammate given a pane, "in-process" for one kept
	// inside the lead.
	Backend string `json:"backend"`
	// Pane is the pane opened for the teammate, empty in process.
	Pane string `json:"pane,omitempty"`
	// Program is what the pane was respawned with: CLAUDE_CODE_TEAMMATE_COMMAND,
	// or the fake itself without one. Args are the arguments appended to it,
	// exactly as the teammate receives them, and Command the shell command line
	// both were handed to tmux in.
	Program string   `json:"program,omitempty"`
	Args    []string `json:"args,omitempty"`
	Command string   `json:"command,omitempty"`
}

// teammate is one entry of EnvTeammates.
type teammate struct {
	name, agentType string
}

// parseTeammates reads EnvTeammates: "name:type" entries separated by commas,
// with an empty type for a teammate spawned from no agent definition. A name
// Claude Code would refuse, or would rename, is an error: the test asked for a
// team the product never opens.
func parseTeammates(spec string) ([]teammate, error) {
	var out []teammate
	seen := map[string]bool{}
	for _, entry := range strings.Split(spec, ",") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		name, agentType, _ := strings.Cut(entry, ":")
		name, agentType = strings.TrimSpace(name), strings.TrimSpace(agentType)
		switch key := strings.ToLower(name); {
		case !teammateName.MatchString(name):
			return nil, fmt.Errorf("teammate name %q is not one Claude Code accepts", name)
		case key == strings.ToLower(leadName) || key == "main":
			return nil, fmt.Errorf("teammate name %q is reserved", name)
		case seen[key]:
			return nil, fmt.Errorf("teammate %q is given twice", name)
		default:
			seen[key] = true
		}
		out = append(out, teammate{name: name, agentType: agentType})
	}
	if len(out) == 0 {
		return nil, errors.New("no teammate named")
	}
	return out, nil
}

// isTeammate reports an invocation that is itself a teammate: Claude Code
// starts every one of them with its agent id, and a teammate opens no team.
func isTeammate(args []string) bool {
	_, ok := flagValue(args, "--agent-id")
	return ok
}

// teamName is the directory Claude Code names a session's team after.
func teamName(sessionID string) string {
	if len(sessionID) > 8 {
		sessionID = sessionID[:8]
	}
	return "session-" + sessionID
}

// teamMember is one member of the team file, in the shape Claude Code writes
// (internal/domain/team/testdata/config/team.json).
type teamMember struct {
	AgentID       string   `json:"agentId"`
	Name          string   `json:"name"`
	AgentType     string   `json:"agentType,omitempty"`
	Color         string   `json:"color,omitempty"`
	Model         string   `json:"model,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	JoinedAt      int64    `json:"joinedAt"`
	TmuxPaneID    string   `json:"tmuxPaneId"`
	CWD           string   `json:"cwd"`
	Subscriptions []string `json:"subscriptions"`
	BackendType   string   `json:"backendType"`
}

// teamFile is the team configuration Claude Code keeps at
// <config dir>/teams/<team>/config.json.
type teamFile struct {
	Name          string       `json:"name"`
	CreatedAt     int64        `json:"createdAt"`
	LeadAgentID   string       `json:"leadAgentId"`
	LeadSessionID string       `json:"leadSessionId"`
	Members       []teamMember `json:"members"`
}

// teamRun is one lead opening its teammates.
type teamRun struct {
	p    Process
	inv  Invocation
	path string
	doc  teamFile
	// tmux, socket and lead are the tmux binary, the server the lead runs on
	// and its pane; window is the lead's window, read once as the product
	// caches it.
	tmux, socket, lead, window string
}

// openTeam opens the teammates EnvTeammates names, when this invocation is a
// lead asked for them, and returns the exit status to end with, 0 to go on.
func (p Process) openTeam(inv Invocation) int {
	spec := p.getenv(EnvTeammates)
	if spec == "" || isTeammate(inv.Args) {
		return 0
	}
	mates, err := parseTeammates(spec)
	if err != nil {
		fmt.Fprintf(p.Stderr, "fakeclaude: %s: %v\n", EnvTeammates, err)
		return 2
	}
	// The team file is written, so it goes where the test put the Claude
	// configuration and nowhere else: never under a home directory.
	configDir := p.getenv("CLAUDE_CONFIG_DIR")
	if !filepath.IsAbs(configDir) {
		fmt.Fprintf(p.Stderr, "fakeclaude: %s needs an absolute CLAUDE_CONFIG_DIR (got %q)\n", EnvTeammates, configDir)
		return 2
	}
	t := &teamRun{p: p, inv: inv}
	if err := t.run(configDir, mates); err != nil {
		fmt.Fprintf(p.Stderr, "fakeclaude: team: %v\n", err)
		return 1
	}
	return 0
}

func (t *teamRun) run(configDir string, mates []teammate) error {
	sessionID := t.p.sessionID()
	name := teamName(sessionID)
	t.path = filepath.Join(configDir, "teams", name, "config.json")
	now := time.Now().UnixMilli()
	t.doc = teamFile{
		Name: name, CreatedAt: now, LeadAgentID: leadName + "@" + name, LeadSessionID: sessionID,
		Members: []teamMember{{
			AgentID: leadName + "@" + name, Name: leadName, AgentType: leadName, JoinedAt: now,
			TmuxPaneID: leadPane, CWD: t.p.Dir, Subscriptions: []string{}, BackendType: backendInProcess,
		}},
	}
	if err := t.write(); err != nil {
		return err
	}
	inTmux, err := t.inTmux()
	if err != nil {
		return err
	}
	for i, m := range mates {
		open := t.keep
		if inTmux {
			open = t.openPane
		}
		if err := open(m, palette[(i+1)%len(palette)]); err != nil {
			return fmt.Errorf("%s: %w", m.name, err)
		}
	}
	return nil
}

// inTmux reports whether the teammates get panes: the lead runs in a pane of
// a tmux server and was not told to keep its teammates in process.
func (t *teamRun) inTmux() (bool, error) {
	if t.teammateMode() == backendInProcess {
		return false, nil
	}
	socket, _, _ := strings.Cut(t.p.getenv("TMUX"), ",")
	t.lead = t.p.getenv("TMUX_PANE")
	if !filepath.IsAbs(socket) || !strings.HasPrefix(t.lead, "%") {
		return false, nil
	}
	bin, err := lookPath("tmux", t.p.getenv("PATH"))
	if err != nil {
		return false, err
	}
	t.tmux, t.socket = bin, socket
	return true, nil
}

// teammateMode is the backend the lead was configured with: the
// --teammate-mode flag, then the teammateMode setting, then auto.
func (t *teamRun) teammateMode() string {
	if mode, ok := flagValue(t.inv.Args, "--teammate-mode"); ok {
		return mode
	}
	var doc struct {
		TeammateMode string `json:"teammateMode"`
	}
	if len(t.inv.Settings) > 0 && json.Unmarshal(t.inv.Settings, &doc) == nil && doc.TeammateMode != "" {
		return doc.TeammateMode
	}
	return "auto"
}

// keep records a teammate that runs inside the lead.
func (t *teamRun) keep(m teammate, color string) error {
	t.add(m, color, backendInProcess, backendInProcess)
	if err := t.write(); err != nil {
		return err
	}
	return t.p.record(TeammateSpawn{Kind: "teammate", Name: m.name, AgentType: m.agentType, Team: t.doc.Name, Backend: backendInProcess})
}

// openPane opens a teammate the way the tmux backend does inside tmux: a pane
// split into the lead's window, styled and titled, the window tiled, the team
// file updated, then the teammate started in the pane by respawning it.
func (t *teamRun) openPane(m teammate, color string) error {
	if gate := t.p.getenv(EnvTeamGate); gate != "" {
		if _, err := t.tmuxRun("wait-for", gate); err != nil {
			return err
		}
	}
	if t.window == "" {
		out, err := t.tmuxRun("display-message", "-t", t.lead, "-p", "#{window_id}")
		if err != nil {
			return err
		}
		t.window = strings.TrimSpace(out)
	}
	panes, err := t.panes()
	if err != nil {
		return err
	}
	if len(panes) == 0 {
		return fmt.Errorf("window %s has no pane", t.window)
	}
	// The first teammate of a window the lead has to itself takes 70% of it
	// beside the lead. Every later one splits the middle pane of the ones
	// after the first, alternating the direction with their count.
	first := len(panes) == 1
	split := []string{"split-window", "-d", "-t", t.lead, "-h", "-l", "70%"}
	if !first {
		rest := panes[1:]
		dir := "-h"
		if len(rest)%2 == 1 {
			dir = "-v"
		}
		split = []string{"split-window", "-d", "-t", rest[(len(rest)-1)/2], dir}
	}
	out, err := t.tmuxRun(append(split, "-P", "-F", "#{pane_id}", "--", paneCommand)...)
	if err != nil {
		return err
	}
	pane := strings.TrimSpace(out)
	drawn := tmuxColors[color]
	for _, cmd := range [][]string{
		{"set-option", "-p", "-t", pane, "window-style", "bg=default,fg=" + drawn},
		{"set-option", "-p", "-t", pane, "pane-border-style", "fg=" + drawn},
		{"set-option", "-p", "-t", pane, "pane-active-border-style", "fg=" + drawn},
		{"select-pane", "-t", pane, "-T", m.name},
		{"set-option", "-p", "-t", pane, "pane-border-format", BorderFormat(color)},
	} {
		if _, err := t.tmuxRun(cmd...); err != nil {
			return err
		}
	}
	if err := t.rebalance(); err != nil {
		return err
	}
	t.add(m, color, pane, backendTmux)
	if err := t.write(); err != nil {
		return err
	}
	if first {
		if _, err := t.tmuxRun("set-option", "-w", "-t", t.window, "pane-border-status", "top"); err != nil {
			return err
		}
	}
	program, args := t.teammateCommand(m, color)
	command := t.commandLine(program, args)
	if _, err := t.tmuxRun("set-option", "-p", "-t", pane, "remain-on-exit", "failed"); err != nil {
		return err
	}
	if _, err := t.tmuxRun("respawn-pane", "-k", "-t", pane, "--", command); err != nil {
		return err
	}
	return t.p.record(TeammateSpawn{
		Kind: "teammate", Name: m.name, AgentType: m.agentType, Team: t.doc.Name, Backend: backendTmux,
		Pane: pane, Program: program, Args: args, Command: command,
	})
}

// BorderFormat is the pane-border-format the fake gives a teammate of color,
// which is the one Claude Code gives it: its title in its color.
func BorderFormat(color string) string {
	return "#[fg=" + tmuxColors[color] + ",bold] #{pane_title} #[default]"
}

// panes lists the lead's window in the order tmux numbers its panes.
func (t *teamRun) panes() ([]string, error) {
	out, err := t.tmuxRun("list-panes", "-t", t.window, "-F", "#{pane_id}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// rebalance tiles a window holding the lead and more than one other pane with
// the first pane in a column of 30%.
func (t *teamRun) rebalance() error {
	panes, err := t.panes()
	if err != nil || len(panes) <= 2 {
		return err
	}
	if _, err := t.tmuxRun("select-layout", "-t", t.window, "main-vertical"); err != nil {
		return err
	}
	_, err = t.tmuxRun("resize-pane", "-t", panes[0], "-x", "30%")
	return err
}

// teammateCommand is the program a teammate's pane runs and the arguments
// Claude Code appends to it, in its order.
func (t *teamRun) teammateCommand(m teammate, color string) (string, []string) {
	program := t.p.getenv("CLAUDE_CODE_TEAMMATE_COMMAND")
	if program == "" {
		program = t.p.self()
	}
	args := []string{
		"--agent-id", m.name + "@" + t.doc.Name, "--agent-name", m.name, "--team-name", t.doc.Name,
		"--agent-color", color, "--parent-session-id", t.doc.LeadSessionID,
	}
	if m.agentType != "" {
		args = append(args, "--agent-type", m.agentType)
	}
	mode, _ := flagValue(t.inv.Args, "--permission-mode")
	switch {
	case mode == "bypassPermissions" || hasArg(t.inv.Args, "--dangerously-skip-permissions"):
		args = append(args, "--dangerously-skip-permissions")
	case mode == "acceptEdits" || mode == "auto":
		args = append(args, "--permission-mode", mode)
	}
	for _, flag := range []string{"--effort", "--settings"} {
		if v, ok := flagValue(t.inv.Args, flag); ok {
			args = append(args, flag, v)
		}
	}
	for _, dir := range flagValues(t.inv.Args, "--plugin-dir") {
		args = append(args, "--plugin-dir", dir)
	}
	if model, ok := flagValue(t.inv.Args, "--model"); ok {
		args = append(args, "--model", model)
	}
	return program, args
}

// commandLine is the shell command a teammate's pane is respawned with: the
// lead's directory, the variables every teammate is given, then the program
// and its arguments, each one quoted as one word.
func (t *teamRun) commandLine(program string, args []string) string {
	env := []string{"CLAUDECODE=1", "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1"}
	for _, key := range forwarded {
		if v := t.p.getenv(key); v != "" {
			env = append(env, key+"="+shellWord(v))
		}
	}
	words := make([]string, 0, len(args)+1)
	words = append(words, shellWord(program))
	for _, a := range args {
		words = append(words, shellWord(a))
	}
	return "cd " + shellWord(t.p.Dir) + " && env " + strings.Join(env, " ") + " " + strings.Join(words, " ")
}

// add puts a teammate in the team file.
func (t *teamRun) add(m teammate, color, pane, backend string) {
	member := teamMember{
		AgentID: m.name + "@" + t.doc.Name, Name: m.name, AgentType: m.agentType, Color: color,
		Prompt: teamPrompt, JoinedAt: time.Now().UnixMilli(), TmuxPaneID: pane, CWD: t.p.Dir,
		Subscriptions: []string{}, BackendType: backend,
	}
	if model, ok := flagValue(t.inv.Args, "--model"); ok {
		member.Model = model
	}
	t.doc.Members = append(t.doc.Members, member)
}

// write replaces the team file in one rename, so a reader never sees half of
// it, the way the product's own writes are never read half done.
func (t *teamRun) write() error {
	data, err := json.MarshalIndent(t.doc, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), t.path)
}

// tmuxRun runs one tmux command on the lead's server, addressed by the socket
// path in $TMUX as the product addresses it, and records it. A command waits
// for as long as tmux takes: a wait-for gate is held by the test, and the lead
// gives up only when it is told to stop.
func (t *teamRun) tmuxRun(args ...string) (string, error) {
	argv := append([]string{"-S", t.socket}, args...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-t.p.Signal:
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, t.tmux, argv...)
	cmd.Env = t.p.Environ
	cmd.Dir = t.p.Dir
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	run := TmuxRun{Kind: "tmux", Args: argv, Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		run.ExitCode = exitErr.ExitCode()
	case err != nil:
		run.ExitCode = -1
		run.Error = err.Error()
	}
	if rerr := t.p.record(run); rerr != nil {
		return "", rerr
	}
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// sessionID is the session id the lead runs as.
func (p Process) sessionID() string {
	if id := p.getenv(EnvSessionID); id != "" {
		return id
	}
	return DefaultSessionID
}

// self is the fake's own path, which a teammate is started with when no
// launcher replaces it.
func (p Process) self() string {
	if p.Executable != "" {
		return p.Executable
	}
	return p.Args[0]
}

// lookPath finds name in the directories of path, as a shell finds a command.
// Relative directories are skipped: the lead's working directory is the
// project under test, never a place to run tmux from.
func lookPath(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not on PATH", name)
}

// flagValues returns every value of a repeatable long option, in order,
// stopping at "--".
func flagValues(args []string, name string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			out = append(out, v)
			continue
		}
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
			i++
		}
	}
	return out
}

// safeWord matches a word the shell reads as itself.
var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes s as one POSIX shell word, leaving a word the shell reads
// as itself bare.
func shellWord(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
