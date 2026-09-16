package app

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestLaunchTeams checks that agent teams reach both places Claude reads them
// from, the environment and the settings file, for the configuration and for
// the per-launch override, and nowhere when neither asks.
func TestLaunchTeams(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		override bool
		want     bool
	}{
		{name: "off", want: false},
		{name: "per-launch override", override: true, want: true},
		{name: "configuration", config: "[claude]\nteams = true\n", want: true},
		{name: "both", config: "[claude]\nteams = true\n", override: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCreateEnv(t)
			if tc.config != "" {
				e.writeConfig(t, tc.config)
			}
			s := openServer(t, e.testHost)
			lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{Teams: tc.override})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := layout.Builtin(layout.Solo, layout.Options{})
			if err != nil {
				t.Fatal(err)
			}
			procs, err := lp.paneProcs(e.Host, plan, e.project, "api")
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(procs[0].Env, session.EnvClaudeTeams+"=1"); got != tc.want {
				t.Fatalf("teams environment = %v, want %v: %q", got, tc.want, procs[0].Env)
			}
			data, err := os.ReadFile(lp.settings)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				TeammateMode string `json:"teammateMode"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if got := doc.TeammateMode == "tmux"; got != tc.want {
				t.Fatalf("teammateMode = %q, want tmux %v", doc.TeammateMode, tc.want)
			}
		})
	}
}

func TestWsSocketPath(t *testing.T) {
	getenv := func(string) string { return "" }
	h := Host{Getenv: getenv}
	cases := []struct {
		name string
		s    *Server
		want string
	}{
		{name: "socket name", s: &Server{SocketName: "lyna-tmux", Client: tmux.New(tmux.Options{Socket: tmux.Socket{Name: "lyna-tmux"}})}, want: SocketPath(getenv, "lyna-tmux")},
		{name: "socket path of a server the user runs", s: &Server{Client: tmux.New(tmux.Options{Socket: tmux.Socket{Path: "/private/tmp/tmux-501/default"}})}, want: "/private/tmp/tmux-501/default"},
		{name: "no client", s: &Server{SocketName: "x"}, want: SocketPath(getenv, "x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.wsSocketPath(h); got != tc.want {
				t.Fatalf("wsSocketPath = %q, want %q", got, tc.want)
			}
		})
	}
	// The path reaches the Claude environment through the launch plan.
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	s.Client = s.Client.WithSocket(tmux.Socket{Path: "/private/tmp/tmux-501/default"})
	lp, err := s.prepareLaunch(e.Host, e.project, "api", LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lp.base.SocketPath != "/private/tmp/tmux-501/default" {
		t.Fatalf("launch socket %q", lp.base.SocketPath)
	}
}
