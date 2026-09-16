package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestPopupStartRecordsSandbox pins the session options a popup session
// carries: the sandbox profile and the isolation level it was started at, the
// same pair a workspace records, so the sandbox status and the commands run
// inside it read the launch that happened.
func TestPopupStartRecordsSandbox(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	profile, isolation := s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation
	cases := []struct {
		name string
		// profile and isolation replace the configuration for the case.
		profile, isolation string
		// inside runs the case as if this process were in a container.
		inside                     bool
		dir                        string
		wantSandbox, wantIsolation string
	}{
		{name: "configured defaults", dir: "plain", wantSandbox: "standard", wantIsolation: "bash"},
		{name: "strict profile", profile: "strict", dir: "strict", wantSandbox: "strict", wantIsolation: "bash"},
		{name: "container isolation", isolation: "container", inside: true, dir: "container", wantSandbox: "standard", wantIsolation: "container"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolationInsideContainer(t, tc.inside)
			s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation = pick(tc.profile, profile), pick(tc.isolation, isolation)
			t.Cleanup(func() { s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation = profile, isolation })
			dir := filepath.Join(e.root, "popup", tc.dir)
			mkdir(t, dir)
			ctx := tmuxtest.Context(t)
			name := session.PopupName(s.Config.Popup.SessionPrefix, dir)
			created, err := s.popupStart(ctx, e.Host, name, dir)
			if err != nil || !created {
				t.Fatalf("popupStart created %v: %v", created, err)
			}
			target := tmux.ExactSession(name)
			got := map[string]string{}
			for _, opt := range []string{tmux.OptSandbox, tmux.OptIsolation} {
				if got[opt], err = s.Client.ShowOption(ctx, "", target, opt); err != nil {
					t.Fatal(err)
				}
			}
			if got[tmux.OptSandbox] != tc.wantSandbox || got[tmux.OptIsolation] != tc.wantIsolation {
				t.Fatalf("session options %v, want sandbox %q isolation %q", got, tc.wantSandbox, tc.wantIsolation)
			}
		})
	}
}

func TestPopupRequestValidate(t *testing.T) {
	cases := []struct {
		name    string
		req     PopupRequest
		wantErr string
	}{
		{name: "terminal client", req: PopupRequest{Pane: "%3", Client: "/dev/ttys003"}},
		{name: "control mode client", req: PopupRequest{Pane: "%0", Client: "client-1234"}},
		{name: "pane name", req: PopupRequest{Pane: "main", Client: "c"}, wantErr: "not a pane id"},
		{name: "pane pattern", req: PopupRequest{Pane: "%*", Client: "c"}, wantErr: "not a pane id"},
		{name: "no client", req: PopupRequest{Pane: "%3"}, wantErr: "not a tmux client name"},
		{name: "control character in client", req: PopupRequest{Pane: "%3", Client: "/dev/tty\n"}, wantErr: "not a tmux client name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.popupValidate()
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("popupValidate = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPopupTmuxPath(t *testing.T) {
	cases := []struct {
		name    string
		bin     string
		look    func(string) (string, error)
		want    string
		wantErr string
	}{
		{name: "absolute binary", bin: "/opt/homebrew/bin/tmux", want: "/opt/homebrew/bin/tmux"},
		{name: "found on PATH", bin: "tmux", look: func(string) (string, error) { return "/usr/bin/tmux", nil }, want: "/usr/bin/tmux"},
		{name: "relative PATH entry", bin: "tmux", look: func(string) (string, error) { return "bin/tmux", nil }, want: filepath.Join(popupCwd(t), "bin/tmux")},
		{name: "missing", bin: "tmux", look: func(string) (string, error) { return "", os.ErrNotExist }, wantErr: "find tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := popupTmuxPath(Host{LookPath: tc.look}, tc.bin)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("popupTmuxPath = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func popupCwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
