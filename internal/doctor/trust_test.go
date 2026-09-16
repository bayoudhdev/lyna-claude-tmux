package doctor

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestCheckProjectTrust(t *testing.T) {
	const state = `{"projects": {"/work/api": {"hasTrustDialogAccepted": true}, "/work/web": {"hasTrustDialogAccepted": false}}}`
	cases := []struct {
		name       string
		home       string
		claudeHome string
		project    string
		files      map[string]string
		errs       map[string]error
		want       Status
		detailHas  string
		wantFix    bool
	}{
		{
			name: "trusted project", home: "/h", claudeHome: "/h/.claude", project: "/work/api",
			files: map[string]string{"/h/.claude.json": state},
			want:  StatusOK, detailHas: "/work/api",
		},
		{
			name: "trust refused", home: "/h", claudeHome: "/h/.claude", project: "/work/web",
			files: map[string]string{"/h/.claude.json": state},
			want:  StatusWarn, detailHas: "runs no hooks", wantFix: true,
		},
		{
			name: "never opened", home: "/h", claudeHome: "/h/.claude", project: "/work/cli",
			files: map[string]string{"/h/.claude.json": state},
			want:  StatusWarn, detailHas: "never opened", wantFix: true,
		},
		{
			name: "configured claude directory", home: "/h", claudeHome: "/srv/claude", project: "/work/api",
			files: map[string]string{"/srv/claude/.claude.json": state},
			want:  StatusOK, detailHas: "/work/api",
		},
		{
			name: "no state file yet", home: "/h", claudeHome: "/h/.claude", project: "/work/api",
			want: StatusSkip, detailHas: "/h/.claude.json",
		},
		{
			name: "unreadable state file", home: "/h", claudeHome: "/h/.claude", project: "/work/api",
			errs: map[string]error{"/h/.claude.json": errors.New("permission denied")},
			want: StatusWarn, detailHas: "permission denied", wantFix: true,
		},
		{
			name: "malformed state file", home: "/h", claudeHome: "/h/.claude", project: "/work/api",
			files: map[string]string{"/h/.claude.json": "{"},
			want:  StatusWarn, detailHas: "decode", wantFix: true,
		},
		{
			name: "outside a project", home: "/h", claudeHome: "/h/.claude",
			files: map[string]string{"/h/.claude.json": state},
			want:  StatusSkip, detailHas: "no project directory",
		},
		{
			name: "home unknown", claudeHome: "/h/.claude", project: "/work/api",
			files: map[string]string{"/h/.claude.json": state},
			want:  StatusSkip, detailHas: "no project directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeSystem{files: tc.files, errs: tc.errs}
			d := f.deps()
			if tc.errs == nil {
				// A path with no entry must read as missing, not as empty.
				d.ReadFile = func(path string, _ int64) ([]byte, error) {
					if content, ok := tc.files[path]; ok {
						return []byte(content), nil
					}
					return nil, fs.ErrNotExist
				}
			}
			d.Home, d.ClaudeHome, d.ProjectDir = tc.home, tc.claudeHome, tc.project
			got := checkProjectTrust(t.Context(), d.withDefaults())
			if len(got) != 1 || got[0].ID != trustID {
				t.Fatalf("results %+v", got)
			}
			r := got[0]
			if r.Status != tc.want {
				t.Fatalf("status %q, want %q (detail %q)", r.Status, tc.want, r.Detail)
			}
			if !strings.Contains(r.Detail, tc.detailHas) {
				t.Fatalf("detail %q, want it to contain %q", r.Detail, tc.detailHas)
			}
			if hasFix := r.Fix != ""; hasFix != tc.wantFix {
				t.Fatalf("fix %q, want one: %v", r.Fix, tc.wantFix)
			}
		})
	}
}
