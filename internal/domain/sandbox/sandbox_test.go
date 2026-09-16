package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseProfile(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Profile
		wantErr bool
	}{
		{name: "empty selects standard", in: "", want: Standard},
		{name: "standard", in: "standard", want: Standard},
		{name: "strict", in: "strict", want: Strict},
		{name: "off", in: "off", want: Off},
		{name: "case sensitive", in: "Strict", wantErr: true},
		{name: "unknown", in: "paranoid", wantErr: true},
		{name: "padded", in: " off", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseProfile(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("ParseProfile(%q) error = %v, want ErrInvalid", tc.in, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseProfile(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestParseIsolation(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Isolation
		wantErr bool
	}{
		{name: "empty selects bash", in: "", want: IsolationBash},
		{name: "bash", in: "bash", want: IsolationBash},
		{name: "process", in: "process", want: IsolationProcess},
		{name: "container", in: "container", want: IsolationContainer},
		{name: "unknown", in: "vm", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseIsolation(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("ParseIsolation(%q) error = %v, want ErrInvalid", tc.in, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseIsolation(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestListsStartWithDefaults(t *testing.T) {
	if got := Profiles(); len(got) != 3 || got[0] != Standard {
		t.Fatalf("Profiles() = %v, want standard first of 3", got)
	}
	if got := Isolations(); len(got) != 3 || got[0] != IsolationBash {
		t.Fatalf("Isolations() = %v, want bash first of 3", got)
	}
}

func TestCheckBypass(t *testing.T) {
	cases := []struct {
		name      string
		profile   Profile
		isolation Isolation
		mode      string
		refused   bool
	}{
		{name: "standard bash bypass refused", profile: Standard, isolation: IsolationBash, mode: PermissionModeBypass, refused: true},
		{name: "standard process bypass refused", profile: Standard, isolation: IsolationProcess, mode: PermissionModeBypass, refused: true},
		{name: "off bash bypass refused", profile: Off, isolation: IsolationBash, mode: PermissionModeBypass, refused: true},
		{name: "strict bash bypass allowed", profile: Strict, isolation: IsolationBash, mode: PermissionModeBypass},
		{name: "standard container bypass allowed", profile: Standard, isolation: IsolationContainer, mode: PermissionModeBypass},
		{name: "off container bypass allowed", profile: Off, isolation: IsolationContainer, mode: PermissionModeBypass},
		{name: "standard auto mode passes", profile: Standard, isolation: IsolationBash, mode: "auto"},
		{name: "off empty mode passes", profile: Off, isolation: IsolationBash, mode: ""},
		{name: "near miss is not bypass", profile: Off, isolation: IsolationBash, mode: "bypasspermissions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckBypass(tc.profile, tc.isolation, tc.mode)
			if got := errors.Is(err, ErrBypassRefused); got != tc.refused {
				t.Fatalf("CheckBypass(%s, %s, %q) = %v, refused want %v", tc.profile, tc.isolation, tc.mode, err, tc.refused)
			}
			if !tc.refused && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestInContainer(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{name: "host", want: false},
		{name: "docker", files: []string{"/.dockerenv"}, want: true},
		{name: "oci runtime", files: []string{"/run/.containerenv"}, want: true},
		{name: "unrelated file", files: []string{"/etc/hostname"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InContainer(func(p string) bool { return slices.Contains(tc.files, p) })
			if got != tc.want {
				t.Fatalf("InContainer(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}

func TestDetectContainer(t *testing.T) {
	docker, podman := ContainerMarkers()[0], ContainerMarkers()[1]
	cases := []struct {
		name   string
		files  []string
		want   ContainerEvidence
		dev    bool
		reason string
	}{
		{name: "host", reason: "does not run inside a container"},
		{
			name:  "the dev container lyna-tmux builds",
			files: []string{docker, DevContainerMarker, DevContainerWorkspace},
			want:  ContainerEvidence{Runtime: true, Image: true, Workspace: true},
			dev:   true,
		},
		{
			name:  "the dev container on an oci runtime",
			files: []string{podman, DevContainerMarker, DevContainerWorkspace},
			want:  ContainerEvidence{Runtime: true, Image: true, Workspace: true},
			dev:   true,
		},
		{
			name:   "an unrelated container",
			files:  []string{docker},
			want:   ContainerEvidence{Runtime: true},
			reason: DevContainerMarker + " is missing",
		},
		{
			name:   "a ci job with a workspace directory",
			files:  []string{docker, DevContainerWorkspace},
			want:   ContainerEvidence{Runtime: true, Workspace: true},
			reason: DevContainerMarker + " is missing",
		},
		{
			name:   "the image without its workspace mount",
			files:  []string{docker, DevContainerMarker},
			want:   ContainerEvidence{Runtime: true, Image: true},
			reason: DevContainerWorkspace + " is missing",
		},
		{
			name:   "the marker outside any container",
			files:  []string{DevContainerMarker, DevContainerWorkspace},
			want:   ContainerEvidence{Image: true, Workspace: true},
			reason: "does not run inside a container",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectContainer(func(p string) bool { return slices.Contains(tc.files, p) })
			if got != tc.want {
				t.Fatalf("DetectContainer(%v) = %+v, want %+v", tc.files, got, tc.want)
			}
			if got.DevContainer() != tc.dev {
				t.Fatalf("DevContainer() = %v, want %v", got.DevContainer(), tc.dev)
			}
			if tc.dev {
				if got.Reason() != "" {
					t.Fatalf("Reason() = %q, want none", got.Reason())
				}
				return
			}
			if !strings.Contains(got.Reason(), tc.reason) {
				t.Fatalf("Reason() = %q, want it to name %q", got.Reason(), tc.reason)
			}
		})
	}
}

func TestDetectEcosystems(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []Ecosystem
	}{
		{name: "empty project", want: nil},
		{name: "go module", files: []string{"go.mod"}, want: []Ecosystem{EcosystemGo}},
		{name: "python via requirements", files: []string{"requirements.txt"}, want: []Ecosystem{EcosystemPython}},
		{name: "python both markers once", files: []string{"pyproject.toml", "requirements.txt"}, want: []Ecosystem{EcosystemPython}},
		{
			name:  "every ecosystem in fixed order",
			files: []string{"Gemfile", "Cargo.toml", "go.mod", "package.json", "pyproject.toml"},
			want:  []Ecosystem{EcosystemNode, EcosystemGo, EcosystemRust, EcosystemPython, EcosystemRuby},
		},
		{name: "nested manifest ignored", files: []string{"web/package.json"}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectEcosystems(func(rel string) bool { return slices.Contains(tc.files, rel) })
			if !slices.Equal(got, tc.want) {
				t.Fatalf("DetectEcosystems(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}

func TestEcosystemDomains(t *testing.T) {
	cases := []struct {
		name string
		in   Ecosystem
		want []string
	}{
		{name: "node", in: EcosystemNode, want: []string{"registry.npmjs.org", "registry.yarnpkg.com"}},
		{name: "go", in: EcosystemGo, want: []string{"proxy.golang.org", "sum.golang.org"}},
		{name: "rust", in: EcosystemRust, want: []string{"crates.io", "index.crates.io", "static.crates.io"}},
		{name: "python", in: EcosystemPython, want: []string{"pypi.org", "files.pythonhosted.org"}},
		{name: "ruby", in: EcosystemRuby, want: []string{"rubygems.org", "index.rubygems.org"}},
		{name: "unknown", in: "cobol", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EcosystemDomains(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("EcosystemDomains(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for _, d := range got {
				if n, err := NormalizeDomain(d); err != nil || n != d {
					t.Fatalf("built-in domain %q is not normalized: %q, %v", d, n, err)
				}
			}
		})
	}
}

func TestBuiltinListsAreDefensiveCopies(t *testing.T) {
	cases := []struct {
		name string
		get  func() []string
	}{
		{name: "github domains", get: GitHubDomains},
		{name: "credential files", get: CredentialFiles},
		{name: "credential env vars", get: CredentialEnvVars},
		{name: "ecosystem domains", get: func() []string { return EcosystemDomains(EcosystemGo) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.get()
			first[0] = "mutated"
			if tc.get()[0] == "mutated" {
				t.Fatal("caller mutation leaked into the built-in list")
			}
		})
	}
}

func TestBuiltinListsAreValid(t *testing.T) {
	for _, p := range CredentialFiles() {
		if n, err := NormalizePath(p); err != nil || n != p {
			t.Errorf("credential file %q: %q, %v", p, n, err)
		}
	}
	for _, d := range GitHubDomains() {
		if n, err := NormalizeDomain(d); err != nil || n != d {
			t.Errorf("github domain %q: %q, %v", d, n, err)
		}
	}
	names := CredentialEnvVars()
	if !slices.IsSorted(names) {
		t.Errorf("credential env vars are not sorted: %v", names)
	}
	for _, n := range names {
		if !envNamePattern(n) {
			t.Errorf("credential env var %q is not a valid variable name", n)
		}
	}
}

func envNamePattern(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return s != ""
}
