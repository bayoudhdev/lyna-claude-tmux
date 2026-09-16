// Package agent models Claude Code agents: the records printed by
// `claude agents --json`, the pane state written by lyna-tmux hooks, and the
// joined view shown by the picker and dashboard.
package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"
)

// Kind is the process kind reported by `claude agents --json`.
type Kind string

const (
	KindInteractive Kind = "interactive"
	KindBackground  Kind = "background"
)

// Status is the attention state of an agent.
type Status string

const (
	// StatusWaiting means the agent needs the user (permission prompt, question).
	StatusWaiting Status = "waiting"
	// StatusIdle means the agent finished its turn.
	StatusIdle Status = "idle"
	// StatusBusy means the agent is working.
	StatusBusy Status = "busy"
	// StatusUnknown means no signal is available yet.
	StatusUnknown Status = "unknown"
)

// ParseStatus maps a raw value (agents JSON, @lt_state pane option) to a
// Status. Anything unrecognized, including the empty string, is StatusUnknown.
func ParseStatus(s string) Status {
	switch Status(s) {
	case StatusWaiting, StatusIdle, StatusBusy:
		return Status(s)
	}
	return StatusUnknown
}

// Rank orders statuses for pickers: agents that need attention first, then
// finished ones, then working ones, then unknown.
func (s Status) Rank() int {
	switch s {
	case StatusWaiting:
		return 0
	case StatusIdle:
		return 1
	case StatusBusy:
		return 2
	}
	return 3
}

// JobState is the lifecycle state of a background job.
type JobState string

const (
	JobWorking JobState = "working"
	JobBlocked JobState = "blocked"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
	JobStopped JobState = "stopped"
)

// Finished reports whether the job reached a terminal state.
func (j JobState) Finished() bool {
	return j == JobDone || j == JobFailed || j == JobStopped
}

// Record is one entry of `claude agents --json`. Interactive sessions carry a
// PID; background jobs carry an ID and a State, and a PID only while their
// process is alive. Unknown fields are ignored so newer Claude versions keep
// decoding.
type Record struct {
	PID        int      `json:"pid,omitempty"`
	ID         string   `json:"id,omitempty"`
	CWD        string   `json:"cwd"`
	Kind       Kind     `json:"kind"`
	StartedAt  int64    `json:"startedAt"`
	SessionID  string   `json:"sessionId,omitempty"`
	Name       string   `json:"name,omitempty"`
	Status     string   `json:"status,omitempty"`
	WaitingFor string   `json:"waitingFor,omitempty"`
	State      JobState `json:"state,omitempty"`
}

// Started returns the start time.
func (r Record) Started() time.Time {
	return time.UnixMilli(r.StartedAt)
}

// Background reports whether the record is a background session or job.
func (r Record) Background() bool {
	return r.Kind == KindBackground
}

// EffectiveStatus derives the attention state. The live process status wins;
// a background job without a live process falls back to its job state.
func (r Record) EffectiveStatus() Status {
	if st := ParseStatus(r.Status); st != StatusUnknown {
		return st
	}
	switch r.State {
	case JobWorking:
		return StatusBusy
	case JobBlocked:
		return StatusWaiting
	}
	if r.State.Finished() {
		return StatusIdle
	}
	return StatusUnknown
}

// Key identifies a record across refreshes: the background job ID when
// present, otherwise the PID.
func (r Record) Key() string {
	if r.ID != "" {
		return "job:" + r.ID
	}
	return fmt.Sprintf("pid:%d", r.PID)
}

// ParseRecords decodes `claude agents --json` output. Records that carry
// neither a PID nor a job ID cannot be addressed and are dropped.
func ParseRecords(data []byte) ([]Record, error) {
	var raw []Record
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode claude agents json: %w", err)
	}
	out := raw[:0]
	for _, r := range raw {
		if r.PID <= 0 && r.ID == "" {
			continue
		}
		if r.PID < 0 {
			r.PID = 0
		}
		out = append(out, r)
	}
	return out, nil
}

// Location is where an agent runs on the lyna-tmux server.
type Location struct {
	Session     string
	WindowIndex int
	WindowName  string
	PaneID      string
	TTY         string
}

// Source tells how an agent was discovered.
type Source string

const (
	// SourceManaged is a Claude pane launched by lyna-tmux.
	SourceManaged Source = "managed"
	// SourcePane is a Claude process found in another pane of the server.
	SourcePane Source = "pane"
	// SourceBackground is a background session or job with no pane.
	SourceBackground Source = "background"
	// SourceExternal is an interactive session outside the lyna-tmux server.
	SourceExternal Source = "external"
)

// Agent is the joined view: the Claude record, the pane it runs in (nil when
// none) and the effective status.
type Agent struct {
	Record   Record
	Location *Location
	Source   Source
	Status   Status
}

// Title returns the display name: the session name when set, else the
// directory name of the working directory.
func (a Agent) Title() string {
	if a.Record.Name != "" {
		return a.Record.Name
	}
	if a.Record.CWD == "" {
		return ""
	}
	return filepath.Base(a.Record.CWD)
}

// Sort orders agents by status rank, then oldest first, then key, so the
// order is stable across refreshes.
func Sort(agents []Agent) {
	slices.SortStableFunc(agents, func(a, b Agent) int {
		return cmp.Or(
			cmp.Compare(a.Status.Rank(), b.Status.Rank()),
			cmp.Compare(a.Record.StartedAt, b.Record.StartedAt),
			cmp.Compare(a.Record.Key(), b.Record.Key()),
		)
	})
}
