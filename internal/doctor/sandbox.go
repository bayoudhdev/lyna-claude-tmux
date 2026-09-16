package doctor

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"sync"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
)

// Paths doctor reads to judge the Linux sandbox.
const (
	procVersionPath = "/proc/version"
	usernsPath      = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	bwrapProfile    = "/etc/apparmor.d/bwrap"
	maxProcBytes    = 4 << 10
)

// fixAppArmorBwrap grants bubblewrap user namespaces on Ubuntu 24.04 and
// later, verbatim from the Claude Code sandboxing documentation.
const fixAppArmorBwrap = `sudo tee /etc/apparmor.d/bwrap > /dev/null <<'EOF'
abi <abi/4.0>,
include <tunables/global>

profile bwrap /usr/bin/bwrap flags=(unconfined) {
  userns,
  include if exists <local/bwrap>
}
EOF
sudo systemctl reload apparmor`

// WSL describes a Windows Subsystem for Linux kernel.
type WSL int

// WSL generations.
const (
	WSLNone WSL = iota
	WSL1
	WSL2
)

// DetectWSL classifies a /proc/version string. WSL2 kernels are built by
// Microsoft as "*-microsoft-standard-WSL2"; WSL1 reports a translated kernel
// named "*-Microsoft".
func DetectWSL(procVersion string) WSL {
	v := strings.ToLower(procVersion)
	switch {
	case !strings.Contains(v, "microsoft"):
		return WSLNone
	case strings.Contains(v, "wsl2") || strings.Contains(v, "microsoft-standard"):
		return WSL2
	default:
		return WSL1
	}
}

// SandboxChecks runs only the checks that decide whether the configured
// sandbox profile and isolation level can start on this machine: the
// platform sandbox, the sandbox runtime for process isolation and Docker for
// container isolation. The sandbox status view shows them as readiness.
func SandboxChecks(ctx context.Context, d Deps) []Result {
	d = d.withDefaults()
	out := append(checkSandbox(ctx, d), checkSandboxRuntime(ctx, d)...)
	if d.Isolation == "container" {
		out = append(out, checkDocker(ctx, d)...)
	}
	return out
}

// checkSandboxRuntime reports the sandbox runtime that process isolation
// wraps Claude in, and ripgrep, which the runtime needs on Linux to find the
// paths it must protect. Other isolation levels do not use it and get no row.
func checkSandboxRuntime(_ context.Context, d Deps) []Result {
	if d.Isolation != "process" {
		return nil
	}
	r := Result{ID: "sandbox-runtime", Title: "Sandbox runtime"}
	path, err := d.LookPath(sandbox.RuntimeBinary)
	if err != nil {
		r.Status = StatusFail
		r.Detail = sandbox.RuntimeBinary + ` is not on PATH; sandbox.isolation = "process" runs Claude inside it`
		r.Fix = sandbox.RuntimeInstall + " (needs Node.js 20.11 or newer)"
	} else {
		r.Status, r.Detail = StatusOK, sandbox.RuntimeBinary+" at "+path
	}
	out := []Result{r}
	if d.GOOS == "linux" {
		out = append(out, lookPathResult(d, "sandbox-ripgrep", "ripgrep", "rg", pkgRipgrep))
	}
	return out
}

func checkSandbox(_ context.Context, d Deps) []Result {
	if d.SandboxProfile == "off" {
		return []Result{{ID: "sandbox", Title: "Sandbox", Status: StatusSkip, Detail: `sandbox.profile = "off"`}}
	}
	switch d.GOOS {
	case "darwin":
		return checkSeatbelt(d)
	case "linux":
		return checkLinuxSandbox(d)
	default:
		return []Result{{
			ID: "sandbox", Title: "Sandbox", Status: StatusFail,
			Detail: "the Claude Code sandbox supports macOS, Linux and WSL2, not " + d.GOOS,
			Fix:    `set sandbox.profile = "off" in config.toml and pass --sandbox off`,
		}}
	}
}

