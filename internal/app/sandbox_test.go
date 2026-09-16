package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// sandboxWriteConfig writes config.toml under the LYNA_TMUX_HOME root.
func sandboxWriteConfig(t *testing.T, root, content string) {
	t.Helper()
	mkdir(t, filepath.Join(root, "config"))
	if err := os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSandboxShowEqualsLaunch starts real workspaces and compares the
// settings file each Claude pane received with what sandbox show prints.
func TestSandboxShowEqualsLaunch(t *testing.T) {
	cases := []struct {
		name   string
		config string
		launch LaunchOptions
		show   SandboxShowRequest
	}{
		{name: "configured profile with extras", config: "[sandbox]\nallowed_domains = [\"example.com\"]\ndeny_read = [\"~/secrets\"]\n"},
		{name: "strict flag", launch: LaunchOptions{Sandbox: "strict"}, show: SandboxShowRequest{Profile: "strict"}},
		{name: "off flag", config: "[claude]\nteams = true\n", launch: LaunchOptions{Sandbox: "off"}, show: SandboxShowRequest{Profile: "off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCreateEnv(t)
			if tc.config != "" {
				sandboxWriteConfig(t, e.root, tc.config)
			}
			if err := os.WriteFile(filepath.Join(e.project, "go.mod"), []byte("module x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := openServer(t, e.testHost)
			if _, err := s.Create(tmuxtest.Context(t), e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo, Launch: tc.launch}); err != nil {
				t.Fatal(err)
			}
			inv := e.invocations(t, 1)[0]
			launched, err := os.ReadFile(inv.SettingsPath)
			if err != nil {
				t.Fatal(err)
			}
			tc.show.Dir = filepath.Join(e.project, "src")
			shown, err := SandboxShow(e.Host, tc.show)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(shown, launched) {
				t.Fatalf("sandbox show differs from the launch settings\nshow:\n%s\nlaunch:\n%s", shown, launched)
			}
		})
	}
}

func TestSandboxShow(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		req     SandboxShowRequest
		wantErr error
		check   func(t *testing.T, doc map[string]any)
	}{
		{name: "process isolation without the runtime installed", req: SandboxShowRequest{Isolation: "process"}, check: func(t *testing.T, doc map[string]any) {
			sb, _ := doc["sandbox"].(map[string]any)
			env, _ := doc["env"].(map[string]any)
			if sb["enabled"] != false || env[sandbox.EnvSubprocessScrub] != "1" {
				t.Fatalf("settings %v", doc)
			}
		}},
		{name: "container isolation from the configuration", config: "[sandbox]\nisolation = \"container\"\n", check: func(t *testing.T, doc map[string]any) {
			sb, _ := doc["sandbox"].(map[string]any)
			if sb["enabled"] != true || sb["failIfUnavailable"] != nil {
				t.Fatalf("settings %v", doc)
			}
		}},
		{name: "off in the configuration is refused", config: "[sandbox]\nprofile = \"off\"\n", wantErr: ErrSandboxOffInConfig},
		{name: "unknown profile", req: SandboxShowRequest{Profile: "loose"}, wantErr: sandbox.ErrInvalid},
		{name: "unknown isolation", req: SandboxShowRequest{Isolation: "vm"}, wantErr: sandbox.ErrInvalid},
		{name: "bypass needs a boundary", config: "[claude]\npermission_mode = \"bypassPermissions\"\n", wantErr: sandbox.ErrBypassRefused},
		{name: "bypass with container isolation", config: "[claude]\npermission_mode = \"bypassPermissions\"\n", req: SandboxShowRequest{Isolation: "container"}, check: func(t *testing.T, doc map[string]any) {
			perms, _ := doc["permissions"].(map[string]any)
			if perms["disableBypassPermissionsMode"] != nil {
				t.Fatalf("permissions %v", perms)
			}
		}},
		{name: "missing directory", req: SandboxShowRequest{Dir: "/nonexistent/lyna-tmux-test"}, wantErr: os.ErrNotExist},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, nil)
			root := h.Getenv("LYNA_TMUX_HOME")
			if tc.config != "" {
				sandboxWriteConfig(t, root, tc.config)
			}
			if tc.req.Dir == "" {
				tc.req.Dir = root
			}
			got, err := SandboxShow(h, tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, got)
			}
			tc.check(t, doc)
			if _, err := os.Lstat(filepath.Join(root, "state")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sandbox show wrote the state directory: %v", err)
			}
		})
	}
}

