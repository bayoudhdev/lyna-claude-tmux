package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	withInfo := func(mainVersion string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: mainVersion}, Settings: settings}, true
		}
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	cases := []struct {
		name        string
		v, c, d     string
		read        func() (*debug.BuildInfo, bool)
		wantVersion string
		wantCommit  string
		wantDate    string
	}{
		{name: "ldflags win", v: "v1.2.3", c: "abcdef", d: "2026-09-15", read: withInfo("v0.0.1"), wantVersion: "v1.2.3", wantCommit: "abcdef", wantDate: "2026-09-15"},
		{name: "go install module version", read: withInfo("v1.0.0"), wantVersion: "v1.0.0"},
		{name: "devel build uses vcs", read: withInfo("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef0123"}, debug.BuildSetting{Key: "vcs.time", Value: "2026-09-15T10:00:00Z"}), wantVersion: "dev", wantCommit: "0123456789ab", wantDate: "2026-09-15T10:00:00Z"},
		{name: "no build info", read: noInfo, wantVersion: "dev"},
		{name: "ldflags commit not overridden by vcs", c: "feedbeef", read: withInfo("", debug.BuildSetting{Key: "vcs.revision", Value: "cafebabe"}), wantVersion: "dev", wantCommit: "feedbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(tc.v, tc.c, tc.d, tc.read)
			if got.Version != tc.wantVersion || got.Commit != tc.wantCommit || got.Date != tc.wantDate {
				t.Fatalf("resolve() = %+v, want version=%q commit=%q date=%q", got, tc.wantVersion, tc.wantCommit, tc.wantDate)
			}
			if got.GoVersion == "" || !strings.Contains(got.Platform, "/") {
				t.Fatalf("runtime fields missing: %+v", got)
			}
		})
	}
}

func TestInfoString(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want string
	}{
		{name: "full", info: Info{Version: "v1.0.0", Commit: "abc", Date: "2026-09-15", Platform: "linux/amd64"}, want: "lyna-tmux v1.0.0 (abc 2026-09-15) linux/amd64"},
		{name: "commit only", info: Info{Version: "dev", Commit: "abc", Platform: "darwin/arm64"}, want: "lyna-tmux dev (abc) darwin/arm64"},
		{name: "date only", info: Info{Version: "dev", Date: "2026", Platform: "darwin/arm64"}, want: "lyna-tmux dev (2026) darwin/arm64"},
		{name: "bare", info: Info{Version: "dev", Platform: "darwin/arm64"}, want: "lyna-tmux dev darwin/arm64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
