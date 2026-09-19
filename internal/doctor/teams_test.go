package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
)

func TestCheckTeams(t *testing.T) {
	const logFile = "/state/lyna-tmux/lyna-tmux.log"
	const (
		placed    = "2026-09-15T12:30:00Z teammate: review-api opened in workspace api\n"
		unplaced  = "2026-09-15T12:31:00Z teammate-unplaced: write-docs opened in workspace api and stays where the agent put it: no window\n"
		fellBack  = "2026-09-15T12:32:00Z teammate-fallback: write-docs opens the way the agent opens it: no server\n"
		otherHook = "2026-09-15T12:40:00Z hook Stop: pane is gone\n"
	)
	cases := []struct {
		name      string
		teams     bool
		mode      string
		logFile   string
		log       string
		logErr    error
		want      Status
		detailHas []string
		fixHas    string
	}{
		{
			name: "teammates open the agent's own way by choice", teams: true, mode: "in-process", logFile: logFile,
			log: fellBack, want: StatusSkip, detailHas: []string{"claude.teams is on", `"in-process"`},
		},
		{
			name: "the log is unknown", teams: true, want: StatusSkip,
			detailHas: []string{"diagnostic log is unknown"},
		},
		{
			name: "the log cannot be read", teams: true, logFile: logFile, logErr: errors.New("permission denied"),
			want: StatusWarn, detailHas: []string{"cannot be read", "permission denied"},
		},
		{
			name: "the log is past its cap", teams: true, logFile: logFile, logErr: fsx.ErrTooLarge,
			want: StatusWarn, detailHas: []string{"cannot be read"},
		},
		{
			name: "no log yet with teams on", teams: true, mode: "lmux", logFile: logFile,
			want: StatusOK, detailHas: []string{"claude.teams is on", "no teammate has opened"},
		},
		{
			name: "no teammate yet with teams off", logFile: logFile, log: otherHook,
			want: StatusSkip, detailHas: []string{"lmux team", "no teammate has opened"},
		},
		{
			name: "the last teammate is placed", teams: true, logFile: logFile, log: fellBack + placed + otherHook,
			want: StatusOK, detailHas: []string{"at 2026-09-15 12:30 UTC", "review-api opened in workspace api"},
		},
		{
			name: "the last teammate could not be placed", teams: true, logFile: logFile, log: placed + unplaced + otherHook,
			want: StatusWarn, detailHas: []string{"could not be arranged", "at 2026-09-15 12:31 UTC", "no window", logFile},
			fixHas: "press w",
		},
		{
			name: "the last teammate fell back", logFile: logFile, log: placed + unplaced + fellBack,
			want: StatusWarn, detailHas: []string{"lmux team", "could not take teammates over", "no server", logFile},
			fixHas: "reason above",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeSystem{files: map[string]string{}, errs: map[string]error{}}
			if tc.log != "" {
				f.files[logFile] = tc.log
			}
			if tc.logErr != nil {
				f.errs[logFile] = tc.logErr
			}
			d := f.deps()
			d.Teams, d.TeammateMode, d.LogFile = tc.teams, tc.mode, tc.logFile
			got := checkTeams(t.Context(), d.withDefaults())
			if len(got) != 1 || got[0].ID != teamsID {
				t.Fatalf("results %+v", got)
			}
			r := got[0]
			if r.Status != tc.want {
				t.Fatalf("status %q, want %q (detail %q)", r.Status, tc.want, r.Detail)
			}
			for _, part := range tc.detailHas {
				if !strings.Contains(r.Detail, part) {
					t.Fatalf("detail %q, want it to contain %q", r.Detail, part)
				}
			}
			if tc.fixHas == "" && r.Fix != "" || !strings.Contains(r.Fix, tc.fixHas) {
				t.Fatalf("fix %q, want %q", r.Fix, tc.fixHas)
			}
		})
	}
}

// TestCheckTeamsReadsTheWholeLog holds the read cap to the log's own: a log
// at its largest, one line past the rotation size, is still read.
func TestCheckTeamsReadsTheWholeLog(t *testing.T) {
	const logFile = "/state/lyna-tmux.log"
	var b strings.Builder
	for b.Len() < hook.LogMaxBytes {
		b.WriteString("2026-09-15T12:00:00Z hook Stop: " + strings.Repeat("x", 200) + "\n")
	}
	b.WriteString("2026-09-15T12:30:00Z teammate: review-api opened in workspace api\n")
	log := b.String()
	f := fakeSystem{files: map[string]string{logFile: log}}
	d := f.deps()
	d.Teams, d.LogFile = true, logFile
	r := checkTeams(t.Context(), d.withDefaults())[0]
	if r.Status != StatusOK || !strings.Contains(r.Detail, "review-api") {
		t.Fatalf("a %d byte log reads as %q: %s", len(log), r.Status, r.Detail)
	}
}
