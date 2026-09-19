// Package team reads the state Claude Code keeps for an agent team: who the
// teammates are, what each one is, and which pane it runs in.
//
// Everything here is a parser. The package opens no file and writes nothing:
// the files belong to Claude Code, which rewrites them on every change of the
// team, so lyna-tmux reads them and never edits them.
package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Name returns the directory name Claude Code derives from a session id: the
// word "session" and the first eight characters of the id. An id shorter than
// that is used whole, which is what the product does.
func Name(sessionID string) string {
	const short = 8
	if len(sessionID) > short {
		sessionID = sessionID[:short]
	}
	return "session-" + sessionID
}

// Backend is where a member's session runs.
type Backend string

// Backends Claude Code records. An unknown value is kept as it is read, so a
// backend added after this release is reported rather than hidden.
const (
	// BackendInProcess runs the agent inside the session that spawned it.
	BackendInProcess Backend = "in-process"
	// BackendTmux runs the agent in a tmux pane.
	BackendTmux Backend = "tmux"
	// BackendITerm2 runs the agent in a terminal split.
	BackendITerm2 Backend = "iterm2"
)

// LeadPane is what Claude Code writes as the pane of a lead that runs in the
// terminal it was started in, in place of a pane id.
const LeadPane = "leader"

// LeadType is the agent type of the entry that stands for the lead.
const LeadType = "team-lead"

// SwarmSocket is the start of the tmux socket name Claude Code builds a server
// of its own on, when a team opens on the tmux backend from a terminal that is
// not already inside tmux. It is a server lyna-tmux never created, so nothing
// here ever sends it a command, reads it or ends it: the name is known only so
// that it can be left alone.
const SwarmSocket = "claude-swarm"

// MaxConfigSize bounds a team configuration. A few members of a few hundred
// bytes each, plus the prompt of every one, stay far below it; anything past
// it is not a file this product wrote.
const MaxConfigSize = 1 << 20

// Errors this package reports on its own.
var (
	// ErrTooLarge reports a file past the size its kind allows.
	ErrTooLarge = errors.New("team: file is too large")
	// ErrNoTaskID reports a task file with no identifier, which nothing can
	// claim, block or complete.
	ErrNoTaskID = errors.New("team: task has no identifier")
)

// Member is one agent of the team.
type Member struct {
	AgentID   string
	Name      string
	AgentType string
	Color     string
	Model     string
	// Dir is the working directory the agent was started in.
	Dir string
	// Pane is the pane the agent runs in, as Claude Code recorded it. It is
	// LeadPane for an in-process lead and empty for a backend that has no
	// pane, so callers ask TmuxPane rather than reading it.
	Pane string
	// Backend is how the agent runs.
	Backend Backend
	// Active is Claude Code's own flag for a teammate that is still working.
	// It is absent for a lead, which is why it is not the state the sidebar
	// shows; that comes from the hooks.
	Active bool
	// Lead is set for the entry that stands for the session that spawned the
	// team.
	Lead     bool
	JoinedAt time.Time
}

// TmuxPane returns the tmux pane the member runs in and whether it has one. A
// lead running in the terminal records the word "leader", and a backend that
// is not tmux records nothing, so neither answers a pane.
func (m Member) TmuxPane() (string, bool) {
	if m.Backend != BackendTmux || !strings.HasPrefix(m.Pane, "%") {
		return "", false
	}
	return m.Pane, true
}

// Config is the team file.
type Config struct {
	Name          string
	LeadAgentID   string
	LeadSessionID string
	CreatedAt     time.Time
	Members       []Member
}

// Teammates returns every member that is not the lead, in file order.
func (c Config) Teammates() []Member {
	out := make([]Member, 0, len(c.Members))
	for _, m := range c.Members {
		if !m.Lead {
			out = append(out, m)
		}
	}
	return out
}

// ByPane returns the member running in a tmux pane.
func (c Config) ByPane(pane string) (Member, bool) {
	if pane == "" {
		return Member{}, false
	}
	for _, m := range c.Members {
		if p, ok := m.TmuxPane(); ok && p == pane {
			return m, true
		}
	}
	return Member{}, false
}

// ByName returns the member with that name.
func (c Config) ByName(name string) (Member, bool) {
	for _, m := range c.Members {
		if m.Name == name {
			return m, true
		}
	}
	return Member{}, false
}

// wire is the file as Claude Code writes it. Fields it may add later are
// ignored by the decoder, and fields it leaves out stay zero, so a team from a
// newer release still reads.
type wire struct {
	Name          string       `json:"name"`
	CreatedAt     int64        `json:"createdAt"`
	LeadAgentID   string       `json:"leadAgentId"`
	LeadSessionID string       `json:"leadSessionId"`
	Members       []wireMember `json:"members"`
}

type wireMember struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	AgentType string `json:"agentType"`
	Color     string `json:"color"`
	Model     string `json:"model"`
	CWD       string `json:"cwd"`
	PaneID    string `json:"tmuxPaneId"`
	Backend   string `json:"backendType"`
	Active    bool   `json:"isActive"`
	JoinedAt  int64  `json:"joinedAt"`
}

// ParseConfig decodes a team configuration. A member with no name cannot be
// addressed by anything this product does, so it is dropped rather than shown
// as a row nobody can reach.
func ParseConfig(data []byte) (Config, error) {
	if len(data) > MaxConfigSize {
		return Config{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return Config{}, fmt.Errorf("decode team configuration: %w", err)
	}
	c := Config{
		Name:          w.Name,
		LeadAgentID:   w.LeadAgentID,
		LeadSessionID: w.LeadSessionID,
		CreatedAt:     millis(w.CreatedAt),
	}
	for _, m := range w.Members {
		if m.Name == "" {
			continue
		}
		c.Members = append(c.Members, Member{
			AgentID:   m.AgentID,
			Name:      m.Name,
			AgentType: m.AgentType,
			Color:     m.Color,
			Model:     m.Model,
			Dir:       m.CWD,
			Pane:      m.PaneID,
			Backend:   Backend(m.Backend),
			Active:    m.Active,
			Lead:      isLead(m, w.LeadAgentID),
			JoinedAt:  millis(m.JoinedAt),
		})
	}
	return c, nil
}

// isLead recognizes the lead by the identifier the file names as the lead, and
// by the agent type when the file names none, which is the case of a
// configuration written before the identifier was recorded.
func isLead(m wireMember, lead string) bool {
	if lead != "" {
		return m.AgentID == lead
	}
	return m.AgentType == LeadType
}

// millis converts a Claude Code timestamp, zero meaning it wrote none.
func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
