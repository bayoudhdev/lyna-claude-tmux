package doctor

import (
	"errors"
	"slices"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	cases := []struct {
		name string
		in   string
		id   string
		like []string
	}{
		{"ubuntu", osReleaseUbuntu, "ubuntu", []string{"debian"}},
		{"fedora without ID_LIKE", osReleaseFedora, "fedora", nil},
		{"quoted values and several parents", "ID='rocky'\nID_LIKE=\"rhel centos fedora\"\n", "rocky", []string{"rhel", "centos", "fedora"}},
		{"mixed case and spaces", "  ID = Arch \n", "arch", nil},
		{"comments and junk", "# ID=debian\njunk\n=\nID=alpine", "alpine", nil},
		{"empty", "", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, like := ParseOSRelease([]byte(tc.in))
			if id != tc.id || !slices.Equal(like, tc.like) {
				t.Fatalf("ParseOSRelease() = %q, %v; want %q, %v", id, like, tc.id, tc.like)
			}
		})
	}
}

func TestInstallCommand(t *testing.T) {
	cases := []struct {
		name      string
		goos      string
		osRelease string
		readErr   error
		pkg       packages
		want      string
	}{
		{"macOS", "darwin", "", nil, pkgTmux, "brew install tmux"},
		{"macOS docker needs a VM", "darwin", "", nil, pkgDocker, "brew install colima docker && colima start"},
		{"macOS has no bubblewrap", "darwin", "", nil, pkgBubblewrap, ""},
		{"debian", "linux", "ID=debian\n", nil, pkgSocat, "sudo apt-get install socat"},
		{"ubuntu docker", "linux", osReleaseUbuntu, nil, pkgDocker, "sudo apt-get install docker.io"},
		{"linux mint through ID_LIKE", "linux", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n", nil, pkgGit, "sudo apt-get install git"},
		{"fedora docker", "linux", osReleaseFedora, nil, pkgDocker, "sudo dnf install moby-engine"},
		{"rocky through rhel", "linux", "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", nil, pkgNeovim, "sudo dnf install neovim"},
		{"arch", "linux", "ID=arch\n", nil, pkgBubblewrap, "sudo pacman -S bubblewrap"},
		{"alpine", "linux", "ID=alpine\n", nil, pkgTmux, "sudo apk add tmux"},
		{"opensuse tumbleweed", "linux", "ID=opensuse-tumbleweed\nID_LIKE=\"opensuse suse\"\n", nil, pkgTmux, "sudo zypper install tmux"},
		{"opensuse edition without ID_LIKE", "linux", "ID=opensuse-leap\n", nil, pkgTmux, "sudo zypper install tmux"},
		{"unknown distribution", "linux", "ID=nixos\n", nil, pkgTmux, "sudo apt-get install tmux (Debian, Ubuntu) or sudo dnf install tmux (Fedora)"},
		{"unreadable os-release", "linux", "", errors.New("permission denied"), pkgSocat, "sudo apt-get install socat (Debian, Ubuntu) or sudo dnf install socat (Fedora)"},
		{"other platform", "freebsd", "", nil, pkgTmux, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sys := fakeSystem{goos: tc.goos, files: map[string]string{}}
			if tc.osRelease != "" {
				sys.files[osReleasePath] = tc.osRelease
			}
			if tc.readErr != nil {
				sys.errs = map[string]error{osReleasePath: tc.readErr}
			}
			if got := sys.deps().installCommand(tc.pkg); got != tc.want {
				t.Fatalf("installCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}
