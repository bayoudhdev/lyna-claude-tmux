package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/procx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

var (
	// ErrAgentNoPane reports an agent that runs in no pane of the tmux server
	// the picker looks at.
	ErrAgentNoPane = errors.New("agent runs in no pane of this tmux server")
	// ErrAgentNotJob reports an attach request for an agent that is not a
	// background job.
	ErrAgentNotJob = errors.New("only background agents attach")
	// ErrAgentIdentity reports an agent whose process can no longer be proven
	// to be the one that was listed.
	ErrAgentIdentity = errors.New("cannot verify the agent process")
)

// agentsPaneID matches the pane ids tmux prints (#{pane_id}); anything else
// in a cached snapshot is refused before it becomes a tmux target.
var agentsPaneID = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^%[0-9]{1,9}$`) })

// agentsJobID matches background job ids. The first character is never '-',
// so an id can never be read as an option by claude.
var agentsJobID = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`) })

// AgentsView lists the Claude agents for the picker and the dashboard and acts
// on them. Agents are located in the panes of the tmux server the process runs
// inside ($TMUX), or of the lyna-tmux server when it runs outside tmux.
type AgentsView struct {
	host   Host
	server *Server
	// panes is the client of the server whose panes locate agents.
	panes  *tmux.Client
	inside bool
	now    func() time.Time
}

// NewAgentsView returns the agents view for a host and its lyna-tmux server.
// now stamps snapshots; nil uses time.Now.
func NewAgentsView(h Host, s *Server, now func() time.Time) *AgentsView {
	if now == nil {
		now = time.Now
	}
	v := &AgentsView{host: h, server: s, panes: s.Client, now: now}
	if sock, ok := tmux.SocketFromEnv(h.Getenv("TMUX")); ok {
		// switch-client finds the client to move from TMUX_PANE in the
		// environment of the tmux command, which ServerEnviron removes.
		env := ServerEnviron(h.Environ)
		for _, key := range []string{"TMUX", "TMUX_PANE"} {
			if val := h.Getenv(key); val != "" {
				env = append(env, key+"="+val)
			}
		}
		v.panes = tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: sock, Env: env})
		v.inside = true
	}
	return v
}

// Inside reports whether the process runs inside a tmux server, whose client
// a jump then switches.
func (v *AgentsView) Inside() bool { return v.inside }

// Cached returns the snapshot saved by the last refresh, if it can be read.
func (v *AgentsView) Cached() (agent.Snapshot, bool) {
	snap, err := agentsReadCache(v.server.Paths.AgentsCache())
	return snap, err == nil
}

// agentsReadCache reads a snapshot without following a link at path and
// without reading past the snapshot size limit.
func agentsReadCache(path string) (agent.Snapshot, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return agent.Snapshot{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return agent.Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return agent.Snapshot{}, fmt.Errorf("%s: not a regular file", path)
	}
	return agent.ReadSnapshot(f)
}

// Refresh lists the agents now: the records of `claude agents --json`
// joined with the panes of the server and the process ancestry. The snapshot
// is saved for the next first frame.
func (v *AgentsView) Refresh(ctx context.Context) (agent.Snapshot, error) {
	records, err := v.records(ctx)
	if err != nil {
		return agent.Snapshot{}, err
	}
	panes, err := v.listPanes(ctx)
	if err != nil {
		return agent.Snapshot{}, err
	}
	// Without a process table (an unsupported platform) only agents that are
	// themselves a pane's root process are located.
	var parents agent.Parents
	if table, err := procx.Snapshot(); err == nil {
		parents = table.Parents()
	}
	snap := agent.Snapshot{TakenAt: v.now(), Agents: agent.Join(records, panes, parents)}
	data, err := agent.EncodeSnapshot(snap)
	if err != nil {
		return agent.Snapshot{}, err
	}
	if err := fsx.EnsurePrivateDir(v.server.Paths.Cache); err != nil {
		return agent.Snapshot{}, err
	}
	if err := fsx.WriteFileAtomic(v.server.Paths.AgentsCache(), data, fsx.PrivateFile); err != nil {
		return agent.Snapshot{}, fmt.Errorf("save agents snapshot: %w", err)
	}
	return snap, nil
}

func (v *AgentsView) claudeBin() (string, error) {
	return claude.ResolveCommand(v.server.Config.Claude.Command, v.host.lookPath(), v.host.Getenv, v.host.Home, claude.IsExecutable)
}

func (v *AgentsView) records(ctx context.Context) ([]agent.Record, error) {
	bin, err := v.claudeBin()
	if err != nil {
		return nil, err
	}
	c, err := claude.New(claude.Options{Bin: bin, Env: ServerEnviron(v.host.Environ)})
	if err != nil {
		return nil, err
	}
	return c.Agents(ctx)
}

// listPanes returns the panes of the server with the managed flag of their
// session. A server that is not running has none.
func (v *AgentsView) listPanes(ctx context.Context) ([]agent.Pane, error) {
	sessions, err := v.panes.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	managed := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		managed[s.ID] = s.Managed
	}
	panes, err := v.panes.ListPanes(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]agent.Pane, 0, len(panes))
	for _, p := range panes {
		out = append(out, agent.Pane{
			ID: p.ID, Session: p.SessionName, WindowIndex: p.WindowIndex, WindowName: p.WindowName,
			TTY: p.TTY, PID: p.PID, Managed: managed[p.SessionID], Role: p.Role,
		})
	}
	return out, nil
}

