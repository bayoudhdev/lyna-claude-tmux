package claudecfg

import (
	"strings"
	"testing"
)

func TestConfigFile(t *testing.T) {
	cases := []struct{ name, claudeHome, home, want string }{
		{name: "default home", claudeHome: "/h/.claude", home: "/h", want: "/h/.claude.json"},
		{name: "configured directory", claudeHome: "/srv/claude", home: "/h", want: "/srv/claude/.claude.json"},
		{name: "another user's default layout", claudeHome: "/h2/.claude", home: "/h", want: "/h2/.claude/.claude.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfigFile(tc.claudeHome, tc.home); got != tc.want {
				t.Fatalf("ConfigFile(%q, %q) = %q, want %q", tc.claudeHome, tc.home, got, tc.want)
			}
		})
	}
}

func TestProjectTrust(t *testing.T) {
	const state = `{
	  "numStartups": 12,
	  "projects": {
	    "/work/api": {"hasTrustDialogAccepted": true, "lastCost": 0.4},
	    "/work/web": {"hasTrustDialogAccepted": false},
	    "/work/cli": {"lastCost": 1.5}
	  }
	}`
	cases := []struct {
		name, data, dir string
		want            Trust
		wantErr         string
	}{
		{name: "accepted", data: state, dir: "/work/api", want: TrustAccepted},
		{name: "refused", data: state, dir: "/work/web", want: TrustRefused},
		{name: "entry without the key", data: state, dir: "/work/cli", want: TrustRefused},
		{name: "never opened", data: state, dir: "/work/tools", want: TrustUnknown},
		{name: "trust is not inherited", data: state, dir: "/work/api/pkg", want: TrustUnknown},
		{name: "unclean directory", data: state, dir: "/work/api/", want: TrustAccepted},
		{name: "no projects at all", data: `{"numStartups": 1}`, want: TrustUnknown, dir: "/work/api"},
		{name: "malformed", data: `{`, dir: "/work/api", wantErr: "decode .claude.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectTrust([]byte(tc.data), tc.dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ProjectTrust(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}
