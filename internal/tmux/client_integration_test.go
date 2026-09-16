package tmux_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestIntegrationClientEnv checks that the environment of the client that
// starts a server becomes the server's global environment: an explicit
// environment replaces the caller's, a nil one inherits it.
func TestIntegrationClientEnv(t *testing.T) {
	bin := tmuxtest.Require(t)
	t.Setenv("LT_PARENT_SECRET", "leak")
	cases := []struct {
		name     string
		env      []string
		wantMark bool
		wantLeak bool
	}{
		{name: "explicit environment replaces the caller's", env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LT_MARK=kept"}, wantMark: true},
		{name: "nil environment inherits", env: nil, wantLeak: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := tmux.Socket{Name: tmuxtest.Socket(t, bin)}
			client := tmux.New(tmux.Options{Bin: bin, Socket: socket, Config: "/dev/null", Env: tc.env})
			ctx := tmuxtest.Context(t)
			if _, err := client.Run(ctx, "new-session", "-d", "-s", "env", "sleep 3600"); err != nil {
				t.Fatal(err)
			}
			all, err := client.Run(ctx, "show-environment", "-g")
			if err != nil {
				t.Fatal(err)
			}
			vars := strings.Split(all, "\n")
			if got := slices.Contains(vars, "LT_MARK=kept"); got != tc.wantMark {
				t.Errorf("LT_MARK=kept in server environment = %v, want %v", got, tc.wantMark)
			}
			if got := slices.Contains(vars, "LT_PARENT_SECRET=leak"); got != tc.wantLeak {
				t.Errorf("caller variable in server environment = %v, want %v", got, tc.wantLeak)
			}
		})
	}
}