// agentPane returns the validated pane id of an agent.
func agentPane(a agent.Agent) (string, error) {
	if a.Location == nil {
		return "", fmt.Errorf("%s: %w", a.Title(), ErrAgentNoPane)
	}
	if !agentsPaneID().MatchString(a.Location.PaneID) {
		return "", fmt.Errorf("invalid pane id %q", a.Location.PaneID)
	}
	return a.Location.PaneID, nil
}

// Preview captures the visible content of the agent's pane. The text is
// untrusted; the picker sanitizes it.
func (v *AgentsView) Preview(ctx context.Context, a agent.Agent, _ int) (string, error) {
	pane, err := agentPane(a)
	if err != nil {
		return "", err
	}
	return v.panes.CapturePane(ctx, pane, 0)
}

// Jump brings the agent's pane into view. Inside a tmux server the calling
// client is switched to the pane's session and window and the pane is
// selected; nil is returned. Outside tmux the pane is selected on the
// lyna-tmux server and the returned command attaches this terminal to it.
func (v *AgentsView) Jump(ctx context.Context, a agent.Agent) (*Attach, error) {
	pane, err := agentPane(a)
	if err != nil {
		return nil, err
	}
	if v.inside {
		// A pane id target moves the client to the session and window that
		// hold the pane now, even after a rename or a move since the listing.
		_, err := v.panes.Batch(ctx,
			tmux.Command{"switch-client", "-t", pane},
			tmux.Command{"select-pane", "-t", pane},
		)
		return nil, agentsTargetErr(a, err)
	}
	name, err := v.server.Client.Display(ctx, pane, "#{session_name}")
	if err != nil {
		return nil, agentsTargetErr(a, err)
	}
	if _, err := v.server.Client.Batch(ctx,
		tmux.Command{"select-window", "-t", pane},
		tmux.Command{"select-pane", "-t", pane},
	); err != nil {
		return nil, agentsTargetErr(a, err)
	}
	att, err := v.server.AttachCommand(ctx, v.host, name)
	if err != nil {
		return nil, err
	}
	return &att, nil
}

// agentsTargetErr explains a pane that disappeared since the listing.
func agentsTargetErr(a agent.Agent, err error) error {
	if errors.Is(err, tmux.ErrNotFound) || errors.Is(err, tmux.ErrNoServer) {
		return fmt.Errorf("%s: its pane is gone; refresh the list: %w", a.Title(), ErrAgentNoPane)
	}
	return err
}

// AttachJob returns the command that opens a background agent in this
// terminal: `claude attach <id>`.
func (v *AgentsView) AttachJob(a agent.Agent) (Attach, error) {
	if a.Source != agent.SourceBackground || a.Record.ID == "" {
		return Attach{}, fmt.Errorf("%s: %w", a.Title(), ErrAgentNotJob)
	}
	if !agentsJobID().MatchString(a.Record.ID) {
		return Attach{}, fmt.Errorf("invalid background job id %q", a.Record.ID)
	}
	bin, err := v.claudeBin()
	if err != nil {
		return Attach{}, err
	}
	env := ServerEnviron(v.host.Environ)
	for _, key := range []string{"TMUX", "TMUX_PANE"} {
		if val := v.host.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return Attach{Argv: []string{bin, "attach", a.Record.ID}, Env: env}, nil
}

// Kill sends SIGTERM to the agent's process once it is proven to be the same
// Claude process that was listed.
func (v *AgentsView) Kill(a agent.Agent) error {
	return agentsKill(a.Record, procx.Read, procx.Kill)
}

// agentsKill verifies a record's process and signals it. claude stamps
// startedAt after its process started, and a PID is held by one live process
// at a time, so a live process with this PID that started no later than the
// stamp is the listed one; a reused PID starts after it. procx.Kill then pins
// the process by its exact start time and checks it is still Claude.
func agentsKill(r agent.Record, read func(pid int) (procx.Proc, error), kill func(pid int, start time.Time, sig syscall.Signal) error) error {
	if r.PID <= 1 {
		return fmt.Errorf("agent has no process to signal: %w", procx.ErrInvalidPID)
	}
	if r.StartedAt <= 0 {
		return fmt.Errorf("pid %d: no start time was listed: %w", r.PID, ErrAgentIdentity)
	}
	p, err := read(r.PID)
	if errors.Is(err, procx.ErrNotFound) {
		return fmt.Errorf("pid %d already exited: %w", r.PID, err)
	}
	if err != nil {
		return fmt.Errorf("pid %d: %w", r.PID, err)
	}
	if !procx.IsClaude(p) {
		return fmt.Errorf("pid %d: %w", r.PID, procx.ErrNotClaude)
	}
	if p.Start.UnixMilli() > r.StartedAt {
		return fmt.Errorf("pid %d: %w", r.PID, procx.ErrIdentityChanged)
	}
	return kill(r.PID, p.Start, syscall.SIGTERM)
}
