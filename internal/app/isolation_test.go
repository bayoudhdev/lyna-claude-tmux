package app

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
)

// isolationMarkers makes the listed absolute paths look present, and every
// other one absent, for the rest of the test. The isolation checks read
// nothing else to decide what kind of container this is.
func isolationMarkers(t *testing.T, files ...string) {
	t.Helper()
	saved := containerMarkerExists
	containerMarkerExists = func(path string) bool { return slices.Contains(files, path) }
	t.Cleanup(func() { containerMarkerExists = saved })
}

// isolationDevContainerFiles is the full evidence of the lyna-tmux dev
// container: a runtime marker, the marker its image writes and the workspace
// mount. Anything less is some other container.
func isolationDevContainerFiles() []string {
	return []string{sandbox.ContainerMarkers()[0], sandbox.DevContainerMarker, sandbox.DevContainerWorkspace}
}

// isolationInsideContainer makes the process look as if it runs inside the
// lyna-tmux dev container (or outside any container) for the rest of the test.
func isolationInsideContainer(t *testing.T, inside bool) {
	t.Helper()
	if !inside {
		isolationMarkers(t)
		return
	}
	isolationMarkers(t, isolationDevContainerFiles()...)
}

// isolationHost is a host without tmux whose PATH lookups resolve from a map.
func isolationHost(t *testing.T, bins map[string]string) Host {
	t.Helper()
	root := t.TempDir()
	env := map[string]string{"LYNA_TMUX_HOME": root, "CLAUDE_CONFIG_DIR": filepath.Join(root, "claude-home")}
	return Host{
		Getenv: func(k string) string { return env[k] },
		Home:   root,
		Exe:    filepath.Join(root, "lyna-tmux"),
		LookPath: func(name string) (string, error) {
			if p, ok := bins[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
	}
}

func TestIsolationAvailable(t *testing.T) {
	docker := sandbox.ContainerMarkers()[0]
	podman := sandbox.ContainerMarkers()[1]
	cases := []struct {
		name      string
		isolation sandbox.Isolation
		bins      map[string]string
		markers   []string
		wantErr   string
	}{
		{name: "bash always", isolation: sandbox.IsolationBash},
		{name: "process with the runtime on PATH", isolation: sandbox.IsolationProcess, bins: map[string]string{"srt": "/opt/bin/srt"}},
		{name: "process without the runtime", isolation: sandbox.IsolationProcess, wantErr: "install it with `npm install -g @anthropic-ai/sandbox-runtime`"},
		{name: "process with a relative runtime path", isolation: sandbox.IsolationProcess, bins: map[string]string{"srt": "bin/srt"}, wantErr: "srt is not on PATH"},
		{name: "container outside a container", isolation: sandbox.IsolationContainer, wantErr: "lyna-tmux sandbox devcontainer up"},
		{name: "container inside the dev container", isolation: sandbox.IsolationContainer, markers: isolationDevContainerFiles()},
		{
			name: "container inside the dev container on an oci runtime", isolation: sandbox.IsolationContainer,
			markers: []string{podman, sandbox.DevContainerMarker, sandbox.DevContainerWorkspace},
		},
		{
			name: "container inside an unrelated container", isolation: sandbox.IsolationContainer,
			markers: []string{docker},
			wantErr: sandbox.DevContainerMarker + " is missing",
		},
		{
			name: "container inside a container holding a workspace", isolation: sandbox.IsolationContainer,
			markers: []string{docker, sandbox.DevContainerWorkspace},
			wantErr: sandbox.DevContainerMarker + " is missing",
		},
		{
			name: "container without the workspace mount", isolation: sandbox.IsolationContainer,
			markers: []string{docker, sandbox.DevContainerMarker},
			wantErr: sandbox.DevContainerWorkspace + " is missing",
		},
		{
			name: "the image marker alone is not a container", isolation: sandbox.IsolationContainer,
			markers: []string{sandbox.DevContainerMarker, sandbox.DevContainerWorkspace},
			wantErr: "lyna-tmux sandbox devcontainer up",
		},
		{name: "unknown level", isolation: "vm", wantErr: "vm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolationMarkers(t, tc.markers...)
			err := isolationAvailable(isolationHost(t, tc.bins), tc.isolation)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrIsolationUnsupported) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want ErrIsolationUnsupported containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestIsolationIsolate(t *testing.T) {
	resolve := func(t *testing.T, in sandbox.Input) sandbox.Resolution {
		t.Helper()
		r, err := sandbox.Resolve(in)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	socket := "/private/tmp/tmux-501/lt-test"
	cmd := claudecfg.Command{
		Argv: []string{"/opt/claude", "--settings=/state/settings/a.json", "-c"},
		Env:  []string{session.EnvManaged + "=1", session.EnvSocket + "=" + socket},
	}
	srt := map[string]string{"srt": "/opt/bin/srt"}
	cases := []struct {
		name    string
		res     func(t *testing.T) sandbox.Resolution
		bins    map[string]string
		cmd     claudecfg.Command
		wrapped bool
		wantErr string
	}{
		{name: "bash is unchanged", res: func(t *testing.T) sandbox.Resolution { return resolve(t, sandbox.Input{}) }, bins: srt, cmd: cmd},
		{name: "container is unchanged", res: func(t *testing.T) sandbox.Resolution {
			return resolve(t, sandbox.Input{Isolation: sandbox.IsolationContainer})
		}, bins: srt, cmd: cmd},
		{name: "process runs under the runtime", res: func(t *testing.T) sandbox.Resolution {
			return resolve(t, sandbox.Input{Profile: sandbox.Strict, Isolation: sandbox.IsolationProcess, Extras: sandbox.Extras{AllowWrite: []string{"/tmp/build"}}})
		}, bins: srt, cmd: cmd, wrapped: true},
		{name: "process without the runtime", res: func(t *testing.T) sandbox.Resolution {
			return resolve(t, sandbox.Input{Isolation: sandbox.IsolationProcess})
		}, cmd: cmd, wantErr: "srt is not on PATH"},
		{name: "process resolution without runtime settings", res: func(*testing.T) sandbox.Resolution {
			return sandbox.Resolution{Profile: sandbox.Standard, Isolation: sandbox.IsolationProcess}
		}, bins: srt, cmd: cmd, wantErr: "without sandbox runtime settings"},
		{name: "process without the socket variable", res: func(t *testing.T) sandbox.Resolution {
			return resolve(t, sandbox.Input{Isolation: sandbox.IsolationProcess})
		}, bins: srt, cmd: claudecfg.Command{Argv: cmd.Argv}, wantErr: session.EnvSocket},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, tc.bins)
			root := filepath.Join(h.Home, "src", "api")
			got, err := isolate(h, tc.res(t), root, tc.cmd)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Env, tc.cmd.Env) {
				t.Fatalf("env changed: %q", got.Env)
			}
			if !tc.wrapped {
				if !slices.Equal(got.Argv, tc.cmd.Argv) {
					t.Fatalf("argv changed: %q", got.Argv)
				}
				return
			}
			n := len(got.Argv)
			if n != len(cmd.Argv)+4 || got.Argv[0] != "/opt/bin/srt" || got.Argv[1] != "--settings" || got.Argv[3] != "--" || !slices.Equal(got.Argv[4:], cmd.Argv) {
				t.Fatalf("argv %q", got.Argv)
			}
			settingsDir := filepath.Join(h.Home, "state", "settings")
			if filepath.Dir(got.Argv[2]) != settingsDir {
				t.Fatalf("runtime settings in %s, want %s", got.Argv[2], settingsDir)
			}
			assertMode(t, got.Argv[2], 0o600)
			assertMode(t, settingsDir, 0o700)
			rt := isolationReadRuntime(t, got.Argv[2])
			writable := []string{root, filepath.Join(h.Home, "claude-home"), filepath.Join(h.Home, ".claude.json"), filepath.Join(h.Home, "state"), "/tmp/build"}
			if !slices.Equal(rt.Filesystem.AllowWrite, writable) {
				t.Fatalf("allowWrite %q, want %q", rt.Filesystem.AllowWrite, writable)
			}
			if !slices.Equal(rt.Network.AllowUnixSockets, []string{socket}) {
				t.Fatalf("allowUnixSockets %q", rt.Network.AllowUnixSockets)
			}
			// The credential files reach the runtime as machine paths under
			// this launch's home, not as the "~/" entries the policy names.
			denied := sandbox.CredentialFiles()
			for i, p := range denied {
				denied[i] = filepath.Join(h.Home, strings.TrimPrefix(p, "~/"))
			}
			if !slices.Equal(rt.Filesystem.DenyRead, denied) || !slices.Contains(rt.Network.AllowedDomains, "api.anthropic.com") {
				t.Fatalf("runtime policy %+v, want denyRead %q", rt, denied)
			}
		})
	}
}

func isolationReadRuntime(t *testing.T, path string) sandbox.Runtime {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rt sandbox.Runtime
	if err := json.Unmarshal(data, &rt); err != nil {
		t.Fatalf("runtime settings: %v\n%s", err, data)
	}
	return rt
}

// TestIsolationProcessLaunch starts a workspace at process isolation on an
// isolated tmux server. The runtime is a script that records its arguments
// and settings file, checks the argument layout the real runtime parses, and
// hands over to the fake claude, which records its own launch.
func TestIsolationProcessLaunch(t *testing.T) {
	e := newCreateEnv(t)
	record := filepath.Join(e.root, "srt.log")
	srt := filepath.Join(e.root, "bin", "srt")
	mkdir(t, filepath.Dir(srt))
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + record + "'; done\n" +
		"[ \"$1\" = --settings ] || exit 64\n" +
		"cp \"$2\" '" + record + ".settings' || exit 65\n" +
		"[ \"$3\" = -- ] || exit 66\n" +
		"shift 3\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(srt, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	lookup := e.LookPath
	e.LookPath = func(name string) (string, error) {
		if name == sandbox.RuntimeBinary {
			return srt, nil
		}
		return lookup(name)
	}
	mkdir(t, filepath.Join(e.project, "node_modules"))
	if err := os.WriteFile(filepath.Join(e.project, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	res, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo, Launch: LaunchOptions{Isolation: "process"}})
	if err != nil {
		t.Fatal(err)
	}
	inv := e.invocations(t, 1)[0]
	args := isolationReadLines(t, record)

	t.Run("runtime argument layout", func(t *testing.T) {
		if len(args) < 4 || args[0] != "--settings" || args[2] != "--" || args[3] != inv.Program {
			t.Fatalf("srt args %q, claude program %q", args, inv.Program)
		}
		if !slices.Equal(args[4:], inv.Args) {
			t.Fatalf("claude args after -- %q, claude saw %q", args[4:], inv.Args)
		}
		assertMode(t, args[1], 0o600)
		if filepath.Dir(args[1]) != s.Paths.SettingsDir() {
			t.Fatalf("runtime settings file %s outside %s", args[1], s.Paths.SettingsDir())
		}
	})
	t.Run("runtime settings", func(t *testing.T) {
		rt := isolationReadRuntime(t, record+".settings")
		if rt.Filesystem.AllowWrite[0] != e.project || !slices.Contains(rt.Filesystem.AllowWrite, s.Paths.State) {
			t.Fatalf("allowWrite %q", rt.Filesystem.AllowWrite)
		}
		if !slices.Equal(rt.Network.AllowUnixSockets, []string{inv.Env[session.EnvSocket]}) {
			t.Fatalf("allowUnixSockets %q, claude socket %q", rt.Network.AllowUnixSockets, inv.Env[session.EnvSocket])
		}
		if !slices.Contains(rt.Network.AllowedDomains, "registry.npmjs.org") {
			t.Fatalf("detected node ecosystem missing from %q", rt.Network.AllowedDomains)
		}
	})
	t.Run("claude settings leave the boundary to the runtime", func(t *testing.T) {
		var doc struct {
			Env     map[string]string `json:"env"`
			Sandbox map[string]any    `json:"sandbox"`
		}
		if err := json.Unmarshal(inv.Settings, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Sandbox["enabled"] != false || len(doc.Sandbox) != 1 || doc.Env[sandbox.EnvSubprocessScrub] != "1" {
			t.Fatalf("settings %s", inv.Settings)
		}
	})
	t.Run("workspace records the profile", func(t *testing.T) {
		sessions, err := s.Sessions(ctx)
		if err != nil || len(sessions) != 1 || sessions[0].Name != res.Name || sessions[0].Sandbox != "standard" {
			t.Fatalf("sessions %+v, %v", sessions, err)
		}
	})
}

// isolationReadLines waits for the runtime script's argument record.
func isolationReadLines(t *testing.T, path string) []string {
	t.Helper()
	var lines []string
	tmuxtest.WaitFor(t, "sandbox runtime started", func() bool {
		data, err := os.ReadFile(path + ".settings")
		if err != nil || len(data) == 0 {
			return false
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		lines = strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		return true
	})
	return lines
}
