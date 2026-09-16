package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

func TestResolveIsolation(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		check func(t *testing.T, r Resolution)
	}{
		{
			name: "bash keeps the sandbox required and has no runtime",
			in:   Input{Profile: Standard, Isolation: IsolationBash},
			check: func(t *testing.T, r Resolution) {
				expect(t, "required", r.Sandbox.Enabled && r.Sandbox.FailIfUnavailable)
				expect(t, "no runtime", r.Runtime == nil)
			},
		},
		{
			name: "process moves the boundary to the runtime",
			in:   Input{Profile: Standard, Isolation: IsolationProcess, Ecosystems: []Ecosystem{EcosystemGo}},
			check: func(t *testing.T, r Resolution) {
				expect(t, "bash sandbox off", !r.Sandbox.Enabled && r.Sandbox.Credentials == nil && !r.Sandbox.FailIfUnavailable)
				expect(t, "subprocess env scrubbed", r.Env[EnvSubprocessScrub] == "1")
				expect(t, "runtime set", r.Runtime != nil)
				want := slices.Concat(ClaudeDomains(), GitHubDomains(), []string{"proxy.golang.org", "sum.golang.org"})
				expect(t, "allowlist", slices.Equal(r.Runtime.Network.AllowedDomains, want))
				expect(t, "credential files denied", slices.Equal(r.Runtime.Filesystem.DenyRead, CredentialFiles()))
				expect(t, "no extra writes", len(r.Runtime.Filesystem.AllowWrite) == 0)
				expect(t, ".env still asks", slices.Equal(r.Permissions.Ask, []string{"Read(.env)", "Read(.env.*)"}))
				expect(t, "bypass still refused", r.Permissions.DisableBypassPermissionsMode == "disable")
			},
		},
		{
			name: "process strict keeps the strict permissions and merges extras",
			in: Input{Profile: Strict, Isolation: IsolationProcess, Extras: Extras{
				AllowWrite: []string{"~/.cache/go-build"}, DenyRead: []string{"~/Documents", "~/.ssh"}, AllowedDomains: []string{"mirror.example.org", "github.com"},
				ExcludedCommands: []string{"docker *"},
			}},
			check: func(t *testing.T, r Resolution) {
				expect(t, "deny .env", slices.Equal(r.Permissions.Deny, []string{"Read(.env)", "Read(.env.*)"}))
				expect(t, "reads blocked", r.Permissions.BlockReadsOutsideWorkingDirectories)
				expect(t, "bypass allowed", r.Permissions.DisableBypassPermissionsMode == "")
				expect(t, "scrub", r.Env[EnvSubprocessScrub] == "1" && len(r.Env) == 1)
				want := slices.Concat(ClaudeDomains(), GitHubDomains(), []string{"mirror.example.org"})
				expect(t, "domains deduped", slices.Equal(r.Runtime.Network.AllowedDomains, want))
				expect(t, "deny read deduped", slices.Equal(r.Runtime.Filesystem.DenyRead, append(CredentialFiles(), "~/Documents")))
				expect(t, "allow write", slices.Equal(r.Runtime.Filesystem.AllowWrite, []string{"~/.cache/go-build"}))
				expect(t, "no excluded commands anywhere", r.Sandbox.ExcludedCommands == nil)
			},
		},
		{
			name: "container does not require the bash sandbox and keeps credential denies",
			in:   Input{Profile: Standard, Isolation: IsolationContainer},
			check: func(t *testing.T, r Resolution) {
				expect(t, "enabled", r.Sandbox.Enabled)
				expect(t, "not required", !r.Sandbox.FailIfUnavailable)
				expect(t, "credential files", credentialPaths(r.Sandbox) == strings.Join(CredentialFiles(), ","))
				expect(t, "credential env vars", credentialNames(r.Sandbox) == strings.Join(CredentialEnvVars(), ","))
				expect(t, "bypass allowed", r.Permissions.DisableBypassPermissionsMode == "" && CheckBypass(r.Profile, r.Isolation, PermissionModeBypass) == nil)
				expect(t, "no runtime", r.Runtime == nil)
			},
		},
		{
			name: "container strict keeps its allowlist",
			in:   Input{Profile: Strict, Isolation: IsolationContainer},
			check: func(t *testing.T, r Resolution) {
				expect(t, "not required", r.Sandbox.Enabled && !r.Sandbox.FailIfUnavailable)
				expect(t, "strict allowlist", r.Sandbox.Network != nil && r.Sandbox.Network.StrictAllowlist)
			},
		},
		{
			name: "container off stays off",
			in:   Input{Profile: Off, Isolation: IsolationContainer},
			check: func(t *testing.T, r Resolution) {
				expect(t, "off", !r.Sandbox.Enabled && r.Runtime == nil)
				expect(t, "bypass allowed", r.Permissions.DisableBypassPermissionsMode == "")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Resolve(tc.in)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			tc.check(t, r)
		})
	}
}

func TestResolveRefusesOffWithProcessIsolation(t *testing.T) {
	_, err := Resolve(Input{Profile: Off, Isolation: IsolationProcess})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "process isolation") {
		t.Fatalf("err = %v", err)
	}
}

