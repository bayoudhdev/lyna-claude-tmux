package statusline

import (
	"encoding/json"
	"errors"
	"strconv"
)

// MaxStdinBytes caps the session JSON read from stdin.
const MaxStdinBytes = 1 << 20

// Status is the part of the session JSON Claude Code sends to a statusLine
// command that the line shows. Unknown fields are ignored.
type Status struct {
	Cwd           string        `json:"cwd"`
	Model         Model         `json:"model"`
	Workspace     Workspace     `json:"workspace"`
	OutputStyle   OutputStyle   `json:"output_style"`
	Cost          Cost          `json:"cost"`
	ContextWindow ContextWindow `json:"context_window"`
	Effort        Effort        `json:"effort"`
	RateLimits    RateLimits    `json:"rate_limits"`
	Worktree      Worktree      `json:"worktree"`
}

// Model identifies the model in use.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Workspace holds the session directories.
type Workspace struct {
	CurrentDir  string `json:"current_dir"`
	ProjectDir  string `json:"project_dir"`
	GitWorktree string `json:"git_worktree"`
}

// OutputStyle names the active output style.
type OutputStyle struct {
	Name string `json:"name"`
}

// Cost is the client-side session cost estimate.
type Cost struct {
	TotalCostUSD Number `json:"total_cost_usd"`
}

// ContextWindow reports context usage; UsedPercentage is null early in a
// session.
type ContextWindow struct {
	UsedPercentage    Number `json:"used_percentage"`
	ContextWindowSize Number `json:"context_window_size"`
}

// Effort is the live reasoning effort; absent for models without one.
type Effort struct {
	Level string `json:"level"`
}

// RateLimits carries the subscription and spend limit windows; each may be
// absent independently.
type RateLimits struct {
	FiveHour   RateWindow `json:"five_hour"`
	SevenDay   RateWindow `json:"seven_day"`
	SpendLimit RateWindow `json:"spend_limit"`
}

// RateWindow is one limit window. ResetsAt is Unix epoch seconds.
type RateWindow struct {
	UsedPercentage Number `json:"used_percentage"`
	ResetsAt       Number `json:"resets_at"`
}

// Worktree describes an active worktree session.
type Worktree struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
}

// Number is a JSON number that Claude Code may send as null or leave out.
// Valid is false for those, and also for a value of another JSON type or out
// of float64 range, so a changed or broken field reads as absent rather than
// as zero.
type Number struct {
	Value float64
	Valid bool
}

// UnmarshalJSON implements json.Unmarshaler. It never fails: anything but a
// number leaves n invalid.
func (n *Number) UnmarshalJSON(data []byte) error {
	*n = Number{}
	// The decoder hands over one complete JSON value, so a leading minus
	// sign or digit means a number in JSON syntax, which ParseFloat accepts.
	if len(data) == 0 || data[0] != '-' && (data[0] < '0' || data[0] > '9') {
		return nil
	}
	v, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return nil
	}
	*n = Number{Value: v, Valid: true}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (n Number) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, n.Value, 'g', -1, 64), nil
}

// Decode parses the session JSON. A field whose JSON type differs from the
// documented one is skipped and the rest is kept, so a future schema change
// to one field does not blank the whole line: a Number reads as absent, and
// for any other field Decode returns the partial Status together with the
// *json.UnmarshalTypeError. Malformed JSON returns a zero Status.
func Decode(data []byte) (Status, error) {
	var s Status
	err := json.Unmarshal(data, &s)
	var typeErr *json.UnmarshalTypeError
	if err != nil && !errors.As(err, &typeErr) {
		return Status{}, err
	}
	return s, err
}
