package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestPluginOptionsFor(t *testing.T) {
	const bin = "/usr/local/bin/lmux"
	str := func(s string) *string { return &s }
	cases := []struct {
		name    string
		bin     string
		user    tmux.PluginUserOptions
		req     PluginRequest
		want    tmux.PluginOptions
		wantErr string
	}{
		{name: "defaults", want: tmux.PluginOptions{Bin: bin, LaunchKey: "y", ListKey: "u", ForwardBell: true}},
		{
			name: "server options", user: tmux.PluginUserOptions{LaunchKey: "Y", ListKey: "M-u", ForwardBell: "on"},
			want: tmux.PluginOptions{Bin: bin, LaunchKey: "Y", ListKey: "M-u", ForwardBell: true},
		},
		{name: "bell option off", user: tmux.PluginUserOptions{ForwardBell: "off"}, want: tmux.PluginOptions{Bin: bin, LaunchKey: "y", ListKey: "u"}},
		{name: "bell option other than on", user: tmux.PluginUserOptions{ForwardBell: "yes"}, want: tmux.PluginOptions{Bin: bin, LaunchKey: "y", ListKey: "u"}},
		{
			name: "flags override options", user: tmux.PluginUserOptions{LaunchKey: "Y", ListKey: "U", ForwardBell: "on"},
			req:  PluginRequest{LaunchKey: str("C-y"), ListKey: str(`\`), NoBell: true},
			want: tmux.PluginOptions{Bin: bin, LaunchKey: "C-y", ListKey: `\`},
		},
		{name: "empty flag leaves the key unbound", user: tmux.PluginUserOptions{ListKey: "U"}, req: PluginRequest{ListKey: str("")}, want: tmux.PluginOptions{Bin: bin, LaunchKey: "y", ForwardBell: true}},
		{name: "key with a space", req: PluginRequest{LaunchKey: str("a b")}, wantErr: `launch key "a b" is not a tmux key name`},
		{name: "control character in an option", user: tmux.PluginUserOptions{ListKey: "u\x1b"}, wantErr: "list key"},
		{name: "overlong key", req: PluginRequest{LaunchKey: str(strings.Repeat("x", 33))}, wantErr: "not a tmux key name"},
		{name: "same key twice", req: PluginRequest{LaunchKey: str("u")}, wantErr: `both "u"`},
		{name: "relative binary", bin: "lyna-tmux", wantErr: "not absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bin
			if tc.bin != "" {
				b = tc.bin
			}
			got, err := PluginOptionsFor(b, tc.user, tc.req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("PluginOptionsFor = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPluginSourceLine writes the plugin configuration under a state
// directory whose name is glob, quote and format syntax, and sources it
// through the printed line on a real server.
func TestPluginSourceLine(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, `it's [a]*? #{x} \ ;`)
	env := map[string]string{"LYNA_TMUX_HOME": home}
	h := Host{Getenv: func(k string) string { return env[k] }, Home: root}
	opts := tmux.PluginOptions{Bin: "/opt/lmux", LaunchKey: "y", ListKey: "u", ForwardBell: true}
	path, err := WritePluginConf(h, opts)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(home, "state") || filepath.Base(path) != PluginConfName {
		t.Fatalf("written to %s", path)
	}
	assertMode(t, path, 0o600)
	if data, err := os.ReadFile(path); err != nil || string(data) != tmux.PluginConf(opts) {
		t.Fatalf("content %q (%v)", data, err)
	}
	// A decoy the unescaped glob would also match.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "plugin.tmux.conf.decoy"), []byte("set-option -g @decoy 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(root, "user.conf")
	if err := os.WriteFile(conf, []byte(PluginSourceLine(path)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "source-file", conf); err != nil {
		t.Fatalf("source %s: %v", PluginSourceLine(path), err)
	}
	keys, err := srv.Client.Run(ctx, "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(keys, "/opt/lmux popup launch --pane") {
		t.Fatalf("plugin bindings not loaded from %s:\n%s", path, keys)
	}
	if decoy, _ := srv.Client.ShowOption(ctx, "-g", "", "@decoy"); decoy != "" {
		t.Fatal("source-file line matched another file")
	}
}

func TestOpenPluginServer(t *testing.T) {
	h := newTestHost(t)
	ctx := tmuxtest.Context(t)
	cases := []struct {
		name     string
		tmuxEnv  string
		wantPath string
		wantErr  error
	}{
		{name: "socket from TMUX", tmuxEnv: "/private/tmp/tmux-501/default,123,4", wantPath: "/private/tmp/tmux-501/default"},
		{name: "outside tmux", tmuxEnv: "", wantErr: ErrNotInTmux},
		{name: "relative socket", tmuxEnv: "default,1,0", wantErr: ErrNotInTmux},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.env["TMUX"] = tc.tmuxEnv
			s, err := OpenPluginServer(ctx, h.Host)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), "$TMUX") {
					t.Fatalf("err = %v, want %v with the remedy", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Client.Socket(); got.Path != tc.wantPath || got.Name != "" {
				t.Fatalf("socket %+v, want path %s", got, tc.wantPath)
			}
			if want := filepath.Join(h.root, "state", "plugin", "settings"); s.Paths.SettingsDir() != want {
				t.Fatalf("settings directory %s, want %s apart from the lyna-tmux server's", s.Paths.SettingsDir(), want)
			}
			if _, err := os.Stat(filepath.Join(h.root, "state", "tmux.conf")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("plugin mode wrote the generated tmux configuration: %v", err)
			}
		})
	}
	h.env["TMUX"] = "/private/tmp/tmux-501/default,1,0"
	h.TmuxBin = filepath.Join(h.root, "no-tmux")
	if _, err := OpenPluginServer(ctx, h.Host); !errors.Is(err, tmux.ErrNotInstalled) {
		t.Fatalf("missing tmux: err = %v", err)
	}
}