func sandboxSys() doctor.Deps {
	return doctor.Deps{GOOS: "darwin"}
}

func TestSandboxStatusOutsideTmux(t *testing.T) {
	cases := []struct {
		name          string
		config        string
		markers       []string
		bins          map[string]string
		tmuxEnv       string
		wantProfile   sandbox.Profile
		wantIsolation sandbox.Isolation
		wantReady     bool
		wantRows      []string
		wantErr       error
	}{
		{name: "configured strict", config: "[sandbox]\nprofile = \"strict\"\n", bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}, wantProfile: sandbox.Strict, wantIsolation: sandbox.IsolationBash, wantReady: true, wantRows: []string{"sandbox"}},
		{name: "platform sandbox missing", wantProfile: sandbox.Standard, wantIsolation: sandbox.IsolationBash, wantRows: []string{"sandbox"}},
		{name: "process isolation needs the runtime", config: "[sandbox]\nisolation = \"process\"\n", bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}, wantProfile: sandbox.Standard, wantIsolation: sandbox.IsolationProcess, wantRows: []string{"sandbox", "sandbox-runtime"}},
		{name: "inside the dev container", markers: isolationDevContainerFiles(), bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}, wantProfile: sandbox.Standard, wantIsolation: sandbox.IsolationContainer, wantRows: []string{"sandbox", "docker"}},
		// A container lyna-tmux did not build confines nothing it knows
		// about, so the status keeps reporting the configured level rather
		// than claiming a boundary.
		{name: "inside an unrelated container", markers: []string{sandbox.ContainerMarkers()[0]}, bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}, wantProfile: sandbox.Standard, wantIsolation: sandbox.IsolationBash, wantReady: true, wantRows: []string{"sandbox"}},
		{name: "inside a container without the image marker", markers: []string{sandbox.ContainerMarkers()[0], sandbox.DevContainerWorkspace}, bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}, wantProfile: sandbox.Standard, wantIsolation: sandbox.IsolationBash, wantReady: true, wantRows: []string{"sandbox"}},
		{name: "a server that does not answer falls back to the configuration", tmuxEnv: "/nonexistent/lyna-tmux-test,1,0", bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}, wantProfile: sandbox.Standard, wantIsolation: sandbox.IsolationBash, wantReady: true, wantRows: []string{"sandbox"}},
		{name: "off in the configuration", config: "[sandbox]\nprofile = \"off\"\n", wantErr: ErrSandboxOffInConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolationMarkers(t, tc.markers...)
			h := isolationHost(t, tc.bins)
			root := h.Getenv("LYNA_TMUX_HOME")
			if tc.tmuxEnv != "" {
				getenv := h.Getenv
				h.Getenv = func(k string) string {
					switch k {
					case "TMUX":
						return tc.tmuxEnv
					case "TMUX_PANE":
						return "%0"
					}
					return getenv(k)
				}
			}
			if tc.config != "" {
				sandboxWriteConfig(t, root, tc.config)
			}
			project := filepath.Join(root, "src", "api")
			mkdir(t, filepath.Join(project, ".git"))
			mkdir(t, filepath.Join(project, "cmd"))
			st, err := SandboxStatus(t.Context(), h, sandboxSys(), filepath.Join(project, "cmd"))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if st.Source != SandboxSourceConfig || st.Session != "" || st.Project != project {
				t.Fatalf("state %+v", st)
			}
			if st.Sandbox.Profile != tc.wantProfile || st.Sandbox.Isolation != tc.wantIsolation || st.Ready != tc.wantReady {
				t.Fatalf("profile %s isolation %s ready %v", st.Sandbox.Profile, st.Sandbox.Isolation, st.Ready)
			}
			var ids []string
			for _, r := range st.Readiness.Results {
				ids = append(ids, r.ID)
			}
			if !slices.Equal(ids, tc.wantRows) {
				t.Fatalf("readiness rows %q, want %q", ids, tc.wantRows)
			}
		})
	}
}

