package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// SnapshotVersion is the wire version written by EncodeSnapshot. Decoders
// refuse any other version: a cache from a newer or older binary is simply
// rebuilt by the next refresh, which is cheaper than guessing its shape.
const SnapshotVersion = 1

// MaxSnapshotSize caps the encoded snapshot a decoder accepts. A few hundred
// agents encode to well under 1 MiB; the cap keeps a corrupted or planted cache
// file from exhausting memory in the picker's first frame.
const MaxSnapshotSize = 4 << 20

var (
	// ErrSnapshotVersion reports a snapshot written with another wire version
	// (or with none).
	ErrSnapshotVersion = errors.New("unsupported agents snapshot version")
	// ErrSnapshotTooLarge reports input over MaxSnapshotSize.
	ErrSnapshotTooLarge = errors.New("agents snapshot too large")
)

// Snapshot is the agent list cached between picker runs, so the picker can
// draw its first frame immediately and refresh in the background.
type Snapshot struct {
	// TakenAt is when the agents were listed. The wire format keeps
	// millisecond precision.
	TakenAt time.Time
	Agents  []Agent
}

// Fresh reports whether the snapshot was taken no more than maxAge before now.
// A snapshot stamped in the future (the clock moved backwards) is not fresh.
func (s Snapshot) Fresh(now time.Time, maxAge time.Duration) bool {
	if s.TakenAt.IsZero() {
		return false
	}
	age := now.Sub(s.TakenAt)
	return age >= 0 && age <= maxAge
}

// The wire structs pin JSON names explicitly so renaming a Go field never
// silently changes the cache format.
type snapshotWire struct {
	Version int         `json:"version"`
	TakenAt int64       `json:"takenAt"`
	Agents  []agentWire `json:"agents"`
}

type agentWire struct {
	Record   Record        `json:"record"`
	Location *locationWire `json:"location,omitempty"`
	Source   Source        `json:"source"`
	Status   Status        `json:"status"`
}

type locationWire struct {
	Session     string `json:"session"`
	WindowIndex int    `json:"windowIndex"`
	WindowName  string `json:"windowName"`
	PaneID      string `json:"paneId"`
	TTY         string `json:"tty"`
}

// EncodeSnapshot serializes a snapshot in the current wire version.
func EncodeSnapshot(s Snapshot) ([]byte, error) {
	w := snapshotWire{
		Version: SnapshotVersion,
		Agents:  make([]agentWire, 0, len(s.Agents)),
	}
	if !s.TakenAt.IsZero() {
		w.TakenAt = s.TakenAt.UnixMilli()
	}
	for _, a := range s.Agents {
		aw := agentWire{Record: a.Record, Source: a.Source, Status: a.Status}
		if l := a.Location; l != nil {
			aw.Location = &locationWire{
				Session:     l.Session,
				WindowIndex: l.WindowIndex,
				WindowName:  l.WindowName,
				PaneID:      l.PaneID,
				TTY:         l.TTY,
			}
		}
		w.Agents = append(w.Agents, aw)
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encode agents snapshot: %w", err)
	}
	return data, nil
}

// ReadSnapshot decodes a snapshot from r, reading at most MaxSnapshotSize+1
// bytes so oversized input is detected without buffering all of it.
func ReadSnapshot(r io.Reader) (Snapshot, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSnapshotSize+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read agents snapshot: %w", err)
	}
	return DecodeSnapshot(data)
}

// DecodeSnapshot parses a snapshot written by EncodeSnapshot. It refuses input
// over MaxSnapshotSize, other wire versions, unknown sources and records that
// cannot be addressed. Unrecognized status values decode as StatusUnknown.
func DecodeSnapshot(data []byte) (Snapshot, error) {
	if len(data) > MaxSnapshotSize {
		return Snapshot{}, fmt.Errorf("%w: %d bytes over %d", ErrSnapshotTooLarge, len(data), MaxSnapshotSize)
	}
	// Probe the version alone first: another version may use shapes this
	// decoder would misread or reject with a confusing type error.
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return Snapshot{}, fmt.Errorf("decode agents snapshot: %w", err)
	}
	if probe.Version != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("%w: %d", ErrSnapshotVersion, probe.Version)
	}
	var w snapshotWire
	if err := json.Unmarshal(data, &w); err != nil {
		return Snapshot{}, fmt.Errorf("decode agents snapshot: %w", err)
	}
	s := Snapshot{Agents: make([]Agent, 0, len(w.Agents))}
	if w.TakenAt != 0 {
		s.TakenAt = time.UnixMilli(w.TakenAt)
	}
	for i, aw := range w.Agents {
		a, err := aw.agent()
		if err != nil {
			return Snapshot{}, fmt.Errorf("decode agents snapshot: agent %d: %w", i, err)
		}
		s.Agents = append(s.Agents, a)
	}
	return s, nil
}

func (aw agentWire) agent() (Agent, error) {
	switch aw.Source {
	case SourceManaged, SourcePane, SourceBackground, SourceExternal:
	default:
		return Agent{}, fmt.Errorf("unknown source %q", aw.Source)
	}
	if aw.Record.PID < 0 || (aw.Record.PID == 0 && aw.Record.ID == "") {
		return Agent{}, errors.New("record has neither a pid nor a job id")
	}
	a := Agent{Record: aw.Record, Source: aw.Source, Status: ParseStatus(string(aw.Status))}
	if l := aw.Location; l != nil {
		a.Location = &Location{
			Session:     l.Session,
			WindowIndex: l.WindowIndex,
			WindowName:  l.WindowName,
			PaneID:      l.PaneID,
			TTY:         l.TTY,
		}
	}
	return a, nil
}
