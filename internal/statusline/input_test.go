package statusline

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// documentedStatus is the full example session JSON from the statusLine
// documentation, with every field the line reads.
const documentedStatus = `{
  "cwd": "/home/user/project",
  "session_id": "abc123",
  "session_name": "my-session",
  "transcript_path": "/path/to/transcript.jsonl",
  "model": {"id": "claude-opus-5", "display_name": "Opus"},
  "workspace": {
    "current_dir": "/home/user/project",
    "project_dir": "/home/user/project",
    "added_dirs": [],
    "git_worktree": "feature-xyz",
    "repo": {"host": "github.com", "owner": "acme", "name": "project"}
  },
  "version": "2.1.90",
  "output_style": {"name": "Explanatory"},
  "cost": {"total_cost_usd": 0.01234, "total_duration_ms": 45000, "total_api_duration_ms": 2300, "total_lines_added": 156, "total_lines_removed": 23},
  "context_window": {
    "total_input_tokens": 15500, "total_output_tokens": 1200, "context_window_size": 200000,
    "used_percentage": 8, "remaining_percentage": 92,
    "current_usage": {"input_tokens": 8500, "output_tokens": 1200, "cache_creation_input_tokens": 5000, "cache_read_input_tokens": 2000}
  },
  "prompt_cache": {"warm": true, "hit_ratio": 0.91},
  "fast_mode": false,
  "effort": {"level": "high"},
  "thinking": {"enabled": true},
  "rate_limits": {
    "five_hour": {"used_percentage": 23.5, "resets_at": 1789500900},
    "seven_day": {"used_percentage": 41.2, "resets_at": 1789788600},
    "spend_limit": {"used_percentage": 62.8, "resets_at": 1790000000}
  },
  "vim": {"mode": "NORMAL"},
  "agent": {"name": "security-reviewer"},
  "pr": {"number": 1234, "url": "https://example.com/pull/1234", "review_state": "pending"},
  "worktree": {"name": "my-feature", "path": "/path/to/.claude/worktrees/my-feature", "branch": "worktree-my-feature", "original_cwd": "/home/user/project", "original_branch": "main"}
}`

func num(v float64) Number { return Number{Value: v, Valid: true} }

func TestDecode(t *testing.T) {
	cases := []struct {
		name     string
		data     string
		want     Status
		wantErr  bool
		typeErr  bool
		wantZero bool
	}{
		{
			name: "documented example",
			data: documentedStatus,
			want: Status{
				Cwd:           "/home/user/project",
				Model:         Model{ID: "claude-opus-5", DisplayName: "Opus"},
				Workspace:     Workspace{CurrentDir: "/home/user/project", ProjectDir: "/home/user/project", GitWorktree: "feature-xyz"},
				OutputStyle:   OutputStyle{Name: "Explanatory"},
				Cost:          Cost{TotalCostUSD: num(0.01234)},
				ContextWindow: ContextWindow{UsedPercentage: num(8), ContextWindowSize: num(200000)},
				Effort:        Effort{Level: "high"},
				RateLimits: RateLimits{
					FiveHour:   RateWindow{UsedPercentage: num(23.5), ResetsAt: num(1789500900)},
					SevenDay:   RateWindow{UsedPercentage: num(41.2), ResetsAt: num(1789788600)},
					SpendLimit: RateWindow{UsedPercentage: num(62.8), ResetsAt: num(1790000000)},
				},
				Worktree: Worktree{Name: "my-feature", Branch: "worktree-my-feature"},
			},
		},
		{
			name: "early session nulls",
			data: `{"model":{"display_name":"Sonnet"},"context_window":{"used_percentage":null,"current_usage":null},"cost":{"total_cost_usd":null}}`,
			want: Status{Model: Model{DisplayName: "Sonnet"}},
		},
		{
			name: "absent windows stay invalid",
			data: `{"rate_limits":{"seven_day":{"used_percentage":5}}}`,
			want: Status{RateLimits: RateLimits{SevenDay: RateWindow{UsedPercentage: num(5)}}},
		},
		{
			name:    "changed field type keeps the rest",
			data:    `{"model":"Opus","effort":{"level":"max"},"cost":{"total_cost_usd":"1.5"}}`,
			want:    Status{Effort: Effort{Level: "max"}},
			wantErr: true, typeErr: true,
		},
		{name: "not an object", data: `["model"]`, wantErr: true, typeErr: true, wantZero: true},
		{name: "malformed", data: `{"model":{"display_name":"Opus"`, wantErr: true, wantZero: true},
		{name: "empty", data: ``, wantErr: true, wantZero: true},
		{name: "trailing garbage", data: `{"model":{"display_name":"Opus"}} x`, wantErr: true, wantZero: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode([]byte(tc.data))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Decode error = %v; want error %v", err, tc.wantErr)
			}
			var typeErr *json.UnmarshalTypeError
			if tc.typeErr != errors.As(err, &typeErr) {
				t.Fatalf("Decode error %v; want type error %v", err, tc.typeErr)
			}
			if tc.wantZero {
				if !reflect.DeepEqual(got, Status{}) {
					t.Fatalf("Decode = %+v; want zero", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Decode =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestNumber(t *testing.T) {
	cases := []struct {
		data string
		want Number
	}{
		{data: `0`, want: num(0)},
		{data: `-0.5`, want: num(-0.5)},
		{data: `1.5e3`, want: num(1500)},
		{data: `1789500900`, want: num(1789500900)},
		{data: `null`},
		{data: `"12"`},
		{data: `true`},
		{data: `{"value":1}`},
		{data: `[1]`},
		{data: `1e400`},
		{data: `-1e400`},
	}
	for _, tc := range cases {
		t.Run(tc.data, func(t *testing.T) {
			var w struct {
				N Number `json:"n"`
			}
			if err := json.Unmarshal([]byte(`{"n":`+tc.data+`}`), &w); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if w.N != tc.want {
				t.Fatalf("Number = %+v; want %+v", w.N, tc.want)
			}
			enc, err := json.Marshal(w.N)
			if err != nil {
				t.Fatal(err)
			}
			var back Number
			if err := json.Unmarshal(enc, &back); err != nil || back != w.N {
				t.Fatalf("round trip %s = %+v, %v", enc, back, err)
			}
		})
	}
}

func FuzzDecode(f *testing.F) {
	for _, s := range []string{documentedStatus, "", "{", "null", "[]", `{"model":1}`, `{"cost":{"total_cost_usd":1e308}}`, `{"model":{"display_name":"\xff"}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Decode(data)
		var typeErr *json.UnmarshalTypeError
		if err != nil && !errors.As(err, &typeErr) && !reflect.DeepEqual(s, Status{}) {
			t.Fatalf("fields returned with a syntax error: %+v", s)
		}
		// Whatever was decoded survives a round trip unchanged.
		enc, merr := json.Marshal(s)
		if merr != nil {
			t.Fatalf("marshal: %v", merr)
		}
		again, derr := Decode(enc)
		if derr != nil {
			t.Fatalf("re-decode %s: %v", enc, derr)
		}
		if !reflect.DeepEqual(again, s) {
			t.Fatalf("round trip changed status:\n%+v\n%+v", s, again)
		}
	})
}