// TestSandboxStatusInPane runs the status the way the generated tmux
// configuration does: from a pane of a workspace on an isolated server, with
// $TMUX and $TMUX_PANE pointing at it.
func TestSandboxStatusInPane(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	ctx := tmuxtest.Context(t)
	plan, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(h.root, "work", "api")
	mkdir(t, project)
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	sleep := []tmux.PaneProcess{{Argv: []string{"/bin/sh", "-c", "exec sleep 3600"}}}
	for _, ws := range []struct{ name, sandbox, isolation string }{
		{"guarded", "strict", "bash"},
		{"open", "off", "bash"},
		{"wrapped", "standard", "process"},
	} {
		if _, err := s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
			Session: ws.name, Project: project, Sandbox: ws.sandbox, Isolation: ws.isolation,
			Window: tmux.WindowSpec{Dir: project, Plan: plan, Procs: sleep},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A session lyna-tmux did not create, carrying the option anyway: only
	// @lt_managed makes a session ours.
	if _, err := s.Client.Run(ctx, "new-session", "-d", "-s", "plain", "-c", h.root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client.Run(ctx, "set-option", "-t", tmux.ExactSession("plain"), tmux.OptSandbox, "strict"); err != nil {
		t.Fatal(err)
	}
	socket := SocketPath(h.Getenv, s.SocketName)
	cases := []struct {
		name        string
		session     string
		wantSource  string
		wantProfile sandbox.Profile
		wantProject string
		// wantIsolation is the level the workspace recorded; empty means the
		// configured one (bash).
		wantIsolation sandbox.Isolation
	}{
		{name: "strict workspace", session: "guarded", wantSource: SandboxSourceSession, wantProfile: sandbox.Strict, wantProject: project, wantIsolation: sandbox.IsolationBash},
		{name: "workspace launched with --sandbox off", session: "open", wantSource: SandboxSourceSession, wantProfile: sandbox.Off, wantProject: project, wantIsolation: sandbox.IsolationBash},
		{name: "isolation of the workspace, not of the configuration", session: "wrapped", wantSource: SandboxSourceSession, wantProfile: sandbox.Standard, wantProject: project, wantIsolation: sandbox.IsolationProcess},
		{name: "session lyna-tmux did not create", session: "plain", wantSource: SandboxSourceConfig, wantProfile: sandbox.Standard, wantProject: h.root, wantIsolation: sandbox.IsolationBash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane, err := s.Client.Display(ctx, tmux.ExactSession(tc.session), "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			h.env["TMUX"] = socket + ",1,0"
			h.env["TMUX_PANE"] = pane
			t.Cleanup(func() { h.env["TMUX"] = "/tmp/outer,1,0"; delete(h.env, "TMUX_PANE") })
			st, err := SandboxStatus(ctx, h.Host, sandboxSys(), h.root)
			if err != nil {
				t.Fatal(err)
			}
			wantSession := tc.session
			if tc.wantSource == SandboxSourceConfig {
				wantSession = ""
			}
			if st.Source != tc.wantSource || st.Session != wantSession || st.Sandbox.Profile != tc.wantProfile || st.Project != tc.wantProject {
				t.Fatalf("state %+v", st)
			}
			if st.Sandbox.Isolation != tc.wantIsolation {
				t.Fatalf("isolation %s, want %s", st.Sandbox.Isolation, tc.wantIsolation)
			}
			if tc.wantProfile == sandbox.Strict && !slices.Contains(st.Sandbox.Domains, "registry.npmjs.org") {
				t.Fatalf("ecosystems not detected from the workspace project: %q", st.Sandbox.Domains)
			}
			if tc.wantProfile == sandbox.Off && (st.Sandbox.BashSandbox || !strings.Contains(st.Readiness.Results[0].Detail, "off")) {
				t.Fatalf("off workspace %+v", st)
			}
		})
	}
}
