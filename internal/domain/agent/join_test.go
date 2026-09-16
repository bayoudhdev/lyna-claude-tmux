package agent

import (
	"reflect"
	"testing"
)

func TestJoin(t *testing.T) {
	managed := Pane{ID: "%1", Session: "api", WindowIndex: 1, WindowName: "claude", TTY: "/dev/ttys001", PID: 100, Managed: true, Role: RoleClaude}
	shell := Pane{ID: "%2", Session: "api", WindowIndex: 2, WindowName: "shell", TTY: "/dev/ttys002", PID: 200, Managed: true}
	foreign := Pane{ID: "%3", Session: "scratch", WindowIndex: 0, WindowName: "zsh", TTY: "/dev/ttys003", PID: 300, Role: RoleClaude}

	loc := func(p Pane) *Location {
		return &Location{Session: p.Session, WindowIndex: p.WindowIndex, WindowName: p.WindowName, PaneID: p.ID, TTY: p.TTY}
	}

	tests := []struct {
		name    string
		records []Record
		panes   []Pane
		parents map[int]int
		want    []Agent
	}{
		{
			name:    "managed claude pane through its shell",
			records: []Record{{PID: 102, Kind: KindInteractive, Status: "busy"}},
			panes:   []Pane{managed},
			parents: map[int]int{102: 101, 101: 100, 100: 1},
			want:    []Agent{{Record: Record{PID: 102, Kind: KindInteractive, Status: "busy"}, Location: loc(managed), Source: SourceManaged, Status: StatusBusy}},
		},
		{
			name:    "process is the pane root",
			records: []Record{{PID: 100, Kind: KindInteractive, Status: "idle"}},
			panes:   []Pane{managed},
			parents: map[int]int{},
			want:    []Agent{{Record: Record{PID: 100, Kind: KindInteractive, Status: "idle"}, Location: loc(managed), Source: SourceManaged, Status: StatusIdle}},
		},
		{
			name:    "managed session without claude role is a plain pane",
			records: []Record{{PID: 201, Kind: KindInteractive}},
			panes:   []Pane{managed, shell},
			parents: map[int]int{201: 200},
			want:    []Agent{{Record: Record{PID: 201, Kind: KindInteractive}, Location: loc(shell), Source: SourcePane, Status: StatusUnknown}},
		},
		{
			name:    "claude role outside a managed session is a plain pane",
			records: []Record{{PID: 301, Kind: KindInteractive}},
			panes:   []Pane{foreign},
			parents: map[int]int{301: 300},
			want:    []Agent{{Record: Record{PID: 301, Kind: KindInteractive}, Location: loc(foreign), Source: SourcePane, Status: StatusUnknown}},
		},
		{
			name:    "no pane ancestor is external",
			records: []Record{{PID: 900, Kind: KindInteractive, Status: "waiting"}},
			panes:   []Pane{managed},
			parents: map[int]int{900: 800, 800: 1, 1: 0},
			want:    []Agent{{Record: Record{PID: 900, Kind: KindInteractive, Status: "waiting"}, Source: SourceExternal, Status: StatusWaiting}},
		},
		{
			name:    "unknown process is external",
			records: []Record{{PID: 900, Kind: KindInteractive}},
			panes:   []Pane{managed},
			parents: map[int]int{},
			want:    []Agent{{Record: Record{PID: 900, Kind: KindInteractive}, Source: SourceExternal, Status: StatusUnknown}},
		},
		{
			name:    "background record inside a pane stays background",
			records: []Record{{PID: 103, ID: "job-1", Kind: KindBackground, State: JobWorking}},
			panes:   []Pane{managed},
			parents: map[int]int{103: 100},
			want:    []Agent{{Record: Record{PID: 103, ID: "job-1", Kind: KindBackground, State: JobWorking}, Source: SourceBackground, Status: StatusBusy}},
		},
		{
			name:    "background job without process",
			records: []Record{{ID: "job-2", Kind: KindBackground, State: JobDone}},
			panes:   []Pane{managed},
			want:    []Agent{{Record: Record{ID: "job-2", Kind: KindBackground, State: JobDone}, Source: SourceBackground, Status: StatusIdle}},
		},
		{
			name:    "pid zero interactive record never matches a pane",
			records: []Record{{PID: 0, Kind: KindInteractive}},
			panes:   []Pane{{ID: "%9", PID: 0, Session: "x"}},
			parents: map[int]int{0: 0},
			want:    []Agent{{Record: Record{PID: 0, Kind: KindInteractive}, Source: SourceExternal, Status: StatusUnknown}},
		},
		{
			name:    "parent cycle terminates",
			records: []Record{{PID: 10, Kind: KindInteractive}},
			panes:   []Pane{managed},
			parents: map[int]int{10: 11, 11: 12, 12: 10},
			want:    []Agent{{Record: Record{PID: 10, Kind: KindInteractive}, Source: SourceExternal, Status: StatusUnknown}},
		},
		{
			name:    "self parented process terminates",
			records: []Record{{PID: 10, Kind: KindInteractive}},
			panes:   []Pane{managed},
			parents: map[int]int{10: 10},
			want:    []Agent{{Record: Record{PID: 10, Kind: KindInteractive}, Source: SourceExternal, Status: StatusUnknown}},
		},
		{
			name:    "duplicate pane pid keeps the first pane",
			records: []Record{{PID: 101, Kind: KindInteractive}},
			panes:   []Pane{managed, {ID: "%7", Session: "dup", PID: 100}},
			parents: map[int]int{101: 100},
			want:    []Agent{{Record: Record{PID: 101, Kind: KindInteractive}, Location: loc(managed), Source: SourceManaged, Status: StatusUnknown}},
		},
		{
			name:    "nearest pane wins over an outer pane",
			records: []Record{{PID: 302, Kind: KindInteractive}},
			panes:   []Pane{managed, foreign},
			parents: map[int]int{302: 300, 300: 100},
			want:    []Agent{{Record: Record{PID: 302, Kind: KindInteractive}, Location: loc(foreign), Source: SourcePane, Status: StatusUnknown}},
		},
		{
			name: "result is sorted waiting then idle then busy then unknown",
			records: []Record{
				{PID: 1, Kind: KindInteractive, StartedAt: 10},
				{PID: 2, Kind: KindInteractive, StartedAt: 20, Status: "busy"},
				{PID: 3, Kind: KindInteractive, StartedAt: 30, Status: "waiting"},
				{PID: 4, Kind: KindInteractive, StartedAt: 5, Status: "idle"},
			},
			want: []Agent{
				{Record: Record{PID: 3, Kind: KindInteractive, StartedAt: 30, Status: "waiting"}, Source: SourceExternal, Status: StatusWaiting},
				{Record: Record{PID: 4, Kind: KindInteractive, StartedAt: 5, Status: "idle"}, Source: SourceExternal, Status: StatusIdle},
				{Record: Record{PID: 2, Kind: KindInteractive, StartedAt: 20, Status: "busy"}, Source: SourceExternal, Status: StatusBusy},
				{Record: Record{PID: 1, Kind: KindInteractive, StartedAt: 10}, Source: SourceExternal, Status: StatusUnknown},
			},
		},
		{
			name: "no records",
			want: []Agent{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parents Parents
			if tt.parents != nil {
				parents = ParentMap(tt.parents)
			}
			got := Join(tt.records, tt.panes, parents)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Join() =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestJoinNilParentsStillMatchesPaneRoot(t *testing.T) {
	panes := []Pane{{ID: "%1", Session: "s", PID: 42}}
	got := Join([]Record{{PID: 42}, {PID: 43}}, panes, nil)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	bySource := map[int]Source{}
	for _, a := range got {
		bySource[a.Record.PID] = a.Source
	}
	if bySource[42] != SourcePane || bySource[43] != SourceExternal {
		t.Fatalf("sources = %v, want 42 pane and 43 external", bySource)
	}
}

func TestJoinAncestryBound(t *testing.T) {
	// A chain exactly MaxAncestry steps long reaches the pane at the last
	// check; one more level is beyond the bound and must not match.
	tests := []struct {
		name  string
		depth int
		want  Source
	}{
		{name: "within bound", depth: MaxAncestry - 1, want: SourcePane},
		{name: "beyond bound", depth: MaxAncestry, want: SourceExternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const root = 1_000_000
			parents := map[int]int{}
			leaf := root + tt.depth
			for pid := leaf; pid > root; pid-- {
				parents[pid] = pid - 1
			}
			got := Join([]Record{{PID: leaf}}, []Pane{{ID: "%1", PID: root}}, ParentMap(parents))
			if got[0].Source != tt.want {
				t.Fatalf("depth %d source = %q, want %q", tt.depth, got[0].Source, tt.want)
			}
		})
	}
}

func TestParentMap(t *testing.T) {
	p := ParentMap(map[int]int{5: 1})
	if ppid, ok := p(5); !ok || ppid != 1 {
		t.Fatalf("p(5) = %d, %v", ppid, ok)
	}
	if _, ok := p(6); ok {
		t.Fatal("p(6) reported a parent for an unknown pid")
	}
}
