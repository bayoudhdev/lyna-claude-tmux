package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode"
)

func TestResolveProfiles(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		check func(t *testing.T, r Resolution)
	}{
		{
			name: "standard defaults",
			in:   Input{},
			check: func(t *testing.T, r Resolution) {
				sb := r.Sandbox
				expect(t, "profile", r.Profile == Standard && r.Isolation == IsolationBash)
				expect(t, "enabled", sb.Enabled && sb.FailIfUnavailable)
				expect(t, "auto-allow true", sb.AutoAllowBashIfSandboxed != nil && *sb.AutoAllowBashIfSandboxed)
				expect(t, "escape hatch left to default", sb.AllowUnsandboxedCommands == nil)
				expect(t, "no network block without extras", sb.Network == nil)
				expect(t, "no filesystem block without extras", sb.Filesystem == nil)
				expect(t, "weaker nested sandbox off", !sb.EnableWeakerNestedSandbox)
				expect(t, "credential files", credentialPaths(sb) == strings.Join(CredentialFiles(), ","))
				expect(t, "credential env vars", credentialNames(sb) == strings.Join(CredentialEnvVars(), ","))
				expect(t, "ask .env", slices.Equal(r.Permissions.Ask, []string{"Read(.env)", "Read(.env.*)"}))
				expect(t, "no deny", r.Permissions.Deny == nil)
				expect(t, "reads not blocked", !r.Permissions.BlockReadsOutsideWorkingDirectories)
				expect(t, "bypass disabled", r.Permissions.DisableBypassPermissionsMode == "disable")
				expect(t, "no env scrub", len(r.Env) == 0)
			},
		},
		{
			name: "standard in container keeps bypass available",
			in:   Input{Profile: Standard, Isolation: IsolationContainer},
			check: func(t *testing.T, r Resolution) {
				expect(t, "bypass not disabled", r.Permissions.DisableBypassPermissionsMode == "")
				expect(t, "still sandboxed", r.Sandbox.Enabled)
			},
		},
		{
			name: "strict with ecosystems",
			in:   Input{Profile: Strict, Ecosystems: []Ecosystem{EcosystemNode, EcosystemGo}},
			check: func(t *testing.T, r Resolution) {
				sb := r.Sandbox
				expect(t, "auto-allow false", sb.AutoAllowBashIfSandboxed != nil && !*sb.AutoAllowBashIfSandboxed)
				expect(t, "no unsandboxed retry", sb.AllowUnsandboxedCommands != nil && !*sb.AllowUnsandboxedCommands)
				want := append(GitHubDomains(), "registry.npmjs.org", "registry.yarnpkg.com", "proxy.golang.org", "sum.golang.org")
				expect(t, "allowlist order", sb.Network != nil && slices.Equal(sb.Network.AllowedDomains, want))
				expect(t, "strict allowlist", sb.Network != nil && sb.Network.StrictAllowlist)
				expect(t, "git config readable", sb.Filesystem != nil && slices.Equal(sb.Filesystem.AllowRead, []string{"~/.gitconfig", "~/.config/git"}))
				expect(t, "deny .env", slices.Equal(r.Permissions.Deny, []string{"Read(.env)", "Read(.env.*)"}))
				expect(t, "no ask", r.Permissions.Ask == nil)
				expect(t, "reads blocked", r.Permissions.BlockReadsOutsideWorkingDirectories)
				expect(t, "bypass allowed", r.Permissions.DisableBypassPermissionsMode == "")
				expect(t, "env scrub", r.Env[EnvSubprocessScrub] == "1" && len(r.Env) == 1)
			},
		},
		{
			name: "strict without ecosystems still allows github",
			in:   Input{Profile: Strict},
			check: func(t *testing.T, r Resolution) {
				expect(t, "github only", slices.Equal(r.Sandbox.Network.AllowedDomains, GitHubDomains()))
			},
		},
		{
			name: "off disables and ignores extras",
			in: Input{Profile: Off, WeakerNestedSandbox: true, Ecosystems: []Ecosystem{EcosystemGo}, Extras: Extras{
				AllowWrite: []string{"/tmp/x"}, AllowedDomains: []string{"example.com"}, ExcludedCommands: []string{"docker *"},
			}},
			check: func(t *testing.T, r Resolution) {
				expect(t, "exactly enabled false", !r.Sandbox.Enabled && r.Sandbox.Credentials == nil &&
					r.Sandbox.Network == nil && r.Sandbox.Filesystem == nil && r.Sandbox.ExcludedCommands == nil &&
					!r.Sandbox.EnableWeakerNestedSandbox && !r.Sandbox.FailIfUnavailable)
				expect(t, "no ask or deny", r.Permissions.Ask == nil && r.Permissions.Deny == nil)
				expect(t, "bypass disabled", r.Permissions.DisableBypassPermissionsMode == "disable")
				expect(t, "no env", len(r.Env) == 0)
			},
		},
		{
			name: "weaker nested sandbox only on request",
			in:   Input{Profile: Standard, Isolation: IsolationContainer, WeakerNestedSandbox: true},
			check: func(t *testing.T, r Resolution) {
				expect(t, "set", r.Sandbox.EnableWeakerNestedSandbox)
			},
		},
		{
			name: "standard merges extras",
			in: Input{Profile: Standard, Extras: Extras{
				AllowWrite:       []string{"~/.cache/go-build/", "~/.cache/go-build", "/tmp/build"},
				DenyRead:         []string{"~/Documents"},
				AllowedDomains:   []string{"API.Example.com", "api.example.com", "*.internal.example.com"},
				ExcludedCommands: []string{"docker *", "docker *", "gh"},
			}},
			check: func(t *testing.T, r Resolution) {
				sb := r.Sandbox
				expect(t, "allow write deduped", slices.Equal(sb.Filesystem.AllowWrite, []string{"~/.cache/go-build", "/tmp/build"}))
				expect(t, "deny read", slices.Equal(sb.Filesystem.DenyRead, []string{"~/Documents"}))
				expect(t, "no allow read in standard", sb.Filesystem.AllowRead == nil)
				expect(t, "domains lowercased and deduped", slices.Equal(sb.Network.AllowedDomains, []string{"api.example.com", "*.internal.example.com"}))
				expect(t, "standard allowlist is not strict", sb.Network != nil && !sb.Network.StrictAllowlist)
				expect(t, "commands deduped", slices.Equal(sb.ExcludedCommands, []string{"docker *", "gh"}))
			},
		},
		{
			name: "strict appends user domains after presets without duplicates",
			in:   Input{Profile: Strict, Ecosystems: []Ecosystem{EcosystemRust}, Extras: Extras{AllowedDomains: []string{"crates.io", "mirror.example.org", "GITHUB.COM"}}},
			check: func(t *testing.T, r Resolution) {
				want := append(GitHubDomains(), "crates.io", "index.crates.io", "static.crates.io", "mirror.example.org")
				expect(t, "allowlist", slices.Equal(r.Sandbox.Network.AllowedDomains, want))
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

func TestResolveIsIndependentOfCallerSlices(t *testing.T) {
	extras := Extras{AllowedDomains: []string{"a.example.com"}}
	r, err := Resolve(Input{Profile: Standard, Extras: extras})
	if err != nil {
		t.Fatal(err)
	}
	extras.AllowedDomains[0] = "changed.example.com"
	if r.Sandbox.Network.AllowedDomains[0] != "a.example.com" {
		t.Fatal("resolution aliases the caller's slice")
	}
	r.Permissions.Ask[0] = "changed"
	again, _ := Resolve(Input{Profile: Standard})
	if again.Permissions.Ask[0] != "Read(.env)" {
		t.Fatal("resolution aliases a package-level list")
	}
}

func TestResolveErrors(t *testing.T) {
	long := strings.Repeat("a", maxValueBytes+1)
	many := make([]string, maxExtras+1)
	for i := range many {
		many[i] = "/tmp/x"
	}
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{name: "bad profile", in: Input{Profile: "loose"}, want: "profile must be one of"},
		{name: "bad isolation", in: Input{Isolation: "vm"}, want: "isolation must be one of"},
		{name: "unknown ecosystem", in: Input{Profile: Strict, Ecosystems: []Ecosystem{"cobol"}}, want: `unknown ecosystem "cobol"`},
		{name: "relative write path", in: Input{Extras: Extras{AllowWrite: []string{"build"}}}, want: "allow_write[1]: path must be absolute"},
		{name: "tilde without slash", in: Input{Extras: Extras{DenyRead: []string{"~secrets"}}}, want: "deny_read[1]"},
		{name: "control byte in path", in: Input{Extras: Extras{AllowWrite: []string{"/tmp/a\nb"}}}, want: "control characters"},
		{name: "empty path", in: Input{Extras: Extras{AllowWrite: []string{""}}}, want: "must not be empty"},
		{name: "oversized value", in: Input{Extras: Extras{ExcludedCommands: []string{long}}}, want: "longer than"},
		{name: "invalid utf8", in: Input{Extras: Extras{ExcludedCommands: []string{"\xff"}}}, want: "UTF-8"},
		{name: "blank command", in: Input{Extras: Extras{ExcludedCommands: []string{"   "}}}, want: "blank"},
		{name: "bare wildcard domain", in: Input{Extras: Extras{AllowedDomains: []string{"*"}}}, want: "must be a hostname"},
		{name: "url is not a domain", in: Input{Extras: Extras{AllowedDomains: []string{"https://example.com"}}}, want: "must be a hostname"},
		{name: "inner wildcard", in: Input{Extras: Extras{AllowedDomains: []string{"api.*.example.com"}}}, want: "must be a hostname"},
		{name: "too many entries", in: Input{Extras: Extras{AllowWrite: many}}, want: "at most 256 entries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resolve error = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestExtrasValidateReportsEveryProblem(t *testing.T) {
	err := Extras{AllowWrite: []string{"rel"}, AllowedDomains: []string{"bad domain"}}.Validate()
	if err == nil {
		t.Fatal("Validate accepted invalid extras")
	}
	for _, want := range []string{"allow_write[1]", "allowed_domains[1]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	if err := (Extras{AllowWrite: []string{"/ok"}}).Validate(); err != nil {
		t.Fatalf("Validate rejected valid extras: %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "absolute", in: "/tmp/build", want: "/tmp/build"},
		{name: "home", in: "~/.kube", want: "~/.kube"},
		{name: "trailing slash removed", in: "~/.aws/", want: "~/.aws"},
		{name: "several trailing slashes removed", in: "/tmp/x///", want: "/tmp/x"},
		{name: "root kept", in: "/", want: "/"},
		{name: "home root kept", in: "~/", want: "~/"},
		{name: "double slash absolute", in: "//tmp", want: "//tmp"},
		{name: "relative rejected", in: "./out", wantErr: true},
		{name: "bare tilde rejected", in: "~", wantErr: true},
		{name: "other user home rejected", in: "~bob/x", wantErr: true},
		{name: "tab rejected", in: "/tmp/\tx", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePath(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("NormalizePath(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "host", in: "registry.npmjs.org", want: "registry.npmjs.org"},
		{name: "uppercase folded", in: "Example.COM", want: "example.com"},
		{name: "wildcard", in: "*.example.com", want: "*.example.com"},
		{name: "single label", in: "localhost", want: "localhost"},
		{name: "hyphen inside", in: "my-host.example", want: "my-host.example"},
		{name: "leading hyphen", in: "-bad.example", wantErr: true},
		{name: "trailing dot", in: "example.com.", wantErr: true},
		{name: "port", in: "example.com:443", wantErr: true},
		{name: "path", in: "example.com/x", wantErr: true},
		{name: "space", in: "exa mple.com", wantErr: true},
		{name: "label too long", in: strings.Repeat("a", 64) + ".com", wantErr: true},
		{name: "name too long", in: strings.Repeat("abcdefghi.", 26) + "com", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDomain(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("NormalizeDomain(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func FuzzNormalizeDomain(f *testing.F) {
	for _, s := range []string{"example.com", "*.Example.com", "a-b.c", "", "*", "x..y", "\x00", "é.com"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := NormalizeDomain(s)
		if err != nil {
			return
		}
		if got != strings.ToLower(got) || strings.ContainsFunc(got, unicode.IsSpace) {
			t.Fatalf("accepted %q as %q: not lowercase or contains spaces", s, got)
		}
		if strings.ContainsAny(got, "/:@?#[]\\") || strings.Count(got, "*") > 1 || (strings.Contains(got, "*") && !strings.HasPrefix(got, "*.")) {
			t.Fatalf("accepted %q as %q: not a plain host pattern", s, got)
		}
		again, err := NormalizeDomain(got)
		if err != nil || again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q, %v", s, got, again, err)
		}
	})
}

func FuzzNormalizePath(f *testing.F) {
	for _, s := range []string{"/", "~/", "~/.aws/", "/tmp//", "rel", "~", "/a\nb", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := NormalizePath(s)
		if err != nil {
			return
		}
		if !strings.HasPrefix(got, "/") && !strings.HasPrefix(got, "~/") {
			t.Fatalf("accepted %q as %q without an absolute or home prefix", s, got)
		}
		if strings.ContainsFunc(got, unicode.IsControl) {
			t.Fatalf("accepted %q as %q with control characters", s, got)
		}
		if !strings.HasPrefix(s, got) {
			t.Fatalf("normalizing %q produced %q, which is not a prefix", s, got)
		}
		again, err := NormalizePath(got)
		if err != nil || again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q, %v", s, got, again, err)
		}
	})
}

func expect(t *testing.T, what string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("%s: condition failed", what)
	}
}

func credentialPaths(sb Settings) string {
	if sb.Credentials == nil {
		return ""
	}
	var parts []string
	for _, f := range sb.Credentials.Files {
		if f.Mode != CredentialModeDeny {
			return "non-deny mode"
		}
		parts = append(parts, f.Path)
	}
	return strings.Join(parts, ",")
}

func credentialNames(sb Settings) string {
	if sb.Credentials == nil {
		return ""
	}
	var parts []string
	for _, v := range sb.Credentials.EnvVars {
		if v.Mode != CredentialModeDeny {
			return "non-deny mode"
		}
		parts = append(parts, v.Name)
	}
	return strings.Join(parts, ",")
}

// TestCredentialListsArePinned writes the credential paths and variables out
// in full, so a path dropped from the source cannot pass a suite that builds
// its expectation from the same source. Every enabled profile denies these,
// and a deletion here is a deletion from every workspace.
func TestCredentialListsArePinned(t *testing.T) {
	wantFiles := []string{
		"~/.ssh",
		"~/.aws",
		"~/.gnupg",
		"~/.config/gh",
		"~/.netrc",
		"~/.docker/config.json",
		"~/.kube",
		"~/.npmrc",
		"~/.pypirc",
		"~/.config/gcloud",
		"~/.azure",
		"~/.git-credentials",
	}
	wantVars := []string{
		"ANTHROPIC_API_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AZURE_CLIENT_SECRET",
		"CARGO_REGISTRY_TOKEN",
		"CLOUDFLARE_API_TOKEN",
		"DIGITALOCEAN_ACCESS_TOKEN",
		"DOCKER_PASSWORD",
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"GITLAB_TOKEN",
		"GOOGLE_API_KEY",
		"HF_TOKEN",
		"NODE_AUTH_TOKEN",
		"NPM_TOKEN",
		"OPENAI_API_KEY",
		"PYPI_API_TOKEN",
		"SLACK_BOT_TOKEN",
		"STRIPE_SECRET_KEY",
		"TWINE_PASSWORD",
		"VAULT_TOKEN",
	}
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "credential files", got: CredentialFiles(), want: wantFiles},
		{name: "credential environment variables", got: CredentialEnvVars(), want: wantVars},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Fatalf("%s =\n%q\nwant\n%q", tc.name, tc.got, tc.want)
			}
		})
	}
	t.Run("the lists are copies", func(t *testing.T) {
		files := CredentialFiles()
		files[0] = "changed"
		if CredentialFiles()[0] != wantFiles[0] {
			t.Fatal("CredentialFiles hands out the package list itself")
		}
	})
}
