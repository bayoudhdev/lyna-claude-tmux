package xdg

import (
	"errors"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		home    string
		want    Paths
		wantErr error
	}{
		{
			name: "defaults",
			home: "/home/dev",
			want: Paths{Home: "/home/dev", Config: "/home/dev/.config/lyna-tmux", Data: "/home/dev/.local/share/lyna-tmux", State: "/home/dev/.local/state/lyna-tmux", Cache: "/home/dev/.cache/lyna-tmux"},
		},
		{
			name: "xdg overrides",
			env:  map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/d", "XDG_STATE_HOME": "/st/", "XDG_CACHE_HOME": "/c"},
			home: "/home/dev",
			want: Paths{Home: "/home/dev", Config: "/cfg/lyna-tmux", Data: "/d/lyna-tmux", State: "/st/lyna-tmux", Cache: "/c/lyna-tmux"},
		},
		{
			name: "relative xdg ignored",
			env:  map[string]string{"XDG_CONFIG_HOME": "cfg", "XDG_DATA_HOME": "share", "XDG_STATE_HOME": "./st"},
			home: "/home/dev",
			want: Paths{Home: "/home/dev", Config: "/home/dev/.config/lyna-tmux", Data: "/home/dev/.local/share/lyna-tmux", State: "/home/dev/.local/state/lyna-tmux", Cache: "/home/dev/.cache/lyna-tmux"},
		},
		{
			name: "lyna home wins",
			env:  map[string]string{"LYNA_TMUX_HOME": "/tmp/lt", "XDG_CONFIG_HOME": "/cfg"},
			home: "/home/dev",
			want: Paths{Home: "/home/dev", Config: "/tmp/lt/config", Data: "/tmp/lt/data", State: "/tmp/lt/state", Cache: "/tmp/lt/cache"},
		},
		{
			name: "lyna home without home",
			env:  map[string]string{"LYNA_TMUX_HOME": "/tmp/lt"},
			want: Paths{Home: "/tmp/lt", Config: "/tmp/lt/config", Data: "/tmp/lt/data", State: "/tmp/lt/state", Cache: "/tmp/lt/cache"},
		},
		{name: "no home", wantErr: ErrNoHome},
		{name: "relative home", home: "home", wantErr: ErrNoHome},
		{name: "relative lyna home ignored", env: map[string]string{"LYNA_TMUX_HOME": "lt"}, wantErr: ErrNoHome},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(envOf(tc.env), tc.home)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("Resolve() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDerivedPaths(t *testing.T) {
	p := Paths{Home: "/h", Config: "/h/.config/lyna-tmux", Data: "/h/.local/share/lyna-tmux", State: "/h/.local/state/lyna-tmux", Cache: "/h/.cache/lyna-tmux"}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"config file", p.ConfigFile(), "/h/.config/lyna-tmux/config.toml"},
		{"local tmux conf", p.LocalTmuxConf(), "/h/.config/lyna-tmux/tmux.local.conf"},
		{"tmux conf", p.TmuxConf(), "/h/.local/state/lyna-tmux/tmux.conf"},
		{"settings dir", p.SettingsDir(), "/h/.local/state/lyna-tmux/settings"},
		{"log", p.LogFile(), "/h/.local/state/lyna-tmux/lyna-tmux.log"},
		{"agents cache", p.AgentsCache(), "/h/.cache/lyna-tmux/agents.json"},
		{"review dir", p.ReviewDir(), "/h/.local/share/lyna-tmux/review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestClaudeHome(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "default", want: "/h/.claude"},
		{name: "override", env: map[string]string{"CLAUDE_CONFIG_DIR": "/opt/claude/"}, want: "/opt/claude"},
		{name: "relative ignored", env: map[string]string{"CLAUDE_CONFIG_DIR": "claude"}, want: "/h/.claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeHome(envOf(tc.env), "/h"); got != tc.want {
				t.Fatalf("ClaudeHome() = %q, want %q", got, tc.want)
			}
		})
	}
}
