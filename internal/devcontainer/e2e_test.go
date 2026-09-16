package devcontainer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDockerLifecycleE2E renders the files into a scratch project, builds the
// image, starts the container with the firewall, checks the tools inside and
// tears everything down. It needs a working Docker engine, network access and
// a published lyna-tmux release, so it runs only when LYNA_TMUX_E2E_DOCKER=1.
func TestDockerLifecycleE2E(t *testing.T) {
	if os.Getenv("LYNA_TMUX_E2E_DOCKER") != "1" {
		t.Skip("set LYNA_TMUX_E2E_DOCKER=1 to build and run the dev container (needs Docker and network access)")
	}
	bin, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not on PATH")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()

	dir := t.TempDir()
	project := "e2e-" + strings.ToLower(time.Now().UTC().Format("20060102t150405"))
	files, err := Render(Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, files, false); err != nil {
		t.Fatal(err)
	}

	target := Target{Dir: dir, Project: project, User: DefaultUser}
	d := Docker{Bin: bin, Exec: Exec{Stdout: testWriter{t}, Stderr: testWriter{t}}}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		if err := d.Down(cleanup, target); err != nil {
			t.Errorf("down: %v", err)
		}
		for _, argv := range [][]string{{bin, "volume", "rm", target.Volume()}, {bin, "image", "rm", target.Image()}} {
			if out, err := exec.CommandContext(cleanup, argv[0], argv[1:]...).CombinedOutput(); err != nil {
				t.Errorf("%v: %v\n%s", argv[1:3], err, out)
			}
		}
	})

	if err := d.Up(ctx, target); err != nil {
		t.Fatalf("up: %v", err)
	}
	state, err := d.State(ctx, target)
	if err != nil || state != StateRunning {
		t.Fatalf("state = %q, %v", state, err)
	}
	// Up is idempotent on a running container and reapplies the firewall.
	if err := d.Up(ctx, target); err != nil {
		t.Fatalf("second up: %v", err)
	}

	inContainer := func(args ...string) (string, error) {
		argv := append([]string{bin, "exec", "--user", target.User, target.Container()}, args...)
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
		return string(out), err
	}
	checks := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "lyna-tmux installed", args: []string{"lyna-tmux", "version"}, want: "lyna-tmux"},
		{name: "claude installed", args: []string{"claude", "--version"}, want: "Claude Code"},
		{name: "tmux installed", args: []string{"tmux", "-V"}, want: "tmux 3."},
		{name: "runs as the container user", args: []string{"id", "-un"}, want: target.User},
		{name: "claude config in the volume", args: []string{"sh", "-c", `printf %s "$CLAUDE_CONFIG_DIR"`}, want: "/home/" + target.User + "/.claude"},
		{name: "workspace mounted", args: []string{"test", "-f", "/workspace/" + FileDockerfile}},
		{name: "docker socket absent", args: []string{"test", "-e", "/var/run/docker.sock"}, wantErr: true},
		{name: "blocked egress", args: []string{"curl", "--silent", "--max-time", "10", "https://example.com"}, wantErr: true},
		{name: "allowed egress", args: []string{"curl", "--silent", "--output", "/dev/null", "--max-time", "15", "https://api.anthropic.com"}},
		{name: "no-new-privileges stops sudo", args: []string{"sudo", "-n", FirewallPath}, wantErr: true},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			out, err := inContainer(c.args...)
			if (err != nil) != c.wantErr || !strings.Contains(out, c.want) {
				t.Fatalf("%q: err = %v, output %q, want %q (error expected: %v)", c.args, err, out, c.want, c.wantErr)
			}
		})
	}
}

// testWriter streams docker build and firewall output into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
