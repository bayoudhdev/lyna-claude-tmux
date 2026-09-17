package team_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

func TestName(t *testing.T) {
	cases := []struct {
		name    string
		session string
		want    string
	}{
		{name: "a session id keeps its first eight characters", session: "1a2b3c4d-4eb0-4b95-bca6-1cd785b8ea2e", want: "session-1a2b3c4d"},
		{name: "an id of exactly eight is used whole", session: "1a2b3c4d", want: "session-1a2b3c4d"},
		{name: "a shorter id is used whole", session: "1a2b", want: "session-1a2b"},
		{name: "no id names the directory of no team", session: "", want: "session-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := team.Name(tc.session); got != tc.want {
				t.Fatalf("Name(%q) = %q, want %q", tc.session, got, tc.want)
			}
		})
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "config", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseConfigReadsWhatClaudeCodeWrites(t *testing.T) {
	type member struct {
		name      string
		agentType string
		backend   team.Backend
		pane      string // the answer of TmuxPane, empty when it has none
		lead      bool
		active    bool
	}
	cases := []struct {
		name      string
		fixture   string
		team      string
		leadAgent string
		members   []member
	}{
		{
			name: "a team of three teammates around a lead", fixture: "team",
			team: "session-1a2b3c4d", leadAgent: "team-lead@session-1a2b3c4d",
			members: []member{
				{name: "team-lead", agentType: "team-lead", backend: team.BackendInProcess, lead: true},
				{name: "explore-git", agentType: "Explore", backend: team.BackendTmux, pane: "%0"},
				{name: "review-api", agentType: "security-reviewer", backend: team.BackendTmux, pane: "%1", active: true},
				{name: "tests", agentType: "general-purpose", backend: team.BackendTmux, pane: "%2", active: true},
			},
		},
		{
			name: "a lead with nobody spawned yet", fixture: "lead-only",
			team: "session-052a0221", leadAgent: "team-lead@session-052a0221",
			members: []member{
				{name: "team-lead", agentType: "team-lead", backend: team.BackendInProcess, lead: true},
			},
		},
		{
			name: "a release that writes fields we do not know", fixture: "newer-claude",
			team: "session-9f9f9f9f", leadAgent: "team-lead@session-9f9f9f9f",
			members: []member{
				{name: "team-lead", agentType: "team-lead", backend: team.BackendInProcess, lead: true},
				// No pane: a backend with no tmux pane answers none, whatever
				// else the entry carries.
				{name: "docs", backend: team.BackendITerm2},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := team.ParseConfig(fixture(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Name != tc.team || cfg.LeadAgentID != tc.leadAgent {
				t.Fatalf("team %q lead %q, want %q and %q", cfg.Name, cfg.LeadAgentID, tc.team, tc.leadAgent)
			}
			if len(cfg.Members) != len(tc.members) {
				t.Fatalf("%d members, want %d", len(cfg.Members), len(tc.members))
			}
			for i, want := range tc.members {
				got := cfg.Members[i]
				pane, hasPane := got.TmuxPane()
				if !hasPane {
					pane = ""
				}
				if got.Name != want.name || got.AgentType != want.agentType || got.Backend != want.backend ||
					pane != want.pane || got.Lead != want.lead || got.Active != want.active {
					t.Fatalf("member %d = %+v (pane %q), want %+v", i, got, pane, want)
				}
			}
			if len(cfg.Teammates()) != len(cfg.Members)-1 {
				t.Fatalf("%d teammates of %d members", len(cfg.Teammates()), len(cfg.Members))
			}
		})
	}
}

func TestParseConfigLookups(t *testing.T) {
	cfg, err := team.ParseConfig(fixture(t, "team"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a pane names its teammate", func(t *testing.T) {
		m, ok := cfg.ByPane("%1")
		if !ok || m.Name != "review-api" {
			t.Fatalf("ByPane(%%1) = %+v, %v", m, ok)
		}
	})
	t.Run("a pane of no teammate names nobody", func(t *testing.T) {
		for _, pane := range []string{"%7", "", team.LeadPane} {
			if m, ok := cfg.ByPane(pane); ok {
				t.Fatalf("ByPane(%q) = %+v", pane, m)
			}
		}
	})
	t.Run("a name finds its member", func(t *testing.T) {
		m, ok := cfg.ByName("tests")
		if !ok || m.Model != "claude-sonnet-5" || m.Dir != "/home/dev/acme-api/service" {
			t.Fatalf("ByName(tests) = %+v, %v", m, ok)
		}
		if _, ok := cfg.ByName("nobody"); ok {
			t.Fatal("ByName(nobody) found a member")
		}
	})
	t.Run("the lead keeps its timestamps", func(t *testing.T) {
		if cfg.CreatedAt.IsZero() || cfg.Members[0].JoinedAt.IsZero() {
			t.Fatalf("created %v, joined %v", cfg.CreatedAt, cfg.Members[0].JoinedAt)
		}
	})
}

func TestParseConfigRefusals(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantErr error
		check   func(t *testing.T, c team.Config)
	}{
		{name: "not json at all", data: []byte("{"), wantErr: nil},
		{name: "a list where the file is an object", data: []byte("[]"), wantErr: nil},
		{
			name: "a file past the cap", wantErr: team.ErrTooLarge,
			data: append([]byte(`{"name":"session-1","members":[]}`), make([]byte, team.MaxConfigSize)...),
		},
		{
			name: "a member with no name is dropped",
			data: []byte(`{"name":"session-1","leadAgentId":"a","members":[{"agentId":"a","name":"lead"},{"agentId":"b"}]}`),
			check: func(t *testing.T, c team.Config) {
				if len(c.Members) != 1 || c.Members[0].Name != "lead" {
					t.Fatalf("members %+v", c.Members)
				}
			},
		},
		{
			name: "no lead identifier falls back to the agent type",
			data: []byte(`{"name":"session-1","members":[{"name":"lead","agentType":"team-lead"},{"name":"worker","agentType":"Explore"}]}`),
			check: func(t *testing.T, c team.Config) {
				if !c.Members[0].Lead || c.Members[1].Lead {
					t.Fatalf("lead flags %v %v", c.Members[0].Lead, c.Members[1].Lead)
				}
			},
		},
		{
			name: "a timestamp nobody wrote stays zero",
			data: []byte(`{"name":"session-1","members":[{"name":"lead","joinedAt":0}]}`),
			check: func(t *testing.T, c team.Config) {
				if !c.CreatedAt.IsZero() || !c.Members[0].JoinedAt.IsZero() {
					t.Fatalf("created %v joined %v", c.CreatedAt, c.Members[0].JoinedAt)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := team.ParseConfig(tc.data)
			switch {
			case tc.check != nil:
				if err != nil {
					t.Fatal(err)
				}
				tc.check(t, cfg)
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
			case err == nil:
				t.Fatalf("no error, config %+v", cfg)
			}
		})
	}
}

func TestMemberTmuxPane(t *testing.T) {
	cases := []struct {
		name    string
		member  team.Member
		want    string
		wantAny bool
	}{
		{name: "a teammate of the tmux backend", member: team.Member{Backend: team.BackendTmux, Pane: "%3"}, want: "%3", wantAny: true},
		{name: "the lead of an in-process team", member: team.Member{Backend: team.BackendInProcess, Pane: team.LeadPane}},
		{name: "the word leader is never a pane", member: team.Member{Backend: team.BackendTmux, Pane: team.LeadPane}},
		{name: "a terminal split is not a tmux pane", member: team.Member{Backend: team.BackendITerm2, Pane: "w1p3"}},
		{name: "a backend added after this release", member: team.Member{Backend: "wezterm", Pane: "%1"}},
		{name: "no pane recorded at all", member: team.Member{Backend: team.BackendTmux}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.member.TmuxPane()
			if ok != tc.wantAny || got != tc.want {
				t.Fatalf("TmuxPane() = %q, %v; want %q, %v", got, ok, tc.want, tc.wantAny)
			}
		})
	}
}

func FuzzParseConfig(f *testing.F) {
	for _, name := range []string{"team", "lead-only", "newer-claude"} {
		data, err := os.ReadFile(filepath.Join("testdata", "config", name+".json"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"members":[{"name":"a","backendType":"tmux","tmuxPaneId":"%1"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := team.ParseConfig(data)
		if err != nil {
			return
		}
		for _, m := range cfg.Members {
			if m.Name == "" {
				t.Fatalf("a member with no name survived: %+v", m)
			}
			if pane, ok := m.TmuxPane(); ok && !strings.HasPrefix(pane, "%") {
				t.Fatalf("pane %q is not a tmux pane id", pane)
			}
		}
		if got := len(cfg.Teammates()); got > len(cfg.Members) {
			t.Fatalf("%d teammates of %d members", got, len(cfg.Members))
		}
	})
}
