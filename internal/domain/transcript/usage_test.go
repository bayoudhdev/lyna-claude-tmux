package transcript

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// assistant is one line of an assistant message as Claude Code writes it,
// with the usage the message has streamed so far.
func assistant(id string, in, cacheWrite, cacheRead, out int64, at string) string {
	usage := `{"input_tokens":` + strconv.FormatInt(in, 10) +
		`,"cache_creation_input_tokens":` + strconv.FormatInt(cacheWrite, 10) +
		`,"cache_read_input_tokens":` + strconv.FormatInt(cacheRead, 10) +
		`,"output_tokens":` + strconv.FormatInt(out, 10) + `,"service_tier":"standard"}`
	msg := `{"id":"` + id + `","type":"message","role":"assistant","model":"claude-opus-5",` +
		`"content":[{"type":"text","text":"ok"}],"usage":` + usage + `}`
	if id == "" {
		msg = `{"type":"message","role":"assistant","content":[],"usage":` + usage + `}`
	}
	return `{"parentUuid":null,"isSidechain":false,"type":"assistant","message":` + msg +
		`,"uuid":"u-` + id + `","timestamp":"` + at + `"}` + "\n"
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMeterTally(t *testing.T) {
	const t1, t2, t3 = "2026-09-15T12:00:01.000Z", "2026-09-15T12:00:02.000Z", "2026-09-15T12:00:03.000Z"
	cases := []struct {
		name  string
		limit int
		in    string
		want  Tally
	}{
		{name: "nothing", in: ""},
		{
			name: "one message written over three lines counts once, as its last line says",
			in:   assistant("m1", 3, 100, 1000, 8, t1) + assistant("m1", 3, 100, 1000, 40, t2) + assistant("m1", 3, 100, 1000, 95, t3),
			want: Tally{
				Sum:  Usage{Input: 3, CacheWrite: 100, CacheRead: 1000, Output: 95},
				Last: Usage{Input: 3, CacheWrite: 100, CacheRead: 1000, Output: 95}, LastAt: at(t3),
			},
		},
		{
			name: "messages of two agents interleaved",
			in:   assistant("m1", 1, 0, 10, 5, t1) + assistant("m2", 2, 0, 20, 7, t2) + assistant("m1", 1, 0, 10, 9, t3),
			want: Tally{
				Sum:  Usage{Input: 3, CacheRead: 30, Output: 16},
				Last: Usage{Input: 1, CacheRead: 10, Output: 9}, LastAt: at(t3),
			},
		},
		{
			name: "a message without an id counts line by line",
			in:   assistant("", 1, 0, 0, 4, t1) + assistant("", 1, 0, 0, 4, t2),
			want: Tally{Sum: Usage{Input: 2, Output: 8}, Last: Usage{Input: 1, Output: 4}, LastAt: at(t2)},
		},
		{
			name: "a prompt carrying a usage is not an assistant message",
			in:   strings.Replace(assistant("m1", 1, 1, 1, 1, t1), `"type":"assistant"`, `"type":"user"`, 1),
		},
		{
			name: "an assistant message without a usage",
			in:   `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"usage"}]}}` + "\n",
		},
		{
			name: "a usage of the wrong shape",
			in:   `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":"many"}}}` + "\n" + assistant("m2", 1, 0, 0, 1, t1),
			want: Tally{Sum: Usage{Input: 1, Output: 1}, Last: Usage{Input: 1, Output: 1}, LastAt: at(t1)},
		},
		{
			name: "negative counts read as none",
			in:   assistant("m1", -5, -1, 10, -3, t1),
			want: Tally{Sum: Usage{CacheRead: 10}, Last: Usage{CacheRead: 10}, LastAt: at(t1)},
		},
		{
			name: "a message with no time",
			in:   assistant("m1", 1, 0, 0, 1, "yesterday"),
			want: Tally{Sum: Usage{Input: 1, Output: 1}, Last: Usage{Input: 1, Output: 1}},
		},
		{
			name: "a line not ended yet is not counted",
			in:   assistant("m1", 1, 0, 0, 1, t1) + strings.TrimSuffix(assistant("m2", 5, 0, 0, 5, t2), "\n"),
			want: Tally{Sum: Usage{Input: 1, Output: 1}, Last: Usage{Input: 1, Output: 1}, LastAt: at(t1)},
		},
		{
			name: "a line over the limit is dropped and the next one counted", limit: 400,
			in:   strings.Replace(assistant("m1", 9, 0, 0, 9, t1), `"ok"`, `"`+strings.Repeat("x", 400)+`"`, 1) + assistant("m2", 1, 0, 0, 1, t2),
			want: Tally{Sum: Usage{Input: 1, Output: 1}, Last: Usage{Input: 1, Output: 1}, LastAt: at(t2)},
		},
		{
			name: "counts past any sane size stop at the largest",
			in:   assistant("m1", math.MaxInt64, 0, 0, 0, t1) + assistant("m2", math.MaxInt64, 0, 0, 0, t2),
			want: Tally{Sum: Usage{Input: math.MaxInt64}, Last: Usage{Input: math.MaxInt64}, LastAt: at(t2)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Meter{lines: Lines{limit: tc.limit}}
			if n, err := m.Write([]byte(tc.in)); n != len(tc.in) || err != nil {
				t.Fatalf("Write = %d, %v", n, err)
			}
			if got := m.Tally(); !sameTally(got, tc.want) {
				t.Fatalf("Tally\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestMeterSession tallies a whole session the way Claude Code writes one:
// thinking, text and a tool call streamed as three lines of one message,
// prompts, tool results, a compaction marker and the file history.
func TestMeterSession(t *testing.T) {
	data, err := os.ReadFile("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := Tally{
		Sum:    Usage{Input: 10, CacheWrite: 2550, CacheRead: 49500, Output: 275},
		Last:   Usage{Input: 2, CacheWrite: 150, CacheRead: 17400, Output: 60},
		LastAt: at("2026-09-15T11:58:20.000Z"),
	}
	cases := []struct {
		name  string
		piece int
	}{
		{name: "whole", piece: len(data)},
		{name: "a byte at a time", piece: 1},
		{name: "in reads of 7 bytes", piece: 7},
		{name: "in reads of 1000 bytes", piece: 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m Meter
			for rest := data; len(rest) > 0; {
				n := min(tc.piece, len(rest))
				_, _ = m.Write(rest[:n])
				rest = rest[n:]
			}
			if got := m.Tally(); !sameTally(got, want) {
				t.Fatalf("Tally\n got %+v\nwant %+v", got, want)
			}
		})
	}
	if got := want.Last.Context(); got != 17552 {
		t.Fatalf("the context of the last message is %d", got)
	}
}

func TestUsageSums(t *testing.T) {
	cases := []struct {
		name           string
		u              Usage
		total, context int64
	}{
		{name: "nothing", u: Usage{}},
		{name: "every part", u: Usage{Input: 1, Output: 2, CacheRead: 30, CacheWrite: 400}, total: 433, context: 431},
		{name: "output is not context", u: Usage{Output: 9}, total: 9},
		{
			name:  "a sum past the largest count stops there",
			u:     Usage{Input: math.MaxInt64, Output: 1, CacheRead: math.MaxInt64, CacheWrite: 1},
			total: math.MaxInt64, context: math.MaxInt64,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.Total(); got != tc.total {
				t.Fatalf("Total = %d, want %d", got, tc.total)
			}
			if got := tc.u.Context(); got != tc.context {
				t.Fatalf("Context = %d, want %d", got, tc.context)
			}
		})
	}
}

// sameTally compares two tallies, their times as instants.
func sameTally(a, b Tally) bool {
	return a.Sum == b.Sum && a.Last == b.Last && a.LastAt.Equal(b.LastAt)
}
