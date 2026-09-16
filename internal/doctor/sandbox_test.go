package doctor

import (
	"errors"
	"reflect"
	"testing"
)

func TestDetectWSL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want WSL
	}{
		{"wsl1", procWSL1, WSL1},
		{"wsl2", procWSL2, WSL2},
		{"wsl2 custom kernel naming WSL2", "Linux version 6.6.36.3-microsoft-custom-WSL2", WSL2},
		{"native linux", procLinux, WSLNone},
		{"empty", "", WSLNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectWSL(tc.in); got != tc.want {
				t.Fatalf("DetectWSL() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPowerShellWord(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "<distribution>"},
		{"Ubuntu-24.04", "Ubuntu-24.04"},
		{"My Distro", "'My Distro'"},
		{"it's", "'it''s'"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := powerShellWord(tc.in); got != tc.want {
				t.Fatalf("powerShellWord(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCheckSandbox(t *testing.T) {
	linuxBins := map[string]string{"bwrap": "/usr/bin/bwrap", "socat": "/usr/bin/socat"}
	ubuntu := func(extra map[string]string) map[string]string {
		m := map[string]string{osReleasePath: osReleaseUbuntu, procVersionPath: procLinux}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	bwrapOK := Result{ID: "sandbox-bwrap", Title: "bubblewrap", Status: StatusOK, Detail: "bwrap at /usr/bin/bwrap"}
	socatOK := Result{ID: "sandbox-socat", Title: "socat", Status: StatusOK, Detail: "socat at /usr/bin/socat"}
	usernsOK := Result{ID: "sandbox-userns", Title: "User namespaces", Status: StatusOK, Detail: "no AppArmor restriction on unprivileged user namespaces"}
	runCheckCases(t, checkSandbox, []checkCase{
		{
			name: "profile off",
			sys:  fakeSystem{goos: "linux"},
			edit: func(d *Deps) { d.SandboxProfile = "off" },
			want: []Result{{ID: "sandbox", Title: "Sandbox", Status: StatusSkip, Detail: `sandbox.profile = "off"`}},
		},
		{
			name: "macOS seatbelt present",
			sys:  fakeSystem{goos: "darwin", bins: map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec"}},
			want: []Result{{ID: "sandbox", Title: "Sandbox (Seatbelt)", Status: StatusOK, Detail: "sandbox-exec at /usr/bin/sandbox-exec"}},
		},
		{
			name: "macOS seatbelt missing from PATH",
			sys:  fakeSystem{goos: "darwin"},
			want: []Result{{
				ID: "sandbox", Title: "Sandbox (Seatbelt)", Status: StatusFail,
				Detail: "sandbox-exec is not on PATH; macOS ships it in /usr/bin", Fix: `export PATH="/usr/bin:$PATH"`,
			}},
		},
		{
			name: "unsupported platform",
			sys:  fakeSystem{goos: "windows"},
			want: []Result{{
				ID: "sandbox", Title: "Sandbox", Status: StatusFail,
				Detail: "the Claude Code sandbox supports macOS, Linux and WSL2, not windows",
				Fix:    `set sandbox.profile = "off" in config.toml and pass --sandbox off`,
			}},
		},
		{
			name: "linux ready",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: ubuntu(map[string]string{usernsPath: "0\n"})},
			want: []Result{bwrapOK, socatOK, {ID: "sandbox-userns", Title: "User namespaces", Status: StatusOK, Detail: "unprivileged user namespaces allowed"}},
		},
		{
			name: "linux without userns sysctl",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: ubuntu(nil)},
			want: []Result{bwrapOK, socatOK, usernsOK},
		},
		{
			name: "ubuntu 24.04 missing packages and restricted userns",
			sys:  fakeSystem{goos: "linux", files: ubuntu(map[string]string{usernsPath: "1\n"})},
			want: []Result{
				{ID: "sandbox-bwrap", Title: "bubblewrap", Status: StatusFail, Detail: "bwrap is not on PATH; the Linux sandbox needs it", Fix: "sudo apt-get install bubblewrap"},
				{ID: "sandbox-socat", Title: "socat", Status: StatusFail, Detail: "socat is not on PATH; the Linux sandbox needs it", Fix: "sudo apt-get install socat"},
				{ID: "sandbox-userns", Title: "User namespaces", Status: StatusFail, Detail: "AppArmor restricts unprivileged user namespaces, so bubblewrap cannot isolate commands", Fix: fixAppArmorBwrap},
			},
		},
		{
			name: "restricted userns with the bwrap profile installed",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: ubuntu(map[string]string{usernsPath: "1"}), exists: map[string]bool{bwrapProfile: true}},
			want: []Result{bwrapOK, socatOK, {ID: "sandbox-userns", Title: "User namespaces", Status: StatusOK, Detail: "AppArmor restricts unprivileged user namespaces; /etc/apparmor.d/bwrap grants bubblewrap an exception"}},
		},
		{
			name: "unreadable userns sysctl",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: ubuntu(nil), errs: map[string]error{usernsPath: errors.New("permission denied")}},
			want: []Result{bwrapOK, socatOK, {
				ID: "sandbox-userns", Title: "User namespaces", Status: StatusWarn,
				Detail: "cannot read /proc/sys/kernel/apparmor_restrict_unprivileged_userns: permission denied",
				Fix:    "sysctl kernel.apparmor_restrict_unprivileged_userns (1 means bubblewrap needs the AppArmor profile)",
			}},
		},
		{
			name: "wsl1",
			sys: fakeSystem{
				goos: "linux", bins: linuxBins, env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu-22.04"},
				files: map[string]string{procVersionPath: procWSL1},
			},
			want: []Result{
				{ID: "sandbox-wsl", Title: "WSL", Status: StatusFail, Detail: "WSL1 cannot run bubblewrap; the sandbox needs WSL2", Fix: "wsl --set-version Ubuntu-22.04 2 (run in PowerShell; list distributions with wsl -l -v)"},
				bwrapOK, socatOK, usernsOK,
			},
		},
		{
			name: "wsl2 detected from the kernel alone",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: map[string]string{procVersionPath: procWSL2}},
			want: []Result{{ID: "sandbox-wsl", Title: "WSL", Status: StatusOK, Detail: "WSL2"}, bwrapOK, socatOK, usernsOK},
		},
		{
			name: "wsl variable without a wsl kernel",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, env: map[string]string{"WSL_DISTRO_NAME": "Debian"}},
			want: []Result{
				{ID: "sandbox-wsl", Title: "WSL", Status: StatusWarn, Detail: "WSL_DISTRO_NAME is set but /proc/version does not name a WSL kernel", Fix: "wsl -l -v (run in PowerShell; the VERSION column must be 2)"},
				bwrapOK, socatOK, usernsOK,
			},
		},
		{
			name: "inside a docker container",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: ubuntu(nil), exists: map[string]bool{"/.dockerenv": true}},
			want: []Result{bwrapOK, socatOK, usernsOK, {
				ID: "sandbox-container", Title: "Container", Status: StatusWarn,
				Detail: "running inside a container, where bubblewrap cannot mount a fresh /proc without extra privileges",
				Fix:    `Claude settings: "sandbox": {"enableWeakerNestedSandbox": true} (only when the container is the isolation boundary)`,
			}},
		},
		{
			name: "inside a podman container",
			sys:  fakeSystem{goos: "linux", bins: linuxBins, files: ubuntu(nil), exists: map[string]bool{"/run/.containerenv": true}},
			want: []Result{bwrapOK, socatOK, usernsOK, {
				ID: "sandbox-container", Title: "Container", Status: StatusWarn,
				Detail: "running inside a container, where bubblewrap cannot mount a fresh /proc without extra privileges",
				Fix:    `Claude settings: "sandbox": {"enableWeakerNestedSandbox": true} (only when the container is the isolation boundary)`,
			}},
		},
	})
}

func TestCheckSandboxRuntime(t *testing.T) {
	process := func(d *Deps) { d.Isolation = "process" }
	srtOK := Result{ID: "sandbox-runtime", Title: "Sandbox runtime", Status: StatusOK, Detail: "srt at /usr/local/bin/srt"}
	srtMissing := Result{
		ID: "sandbox-runtime", Title: "Sandbox runtime", Status: StatusFail,
		Detail: `srt is not on PATH; sandbox.isolation = "process" runs Claude inside it`,
		Fix:    "npm install -g @anthropic-ai/sandbox-runtime (needs Node.js 20.11 or newer)",
	}
	runCheckCases(t, checkSandboxRuntime, []checkCase{
		{name: "bash isolation has no row", sys: fakeSystem{goos: "darwin", bins: map[string]string{"srt": "/usr/local/bin/srt"}}},
		{name: "container isolation has no row", sys: fakeSystem{goos: "linux"}, edit: func(d *Deps) { d.Isolation = "container" }},
		{name: "macOS with srt", sys: fakeSystem{goos: "darwin", bins: map[string]string{"srt": "/usr/local/bin/srt"}}, edit: process, want: []Result{srtOK}},
		{name: "macOS without srt", sys: fakeSystem{goos: "darwin"}, edit: process, want: []Result{srtMissing}},
		{
			name: "linux also needs ripgrep", sys: fakeSystem{goos: "linux", bins: map[string]string{"srt": "/usr/local/bin/srt"}, files: map[string]string{osReleasePath: osReleaseFedora}},
			edit: process,
			want: []Result{srtOK, {ID: "sandbox-ripgrep", Title: "ripgrep", Status: StatusFail, Detail: "rg is not on PATH; the Linux sandbox needs it", Fix: "sudo dnf install ripgrep"}},
		},
		{
			name: "linux ready", sys: fakeSystem{goos: "linux", bins: map[string]string{"srt": "/usr/local/bin/srt", "rg": "/usr/bin/rg"}},
			edit: process,
			want: []Result{srtOK, {ID: "sandbox-ripgrep", Title: "ripgrep", Status: StatusOK, Detail: "rg at /usr/bin/rg"}},
		},
	})
}

func TestSandboxChecks(t *testing.T) {
	seatbelt := Result{ID: "sandbox", Title: "Sandbox (Seatbelt)", Status: StatusOK, Detail: "sandbox-exec at /usr/bin/sandbox-exec"}
	mac := fakeSystem{
		goos:    "darwin",
		bins:    map[string]string{"sandbox-exec": "/usr/bin/sandbox-exec", "docker": "/usr/local/bin/docker", "srt": "/opt/homebrew/bin/srt", "tmux": "/opt/homebrew/bin/tmux"},
		outputs: map[string]fakeOutput{"/usr/local/bin/docker info --format {{.ServerVersion}}": {out: "28.4.0\n"}},
	}
	cases := []struct {
		name      string
		isolation string
		profile   string
		want      []Result
	}{
		{name: "bash runs only the platform sandbox", isolation: "bash", want: []Result{seatbelt}},
		{name: "process adds the runtime", isolation: "process", want: []Result{seatbelt, {ID: "sandbox-runtime", Title: "Sandbox runtime", Status: StatusOK, Detail: "srt at /opt/homebrew/bin/srt"}}},
		{name: "container adds docker", isolation: "container", want: []Result{seatbelt, {ID: "docker", Title: "Docker", Status: StatusOK, Detail: "Docker server 28.4.0, client at /usr/local/bin/docker"}}},
		{name: "off profile skips the platform sandbox", isolation: "bash", profile: "off", want: []Result{{ID: "sandbox", Title: "Sandbox", Status: StatusSkip, Detail: `sandbox.profile = "off"`}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := mac.deps()
			d.Isolation = tc.isolation
			if tc.profile != "" {
				d.SandboxProfile = tc.profile
			}
			// A partially filled Deps must not panic: SandboxChecks applies the defaults itself.
			d.Timeout = 0
			if got := SandboxChecks(t.Context(), d); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}
