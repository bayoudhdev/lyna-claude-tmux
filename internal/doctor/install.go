package doctor

import (
	"strings"
)

// manager is a system package manager doctor can name in a fix.
type manager string

const (
	managerBrew    manager = "brew"
	managerApt     manager = "apt"
	managerDnf     manager = "dnf"
	managerPacman  manager = "pacman"
	managerApk     manager = "apk"
	managerZypper  manager = "zypper"
	managerUnknown manager = ""
)

// osReleasePath is the distribution identification file (os-release(5)).
const osReleasePath = "/etc/os-release"

// maxOSReleaseBytes bounds the os-release read; real files are under 1 KiB.
const maxOSReleaseBytes = 64 << 10

// packages names one piece of software in each package manager. brewCmd,
// when set, replaces the plain "brew install" command.
type packages struct {
	brew, apt, dnf, pacman, apk, zypper string
	brewCmd                             string
}

func same(name string) packages {
	return packages{brew: name, apt: name, dnf: name, pacman: name, apk: name, zypper: name}
}

var (
	pkgTmux       = same("tmux")
	pkgGit        = same("git")
	pkgNeovim     = same("neovim")
	pkgBubblewrap = packages{apt: "bubblewrap", dnf: "bubblewrap", pacman: "bubblewrap", apk: "bubblewrap", zypper: "bubblewrap"}
	pkgSocat      = same("socat")
	pkgRipgrep    = same("ripgrep")
	// Docker Engine ships as docker.io on Debian and Ubuntu and moby-engine on
	// Fedora; on macOS a daemon needs a VM, which colima provides from Homebrew.
	pkgDocker = packages{
		brew: "colima docker", brewCmd: "brew install colima docker && colima start",
		apt: "docker.io", dnf: "moby-engine", pacman: "docker", apk: "docker", zypper: "docker",
	}
)

// ParseOSRelease extracts ID and the ID_LIKE list from os-release content.
// Values may be quoted with single or double quotes.
func ParseOSRelease(data []byte) (id string, like []string) {
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "ID":
			id = strings.ToLower(value)
		case "ID_LIKE":
			like = strings.Fields(strings.ToLower(value))
		}
	}
	return id, like
}

var distroManagers = map[string]manager{
	"debian":   managerApt,
	"ubuntu":   managerApt,
	"fedora":   managerDnf,
	"rhel":     managerDnf,
	"centos":   managerDnf,
	"arch":     managerPacman,
	"alpine":   managerApk,
	"suse":     managerZypper,
	"opensuse": managerZypper,
	"sles":     managerZypper,
}

// packageManager picks the manager for the platform: Homebrew on macOS, the
// distribution's own manager on Linux, unknown otherwise.
func (d Deps) packageManager() manager {
	if d.GOOS == "darwin" {
		return managerBrew
	}
	if d.GOOS != "linux" {
		return managerUnknown
	}
	data, err := d.ReadFile(osReleasePath, maxOSReleaseBytes)
	if err != nil {
		return managerUnknown
	}
	id, like := ParseOSRelease(data)
	for _, name := range append([]string{id}, like...) {
		if m, ok := distroManagers[name]; ok {
			return m
		}
		// openSUSE IDs carry the edition: opensuse-tumbleweed, opensuse-leap.
		if strings.HasPrefix(name, "opensuse") {
			return managerZypper
		}
	}
	return managerUnknown
}

// installCommand is the command that installs p with the platform's manager.
// An unknown Linux distribution gets the two most common commands.
func (d Deps) installCommand(p packages) string {
	switch d.packageManager() {
	case managerBrew:
		switch {
		case p.brewCmd != "":
			return p.brewCmd
		case p.brew == "":
			return ""
		}
		return "brew install " + p.brew
	case managerApt:
		return "sudo apt-get install " + p.apt
	case managerDnf:
		return "sudo dnf install " + p.dnf
	case managerPacman:
		return "sudo pacman -S " + p.pacman
	case managerApk:
		return "sudo apk add " + p.apk
	case managerZypper:
		return "sudo zypper install " + p.zypper
	default:
		if d.GOOS != "linux" {
			return ""
		}
		return "sudo apt-get install " + p.apt + " (Debian, Ubuntu) or sudo dnf install " + p.dnf + " (Fedora)"
	}
}
