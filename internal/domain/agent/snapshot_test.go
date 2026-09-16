package agent

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		TakenAt: time.UnixMilli(1789300000123),
		Agents: []Agent{
			{
				Record:   Record{PID: 5121, CWD: "/work/api", Kind: KindInteractive, StartedAt: 1785846401248, SessionID: "6f147909", Name: "api-main", Status: "waiting", WaitingFor: "permission prompt"},
				Location: &Location{Session: "api", WindowIndex: 1, WindowName: "claude", PaneID: "%3", TTY: "/dev/ttys004"},
				Source:   SourceManaged,
				Status:   StatusWaiting,
			},
			{
				Record: Record{ID: "job-2", CWD: "/work/api", Kind: KindBackground, StartedAt: 1789300000500, State: JobDone},
				Source: SourceBackground,
				Status: StatusIdle,
			},
		},
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Snapshot
		want Snapshot
	}{
		{name: "agents with and without location", in: sampleSnapshot(), want: sampleSnapshot()},
		{name: "empty list", in: Snapshot{TakenAt: time.UnixMilli(5)}, want: Snapshot{TakenAt: time.UnixMilli(5), Agents: []Agent{}}},
		{name: "zero time stays zero", in: Snapshot{}, want: Snapshot{Agents: []Agent{}}},
		{
			name: "sub millisecond precision is dropped",
			in:   Snapshot{TakenAt: time.UnixMilli(1000).Add(750 * time.Microsecond)},
			want: Snapshot{TakenAt: time.UnixMilli(1000), Agents: []Agent{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := EncodeSnapshot(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeSnapshot(data)
			if err != nil {
				t.Fatalf("DecodeSnapshot(%s): %v", data, err)
			}
			if !got.TakenAt.Equal(tt.want.TakenAt) || got.TakenAt.IsZero() != tt.want.TakenAt.IsZero() {
				t.Fatalf("TakenAt = %v, want %v", got.TakenAt, tt.want.TakenAt)
			}
			if !reflect.DeepEqual(got.Agents, tt.want.Agents) {
				t.Fatalf("Agents =\n%+v\nwant\n%+v", got.Agents, tt.want.Agents)
			}
		})
	}
}

func TestSnapshotWireFormat(t *testing.T) {
	data, err := EncodeSnapshot(Snapshot{
		TakenAt: time.UnixMilli(42),
		Agents: []Agent{{
			Record:   Record{PID: 7, CWD: "/w", Kind: KindInteractive, StartedAt: 1},
			Location: &Location{Session: "s", WindowIndex: 2, WindowName: "n", PaneID: "%1", TTY: "/dev/t"},
			Source:   SourcePane,
			Status:   StatusBusy,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":1,"takenAt":42,"agents":[{"record":{"pid":7,"cwd":"/w","kind":"interactive","startedAt":1},` +
		`"location":{"session":"s","windowIndex":2,"windowName":"n","paneId":"%1","tty":"/dev/t"},"source":"pane","status":"busy"}]}`
	if string(data) != want {
		t.Fatalf("wire =\n%s\nwant\n%s", data, want)
	}
}

func TestDecodeSnapshotErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr error
		wantMsg string
	}{
		{name: "not json", data: "nope", wantMsg: "decode agents snapshot"},
		{name: "missing version", data: `{"agents":[]}`, wantErr: ErrSnapshotVersion},
		{name: "future version", data: `{"version":2,"agents":{"new":"shape"}}`, wantErr: ErrSnapshotVersion},
		{name: "version with wrong type", data: `{"version":"1"}`, wantMsg: "decode agents snapshot"},
		{name: "agents with wrong type", data: `{"version":1,"agents":{}}`, wantMsg: "decode agents snapshot"},
		{name: "trailing data", data: `{"version":1}{}`, wantMsg: "decode agents snapshot"},
		{name: "unknown source", data: `{"version":1,"agents":[{"record":{"pid":1},"source":"tmux"}]}`, wantMsg: `agent 0: unknown source "tmux"`},
		{name: "missing source", data: `{"version":1,"agents":[{"record":{"pid":1}}]}`, wantMsg: `unknown source ""`},
		{name: "unaddressable record", data: `{"version":1,"agents":[{"record":{"cwd":"/x"},"source":"external"}]}`, wantMsg: "neither a pid nor a job id"},
		{name: "negative pid", data: `{"version":1,"agents":[{"record":{"pid":-4,"id":"j"},"source":"background"}]}`, wantMsg: "neither a pid nor a job id"},
		{name: "too large", data: `{"version":1}` + strings.Repeat(" ", MaxSnapshotSize), wantErr: ErrSnapshotTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeSnapshot([]byte(tt.data))
			if err == nil {
				t.Fatal("DecodeSnapshot succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestDecodeSnapshotNormalizesStatus(t *testing.T) {
	s, err := DecodeSnapshot([]byte(`{"version":1,"takenAt":0,"agents":[
		{"record":{"pid":1},"source":"external","status":"exploded","extra":true},
		{"record":{"id":"j"},"source":"background"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.TakenAt.IsZero() {
		t.Fatalf("TakenAt = %v, want zero", s.TakenAt)
	}
	for i, a := range s.Agents {
		if a.Status != StatusUnknown {
			t.Fatalf("agent %d status = %q, want unknown", i, a.Status)
		}
	}
}

func TestReadSnapshot(t *testing.T) {
	data, err := EncodeSnapshot(sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		src     func() *bytes.Reader
		wantErr error
		wantMsg string
	}{
		{name: "valid", src: func() *bytes.Reader { return bytes.NewReader(data) }},
		{
			name:    "oversized stream stops at the cap",
			src:     func() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{' '}, 2*MaxSnapshotSize)) },
			wantErr: ErrSnapshotTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.src()
			s, err := ReadSnapshot(r)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if unread := r.Len(); unread < MaxSnapshotSize-1 {
					t.Fatalf("reader consumed past the cap: %d bytes left", unread)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Agents) != 2 {
				t.Fatalf("agents = %d, want 2", len(s.Agents))
			}
		})
	}

	t.Run("read error", func(t *testing.T) {
		boom := errors.New("boom")
		_, err := ReadSnapshot(iotest.ErrReader(boom))
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})
}

func TestSnapshotFresh(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	tests := []struct {
		name    string
		takenAt time.Time
		maxAge  time.Duration
		want    bool
	}{
		{name: "zero time", takenAt: time.Time{}, maxAge: time.Hour, want: false},
		{name: "just taken", takenAt: now, maxAge: time.Second, want: true},
		{name: "at the limit", takenAt: now.Add(-time.Minute), maxAge: time.Minute, want: true},
		{name: "past the limit", takenAt: now.Add(-time.Minute - time.Millisecond), maxAge: time.Minute, want: false},
		{name: "from the future", takenAt: now.Add(time.Second), maxAge: time.Hour, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Snapshot{TakenAt: tt.takenAt}).Fresh(now, tt.maxAge); got != tt.want {
				t.Fatalf("Fresh = %v, want %v", got, tt.want)
			}
		})
	}
}

// FuzzDecodeSnapshot checks that decoding never panics, never accepts input
// over the cap, only yields known sources and statuses, and that anything it
// accepts survives an encode and decode round trip unchanged.
func FuzzDecodeSnapshot(f *testing.F) {
	seed, err := EncodeSnapshot(sampleSnapshot())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"version":1,"agents":[]}`))
	f.Add([]byte("{\"version\":1,\"takenAt\":-1,\"agents\":[{\"record\":{\"pid\":1,\"name\":\"\x1b]0;x\x07\"},\"source\":\"pane\",\"status\":\"busy\",\"location\":{}}]}"))
	f.Add([]byte(`{"version":2}`))
	f.Add([]byte(`{"version":1,"agents":[{"record":{"id":"j"},"source":"background","status":"waiting"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := DecodeSnapshot(data)
		if err != nil {
			return
		}
		if len(data) > MaxSnapshotSize {
			t.Fatalf("accepted %d bytes over the cap", len(data))
		}
		for _, a := range s.Agents {
			switch a.Source {
			case SourceManaged, SourcePane, SourceBackground, SourceExternal:
			default:
				t.Fatalf("decoded unknown source %q", a.Source)
			}
			if ParseStatus(string(a.Status)) != a.Status {
				t.Fatalf("decoded unnormalized status %q", a.Status)
			}
		}
		again, err := EncodeSnapshot(s)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		back, err := DecodeSnapshot(again)
		if err != nil {
			t.Fatalf("decode of re-encoded snapshot: %v\n%s", err, again)
		}
		if !back.TakenAt.Equal(s.TakenAt) || !reflect.DeepEqual(back.Agents, s.Agents) {
			t.Fatalf("round trip changed the snapshot:\n%+v\n%+v", s, back)
		}
	})
}
