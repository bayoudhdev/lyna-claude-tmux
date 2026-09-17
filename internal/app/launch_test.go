package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestPrepareLaunchOverrides pins the seam the popup path of plugin mode
// uses: Command replaces claude.command, Args replaces claude.args when it is
// not nil (an empty list included), and ExtraArgs follow whichever list won.
func TestPrepareLaunchOverrides(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	fake, err := e.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(e.root, "bin", "my-agent")
	if err := os.Symlink(fake, agent); err != nil {
		t.Fatal(err)
	}
	s.Config.Claude.Args = []string{"--verbose"}
	cases := []struct {
		name     string
		opts     LaunchOptions
		wantPath string
		wantArgs []string
	}{
		{name: "configuration", wantPath: fake, wantArgs: []string{"--verbose"}},
		{name: "command replaced", opts: LaunchOptions{Command: agent}, wantPath: agent, wantArgs: []string{"--verbose"}},
		{name: "nil args keep the configuration", opts: LaunchOptions{Args: nil, ExtraArgs: []string{"--debug"}}, wantPath: fake, wantArgs: []string{"--verbose", "--debug"}},
		{name: "empty args replace the configuration", opts: LaunchOptions{Args: []string{}}, wantPath: fake, wantArgs: nil},
		{name: "args replace the configuration", opts: LaunchOptions{Args: []string{"--append-system-prompt", "be brief"}}, wantPath: fake, wantArgs: []string{"--append-system-prompt", "be brief"}},
		{name: "extra args follow the replacement", opts: LaunchOptions{Args: []string{"-p"}, Continue: true, ExtraArgs: []string{"--debug"}}, wantPath: fake, wantArgs: []string{"-p", "--continue", "--debug"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lp, err := s.prepareLaunch(e.Host, e.project, "api", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if lp.base.ClaudePath != tc.wantPath || !slices.Equal(lp.base.ExtraArgs, tc.wantArgs) {
				t.Fatalf("path %q args %q, want %q %q", lp.base.ClaudePath, lp.base.ExtraArgs, tc.wantPath, tc.wantArgs)
			}
		})
	}
}
