package transcript

import (
	"bytes"
	"encoding/json"
	"math"
	"time"
)

// Usage is a count of tokens, split the way the model reports them.
type Usage struct {
	// Input is the prompt read without the cache, CacheRead the prompt read
	// from it and CacheWrite the prompt written to it.
	Input, CacheRead, CacheWrite int64
	// Output is what the model wrote.
	Output int64
}

// Total is every token of the count.
func (u Usage) Total() int64 {
	return addTokens(addTokens(u.Input, u.Output), addTokens(u.CacheRead, u.CacheWrite))
}

// Context is the prompt of the count, read and written alike. Of the latest
// message, it is how full the agent's context is.
func (u Usage) Context() int64 {
	return addTokens(u.Input, addTokens(u.CacheRead, u.CacheWrite))
}

// plus adds two counts.
func (u Usage) plus(v Usage) Usage {
	return Usage{
		Input:      addTokens(u.Input, v.Input),
		CacheRead:  addTokens(u.CacheRead, v.CacheRead),
		CacheWrite: addTokens(u.CacheWrite, v.CacheWrite),
		Output:     addTokens(u.Output, v.Output),
	}
}

// addTokens adds two counts that are never negative, stopping at the largest
// one instead of wrapping around. A count read from a file is only as sane as
// the file, and a sum that wrapped would draw as a negative number.
func addTokens(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// Tally is what a transcript says an agent has spent.
type Tally struct {
	// Sum adds up every assistant message of the transcript.
	Sum Usage
	// Last is the latest assistant message alone, whose Context is how full
	// the agent's context is now, and LastAt when it was written.
	Last   Usage
	LastAt time.Time
}

// Meter tallies the usage of a transcript while it grows. Write feeds it the
// bytes the file gained since the last write, in any pieces; the zero value is
// ready to use. It is not safe for concurrent use.
type Meter struct {
	lines Lines
	// byID is the usage of every message that has an id. Claude Code writes
	// one line per content block of a message, every one of them with the
	// usage of the whole message as far as it has streamed, so a message is
	// counted once, with the usage of the last of its lines.
	byID map[string]Usage
	// loose adds up the messages that carry no id, each line on its own.
	loose  Usage
	last   Usage
	lastAt time.Time
}

// Write feeds the meter the next bytes of the transcript. It never fails: a
// line that is not an assistant message with a usage is not counted.
func (m *Meter) Write(p []byte) (int, error) {
	m.lines.Feed(p, m.line)
	return len(p), nil
}

// Tally is what the lines fed so far add up to. A line the last write did not
// end is not in it yet.
func (m *Meter) Tally() Tally {
	sum := m.loose
	for _, u := range m.byID {
		sum = sum.plus(u)
	}
	return Tally{Sum: sum, Last: m.last, LastAt: m.lastAt}
}

// usageKey is in every line that carries a usage. Most lines of a transcript
// are tool results and prompts, which the meter has no use for; looking for
// the key first spares decoding them.
var usageKey = []byte(`"usage"`)

// meterLine is the part of a transcript line the meter reads.
type meterLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string     `json:"id"`
		Usage *usageJSON `json:"usage"`
	} `json:"message"`
}

// usageJSON is a usage as the model reports it.
type usageJSON struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
}

func (u usageJSON) usage() Usage {
	return Usage{
		Input: max(u.Input, 0), Output: max(u.Output, 0),
		CacheWrite: max(u.CacheWrite, 0), CacheRead: max(u.CacheRead, 0),
	}
}

func (m *Meter) line(b []byte) {
	if !bytes.Contains(b, usageKey) {
		return
	}
	var l meterLine
	if json.Unmarshal(b, &l) != nil || l.Type != typeAssistant || l.Message.Usage == nil {
		return
	}
	u := l.Message.Usage.usage()
	if id := l.Message.ID; id != "" {
		if m.byID == nil {
			m.byID = make(map[string]Usage)
		}
		m.byID[id] = u
	} else {
		m.loose = m.loose.plus(u)
	}
	m.last, m.lastAt = u, parseTime(l.Timestamp)
}

// parseTime reads the timestamp of a line, zero when it has none.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