func checkSeatbelt(d Deps) []Result {
	r := Result{ID: "sandbox", Title: "Sandbox (Seatbelt)"}
	path, err := d.LookPath("sandbox-exec")
	if err != nil {
		r.Status = StatusFail
		r.Detail = "sandbox-exec is not on PATH; macOS ships it in /usr/bin"
		r.Fix = `export PATH="/usr/bin:$PATH"`
		return []Result{r}
	}
	r.Status, r.Detail = StatusOK, "sandbox-exec at "+path
	return []Result{r}
}

func checkLinuxSandbox(d Deps) []Result {
	var out []Result
	if r, ok := checkWSL(d); ok {
		out = append(out, r)
	}
	out = append(out,
		lookPathResult(d, "sandbox-bwrap", "bubblewrap", "bwrap", pkgBubblewrap),
		lookPathResult(d, "sandbox-socat", "socat", "socat", pkgSocat),
		checkUserns(d),
	)
	if d.Exists("/.dockerenv") || d.Exists("/run/.containerenv") {
		out = append(out, Result{
			ID: "sandbox-container", Title: "Container", Status: StatusWarn,
			Detail: "running inside a container, where bubblewrap cannot mount a fresh /proc without extra privileges",
			Fix:    `Claude settings: "sandbox": {"enableWeakerNestedSandbox": true} (only when the container is the isolation boundary)`,
		})
	}
	return out
}

func lookPathResult(d Deps, id, title, bin string, p packages) Result {
	r := Result{ID: id, Title: title}
	path, err := d.LookPath(bin)
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, bin+" is not on PATH; the Linux sandbox needs it", d.installCommand(p)
		return r
	}
	r.Status, r.Detail = StatusOK, bin+" at "+path
	return r
}

func checkWSL(d Deps) (Result, bool) {
	data, err := d.ReadFile(procVersionPath, maxProcBytes)
	kind := WSLNone
	if err == nil {
		kind = DetectWSL(string(data))
	}
	if kind == WSLNone && d.Getenv("WSL_DISTRO_NAME") == "" {
		return Result{}, false
	}
	r := Result{ID: "sandbox-wsl", Title: "WSL"}
	switch kind {
	case WSL1:
		r.Status = StatusFail
		r.Detail = "WSL1 cannot run bubblewrap; the sandbox needs WSL2"
		r.Fix = "wsl --set-version " + powerShellWord(d.Getenv("WSL_DISTRO_NAME")) + " 2 (run in PowerShell; list distributions with wsl -l -v)"
	case WSL2:
		r.Status, r.Detail = StatusOK, "WSL2"
	default:
		r.Status, r.Detail = StatusWarn, "WSL_DISTRO_NAME is set but /proc/version does not name a WSL kernel"
		r.Fix = "wsl -l -v (run in PowerShell; the VERSION column must be 2)"
	}
	return r, true
}

var plainWord = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._-]+$`) })

// powerShellWord quotes a distribution name for a PowerShell command line.
func powerShellWord(name string) string {
	if name == "" {
		return "<distribution>"
	}
	if plainWord().MatchString(name) {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

func checkUserns(d Deps) Result {
	r := Result{ID: "sandbox-userns", Title: "User namespaces"}
	data, err := d.ReadFile(usernsPath, maxProcBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.Status, r.Detail = StatusOK, "no AppArmor restriction on unprivileged user namespaces"
	case err != nil:
		r.Status, r.Detail = StatusWarn, "cannot read "+usernsPath+": "+err.Error()
		r.Fix = "sysctl kernel.apparmor_restrict_unprivileged_userns (1 means bubblewrap needs the AppArmor profile)"
	case strings.TrimSpace(string(data)) == "1" && d.Exists(bwrapProfile):
		r.Status, r.Detail = StatusOK, "AppArmor restricts unprivileged user namespaces; "+bwrapProfile+" grants bubblewrap an exception"
	case strings.TrimSpace(string(data)) == "1":
		r.Status = StatusFail
		r.Detail = "AppArmor restricts unprivileged user namespaces, so bubblewrap cannot isolate commands"
		r.Fix = fixAppArmorBwrap
	default:
		r.Status, r.Detail = StatusOK, "unprivileged user namespaces allowed"
	}
	return r
}