func TestRuntimeForLaunch(t *testing.T) {
	res, err := Resolve(Input{
		Profile: Strict, Isolation: IsolationProcess, Ecosystems: []Ecosystem{EcosystemNode},
		Extras: Extras{AllowWrite: []string{"/tmp/build", "/home/u/src/api"}, DenyRead: []string{"~/Documents"}, AllowedDomains: []string{"*.internal.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch := RuntimeLaunch{
		Home:     "/home/u",
		Project:  "/home/u/src/api",
		Writable: []string{"/home/u/.claude", "/home/u/.claude.json", "/home/u/.local/state/lyna-tmux"},
		Sockets:  []string{"/private/tmp/tmux-501/lyna-tmux"},
	}
	cases := []struct {
		name    string
		launch  RuntimeLaunch
		golden  string
		wantErr string
	}{
		{name: "strict with node and extras", launch: launch, golden: "runtime/strict-node.golden"},
		{name: "no sockets leaves the field out", launch: RuntimeLaunch{Home: "/home/u", Project: "/p"}, golden: "runtime/no-sockets.golden"},
		{name: "a trailing slash on the home directory", launch: RuntimeLaunch{Home: "/home/u/", Project: "/p"}, golden: "runtime/no-sockets.golden"},
		{name: "relative project", launch: RuntimeLaunch{Home: "/home/u", Project: "src"}, wantErr: "project must be an absolute path"},
		{name: "home-relative writable path", launch: RuntimeLaunch{Home: "/home/u", Project: "/p", Writable: []string{"~/.claude"}}, wantErr: "writable path must be an absolute path"},
		{name: "control byte in socket", launch: RuntimeLaunch{Home: "/home/u", Project: "/p", Sockets: []string{"/tmp/a\nb"}}, wantErr: "socket must be an absolute path"},
		{name: "no home directory", launch: RuntimeLaunch{Project: "/p"}, wantErr: `denied read path "~/.ssh" is written against the home directory`},
		{name: "home-relative home directory", launch: RuntimeLaunch{Home: "~/u", Project: "/p"}, wantErr: "which is not an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := res.Runtime.ForLaunch(tc.launch)
			if tc.wantErr != "" {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := MarshalRuntime(rt)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, tc.golden, data)
			if !json.Valid(data) || !bytes.HasSuffix(data, []byte("}\n")) {
				t.Fatalf("not a JSON document with a trailing newline:\n%s", data)
			}
		})
	}
	// A credential file the runtime does not resolve is a credential file it
	// hands out, so nothing home-relative may reach the settings file.
	t.Run("the credential denies are machine paths", func(t *testing.T) {
		rt, err := res.Runtime.ForLaunch(launch)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range rt.Filesystem.DenyRead {
			if !strings.HasPrefix(p, "/") {
				t.Fatalf("denyRead keeps a home-relative entry: %q", rt.Filesystem.DenyRead)
			}
		}
		for _, want := range []string{"/home/u/.ssh", "/home/u/.config/gh", "/home/u/Documents"} {
			if !slices.Contains(rt.Filesystem.DenyRead, want) {
				t.Fatalf("denyRead = %q, want %s", rt.Filesystem.DenyRead, want)
			}
		}
	})
	t.Run("a trailing slash is the same path", func(t *testing.T) {
		rt, err := res.Runtime.ForLaunch(RuntimeLaunch{
			Home:     "/home/u",
			Project:  "/home/u/src/api/",
			Writable: []string{"/home/u/.claude/", "/home/u/.claude"},
			Sockets:  []string{"/tmp/lyna-tmux/", "/tmp/lyna-tmux"},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range rt.Filesystem.AllowWrite {
			if strings.HasSuffix(p, "/") {
				t.Fatalf("allowWrite keeps a trailing slash: %q", rt.Filesystem.AllowWrite)
			}
		}
		if got := rt.Filesystem.AllowWrite; len(got) != 3 {
			t.Fatalf("allowWrite = %q, want the project, the configuration directory and the extra once each", got)
		}
		if got := rt.Network.AllowUnixSockets; len(got) != 1 || got[0] != "/tmp/lyna-tmux" {
			t.Fatalf("allowUnixSockets = %q", got)
		}
	})
	t.Run("does not alias the policy", func(t *testing.T) {
		rt, err := res.Runtime.ForLaunch(launch)
		if err != nil {
			t.Fatal(err)
		}
		rt.Network.AllowedDomains[0] = "changed.example"
		rt.Filesystem.DenyRead[0] = "/changed"
		if res.Runtime.Network.AllowedDomains[0] == "changed.example" || res.Runtime.Filesystem.DenyRead[0] == "/changed" {
			t.Fatal("ForLaunch aliases the resolution's lists")
		}
	})
}

// TestMarshalRuntimeRequiredFields pins the fields the runtime's settings
// schema requires: a missing one makes srt refuse to start.
func TestMarshalRuntimeRequiredFields(t *testing.T) {
	data, err := MarshalRuntime(Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ section, field string }{
		{"network", "allowedDomains"},
		{"network", "deniedDomains"},
		{"filesystem", "denyRead"},
		{"filesystem", "allowWrite"},
		{"filesystem", "denyWrite"},
	}
	for _, tc := range cases {
		t.Run(tc.section+"."+tc.field, func(t *testing.T) {
			if got := string(doc[tc.section][tc.field]); got != "[]" {
				t.Fatalf("%s.%s = %q, want []", tc.section, tc.field, got)
			}
		})
	}
	if _, ok := doc["network"]["allowUnixSockets"]; ok {
		t.Fatal("empty allowUnixSockets written")
	}
}

func TestClaudeDomainsIsACopy(t *testing.T) {
	d := ClaudeDomains()
	d[0] = "changed"
	if ClaudeDomains()[0] != "api.anthropic.com" {
		t.Fatal("ClaudeDomains exposes the package list")
	}
	for _, host := range ClaudeDomains() {
		if _, err := NormalizeDomain(host); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
	}
}
