package agent

import (
	"testing"
	"time"
)

// sampleJSON mirrors the shape printed by `claude agents --json` (2.1.x):
// interactive entries, a live background job and a finished one.
const sampleJSON = `[
  {"pid": 5121, "cwd": "/work/api", "kind": "interactive", "startedAt": 1785846401248,
   "sessionId": "6f147909", "name": "api-main", "status": "waiting", "waitingFor": "permission prompt"},
  {"pid": 25186, "cwd": "/work/web", "kind": "interactive", "startedAt": 1789296877193,
   "sessionId": "d6c1718d", "status": "busy", "futureField": {"nested": true}},
  {"pid": 777, "id": "job-1", "cwd": "/work/api", "kind": "background", "startedAt": 1789300000000,
   "sessionId": "aa11", "name": "refactor", "status": "idle", "state": "blocked"},
  {"id": "job-2", "cwd": "/work/api", "kind": "background", "startedAt": 1789300000500,
   "sessionId": "bb22", "state": "done"},
  {"cwd": "/work/ghost", "kind": "interactive", "startedAt": 1}
]`

func TestParseRecords(t *testing.T) {
	recs, err := ParseRecords([]byte(sampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4 (unaddressable entry dropped)", len(recs))
	}
	cases := []struct {
		name       string
		rec        Record
		key        string
		status     Status
		background bool
	}{
		{"waiting interactive", recs[0], "pid:5121", StatusWaiting, false},
		{"busy interactive with unknown field", recs[1], "pid:25186", StatusBusy, false},
		{"live status beats job state", recs[2], "job:job-1", StatusIdle, true},
		{"finished job without process", recs[3], "job:job-2", StatusIdle, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.Key(); got != tc.key {
				t.Errorf("Key = %q, want %q", got, tc.key)
			}
			if got := tc.rec.EffectiveStatus(); got != tc.status {
				t.Errorf("EffectiveStatus = %q, want %q", got, tc.status)
			}
			if got := tc.rec.Background(); got != tc.background {
				t.Errorf("Background = %v, want %v", got, tc.background)
			}
		})
	}
	if recs[0].WaitingFor != "permission prompt" || recs[0].Name != "api-main" || recs[0].SessionID != "6f147909" {
		t.Fatalf("fields not decoded: %+v", recs[0])
	}
	if got := recs[0].Started(); !got.Equal(time.UnixMilli(1785846401248)) {
		t.Fatalf("Started = %v", got)
	}
}

func TestParseRecordsErrors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"empty array", `[]`, 0, false},
		{"negative pid with job id kept", `[{"pid":-1,"id":"j","kind":"background"}]`, 1, false},
		{"negative pid without id dropped", `[{"pid":-1,"kind":"interactive"}]`, 0, false},
		{"not json", `claude: command failed`, 0, true},
		{"object instead of array", `{"pid":1}`, 0, true},
		{"wrong field type", `[{"pid":"12"}]`, 0, true},
		{"empty input", ``, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := ParseRecords([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if len(recs) != tc.want {
				t.Fatalf("got %d records, want %d", len(recs), tc.want)
			}
			for _, r := range recs {
				if r.PID < 0 {
					t.Fatalf("negative PID leaked: %+v", r)
				}
			}
		})
	}
}

func FuzzParseRecords(f *testing.F) {
	f.Add([]byte(sampleJSON))
	f.Add([]byte(`[{"pid":0,"id":""}]`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		recs, err := ParseRecords(data)
		if err != nil {
			return
		}
		for _, r := range recs {
			if r.PID < 0 || (r.PID == 0 && r.ID == "") {
				t.Fatalf("unaddressable record returned: %+v", r)
			}
			if r.EffectiveStatus().Rank() < 0 || r.EffectiveStatus().Rank() > 3 {
				t.Fatalf("rank out of range for %+v", r)
			}
		}
	})
}

func TestParseStatusAndRank(t *testing.T) {
	cases := []struct {
		in   string
		want Status
		rank int
	}{
		{"waiting", StatusWaiting, 0},
		{"idle", StatusIdle, 1},
		{"busy", StatusBusy, 2},
		{"", StatusUnknown, 3},
		{"BUSY", StatusUnknown, 3},
		{"working", StatusUnknown, 3},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseStatus(tc.in)
			if got != tc.want {
				t.Fatalf("ParseStatus(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got.Rank() != tc.rank {
				t.Fatalf("Rank(%q) = %d, want %d", got, got.Rank(), tc.rank)
			}
		})
	}
}

func TestEffectiveStatusFromJobState(t *testing.T) {
	cases := []struct {
		state    JobState
		want     Status
		finished bool
	}{
		{JobWorking, StatusBusy, false},
		{JobBlocked, StatusWaiting, false},
		{JobDone, StatusIdle, true},
		{JobFailed, StatusIdle, true},
		{JobStopped, StatusIdle, true},
		{"", StatusUnknown, false},
		{"paused", StatusUnknown, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			r := Record{ID: "j", Kind: KindBackground, State: tc.state}
			if got := r.EffectiveStatus(); got != tc.want {
				t.Fatalf("EffectiveStatus = %q, want %q", got, tc.want)
			}
			if got := tc.state.Finished(); got != tc.finished {
				t.Fatalf("Finished = %v, want %v", got, tc.finished)
			}
		})
	}
}

func TestAgentTitle(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want string
	}{
		{"name wins", Record{Name: "api-main", CWD: "/work/api"}, "api-main"},
		{"directory name", Record{CWD: "/work/api"}, "api"},
		{"trailing slash", Record{CWD: "/work/api/"}, "api"},
		{"root", Record{CWD: "/"}, "/"},
		{"empty", Record{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Agent{Record: tc.rec}).Title(); got != tc.want {
				t.Fatalf("Title = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSort(t *testing.T) {
	mk := func(key string, st Status, started int64) Agent {
		return Agent{Record: Record{ID: key, StartedAt: started}, Status: st}
	}
	agents := []Agent{
		mk("busy-old", StatusBusy, 1),
		mk("unknown", StatusUnknown, 0),
		mk("idle-new", StatusIdle, 9),
		mk("waiting-new", StatusWaiting, 8),
		mk("idle-old", StatusIdle, 2),
		mk("waiting-b", StatusWaiting, 3),
		mk("waiting-a", StatusWaiting, 3),
	}
	Sort(agents)
	want := []string{"waiting-a", "waiting-b", "waiting-new", "idle-old", "idle-new", "busy-old", "unknown"}
	for i, a := range agents {
		if a.Record.ID != want[i] {
			got := make([]string, len(agents))
			for j, x := range agents {
				got[j] = x.Record.ID
			}
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
