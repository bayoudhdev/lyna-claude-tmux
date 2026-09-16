package sandbox

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestDescribe(t *testing.T) {
	strictNode := append(GitHubDomains(), "registry.npmjs.org", "registry.yarnpkg.com")
	cases := []struct {
		name string
		in   Input
		want Summary
	}{
		{
			name: "standard",
			in:   Input{Profile: Standard},
			want: Summary{
				Profile: Standard, Isolation: IsolationBash, Boundary: IsolationBash.Boundary(),
				BashSandbox: true, RequiresSandbox: true,
				DeniedFiles: CredentialFiles(), DeniedEnvVars: CredentialEnvVars(),
				EnvFiles: EnvFilesAsk, Network: NetworkAsk, Domains: []string{}, WritablePaths: []string{},
				UnsandboxedFallback: FallbackAsk,
			},
		},
		{
			name: "standard with extras",
			in:   Input{Profile: Standard, Extras: Extras{DenyRead: []string{"~/.ssh", "~/Documents"}, AllowWrite: []string{"/tmp/b"}, AllowedDomains: []string{"example.com"}}},
			want: Summary{
				Profile: Standard, Isolation: IsolationBash, Boundary: IsolationBash.Boundary(),
				BashSandbox: true, RequiresSandbox: true,
				DeniedFiles: append(CredentialFiles(), "~/Documents"), DeniedEnvVars: CredentialEnvVars(),
				EnvFiles: EnvFilesAsk, Network: NetworkAsk, Domains: []string{"example.com"}, WritablePaths: []string{"/tmp/b"},
				UnsandboxedFallback: FallbackAsk,
			},
		},
		{
			name: "strict",
			in:   Input{Profile: Strict, Ecosystems: []Ecosystem{EcosystemNode}},
			want: Summary{
				Profile: Strict, Isolation: IsolationBash, Boundary: IsolationBash.Boundary(),
				BashSandbox: true, RequiresSandbox: true, ScrubsSubprocessEnv: true, BlocksReadsOutsideProject: true,
				DeniedFiles: CredentialFiles(), DeniedEnvVars: CredentialEnvVars(),
				EnvFiles: EnvFilesDeny, Network: NetworkAllowlist, Domains: strictNode, WritablePaths: []string{},
				UnsandboxedFallback: FallbackNever, BypassAllowed: true,
			},
		},
		{
			name: "off",
			in:   Input{Profile: Off},
			want: Summary{
				Profile: Off, Isolation: IsolationBash, Boundary: IsolationBash.Boundary(),
				DeniedFiles: []string{}, DeniedEnvVars: []string{}, Domains: []string{}, WritablePaths: []string{},
				EnvFiles: EnvFilesAllow, Network: NetworkOpen, UnsandboxedFallback: FallbackUnsandboxed,
			},
		},
		{
			name: "process",
			in:   Input{Profile: Standard, Isolation: IsolationProcess, Extras: Extras{AllowWrite: []string{"/tmp/b"}}},
			want: Summary{
				Profile: Standard, Isolation: IsolationProcess, Boundary: IsolationProcess.Boundary(),
				ScrubsSubprocessEnv: true, DeniedFiles: CredentialFiles(), DeniedEnvVars: []string{},
				EnvFiles: EnvFilesAsk, Network: NetworkAllowlist, Domains: slices.Concat(ClaudeDomains(), GitHubDomains()),
				WritablePaths: []string{"/tmp/b"}, UnsandboxedFallback: FallbackNever,
			},
		},
		{
			name: "container",
			in:   Input{Profile: Standard, Isolation: IsolationContainer},
			want: Summary{
				Profile: Standard, Isolation: IsolationContainer, Boundary: IsolationContainer.Boundary(),
				BashSandbox: true, DeniedFiles: CredentialFiles(), DeniedEnvVars: CredentialEnvVars(),
				EnvFiles: EnvFilesAsk, Network: NetworkAsk, Domains: []string{}, WritablePaths: []string{},
				UnsandboxedFallback: FallbackAsk, BypassAllowed: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Resolve(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(Describe(r))
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("summary\n got  %s\n want %s", got, want)
			}
		})
	}
}

func TestIsolationBoundaryIsDistinct(t *testing.T) {
	seen := map[string]Isolation{}
	for _, i := range Isolations() {
		b := i.Boundary()
		if prev, ok := seen[b]; ok || b == "" {
			t.Fatalf("%s and %s share boundary %q", prev, i, b)
		}
		seen[b] = i
	}
}

// TestDescribeReadsTheEnvRules pins where the .env line of the summary comes
// from: the rules that name .env files, not the presence of any rule.
func TestDescribeReadsTheEnvRules(t *testing.T) {
	cases := []struct {
		name  string
		perms Permissions
		want  string
	}{
		{name: "no rules", want: EnvFilesAllow},
		{name: "an unrelated ask rule", perms: Permissions{Ask: []string{"Bash(git push:*)"}}, want: EnvFilesAllow},
		{name: "an unrelated deny rule", perms: Permissions{Deny: []string{"Read(~/.aws/**)"}}, want: EnvFilesAllow},
		{name: "the env files are asked about", perms: Permissions{Ask: []string{"Bash(git push:*)", "Read(.env)", "Read(.env.*)"}}, want: EnvFilesAsk},
		{name: "the env files are denied", perms: Permissions{Deny: []string{"Read(.env)"}}, want: EnvFilesDeny},
		{
			name:  "a deny outranks an ask",
			perms: Permissions{Ask: []string{"Read(.env)"}, Deny: []string{"Read(.env.*)"}},
			want:  EnvFilesDeny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Describe(Resolution{Permissions: tc.perms}).EnvFiles; got != tc.want {
				t.Fatalf("EnvFiles = %q, want %q", got, tc.want)
			}
		})
	}
}
