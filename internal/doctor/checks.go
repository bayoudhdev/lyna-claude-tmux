package doctor

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// Fix strings shared by checks.
const (
	fixInstallClaude = "curl -fsSL https://claude.ai/install.sh | bash"
	fixUpdateClaude  = "claude update"
)

// Version is a dotted numeric version such as 2.1.273 or 0.11.
type Version struct {
	Major, Minor, Patch int
}

var versionPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(\d{1,9})\.(\d{1,9})(?:\.(\d{1,9}))?`) })

// ParseVersion finds the first dotted version in s: "2.1.273 (Claude Code)",
// "NVIM v0.11.4" or "git version 2.50.1".
func ParseVersion(s string) (Version, bool) {
	m := versionPattern().FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	// The pattern bounds each number to 9 digits, so Atoi cannot overflow.
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch := 0
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	return Version{Major: major, Minor: minor, Patch: patch}, true
}

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// firstLine returns the first non-empty trimmed line of s.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func checkTmux(ctx context.Context, d Deps) []Result {
	r := Result{ID: "tmux", Title: "tmux"}
	path, err := d.LookPath("tmux")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, "tmux is not on PATH", d.installCommand(pkgTmux)
		return []Result{r}
	}
	out, err := d.output(ctx, path, "-V")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, fmt.Sprintf("%s -V failed: %v", path, err), d.installCommand(pkgTmux)
		return []Result{r}
	}
	v, err := tmux.ParseVersion(out)
	if err != nil {
		r.Status, r.Detail = StatusWarn, fmt.Sprintf("cannot read the tmux version from %q", firstLine(out))
		return []Result{r}
	}
	if !v.Supported() {
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("tmux %s at %s is older than %d.%d; if the distribution package is still older, build a release from https://github.com/tmux/tmux/releases",
			v, path, tmux.MinMajor, tmux.MinMinor)
		r.Fix = d.installCommand(pkgTmux)
		return []Result{r}
	}
	r.Status, r.Detail = StatusOK, fmt.Sprintf("tmux %s at %s", v, path)
	return []Result{r}
}

func checkClaude(ctx context.Context, d Deps) []Result {
	r := Result{ID: "claude", Title: "Claude Code"}
	path, err := d.LookPath("claude")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, "claude is not on PATH", fixInstallClaude
		return []Result{r}
	}
	out, err := d.output(ctx, path, "--version")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, fmt.Sprintf("%s --version failed: %v", path, err), fixInstallClaude
		return []Result{r}
	}
	v, ok := ParseVersion(out)
	if !ok {
		r.Status, r.Detail = StatusWarn, fmt.Sprintf("cannot read the Claude Code version from %q", firstLine(out))
		return []Result{r}
	}
	if d.ClaudeMinVersion != "" {
		minimum, ok := ParseVersion(d.ClaudeMinVersion)
		if ok && v.Less(minimum) {
			r.Status, r.Detail, r.Fix = StatusFail, fmt.Sprintf("Claude Code %s at %s is older than %s", v, path, minimum), fixUpdateClaude
			r.Action = CommandFix("Update Claude Code", path, "update")
			return []Result{r}
		}
	}
	r.Status, r.Detail = StatusOK, fmt.Sprintf("Claude Code %s at %s", v, path)
	return []Result{r}
}

func checkGit(ctx context.Context, d Deps) []Result {
	r := Result{ID: "git", Title: "git"}
	path, err := d.LookPath("git")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, "git is not on PATH; the changes pane, review and worktrees need it", d.installCommand(pkgGit)
		return []Result{r}
	}
	out, err := d.output(ctx, path, "--version")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusFail, fmt.Sprintf("%s --version failed: %v", path, err), d.installCommand(pkgGit)
		return []Result{r}
	}
	v, ok := ParseVersion(out)
	switch {
	case !ok:
		r.Status = StatusWarn
		r.Detail = fmt.Sprintf("cannot read the git version from %q; the workspace needs %d.%d or newer",
			firstLine(out), gitMinMajor, gitMinMinor)
		r.Fix = d.installCommand(pkgGit)
	case v.Less(Version{Major: gitMinMajor, Minor: gitMinMinor}):
		r.Status = StatusWarn
		r.Detail = fmt.Sprintf("git %s at %s is older than %d.%d, which is where `git worktree list` learned to separate its records with NUL; if the distribution package is still older, install a newer one from https://git-scm.com/downloads",
			v, path, gitMinMajor, gitMinMinor)
		r.Fix = d.installCommand(pkgGit)
	default:
		r.Status = StatusOK
		r.Detail = fmt.Sprintf("git %s at %s, past the %d.%d the worktree list, the interactive rebase and the force-with-lease push need",
			v, path, gitMinMajor, gitMinMinor)
	}
	return []Result{r}
}

func checkTruecolor(_ context.Context, d Deps) []Result {
	r := Result{ID: "truecolor", Title: "Truecolor"}
	info := termx.Detect(d.Getenv)
	if info.Truecolor {
		r.Status, r.Detail = StatusOK, "24-bit color in "+info.Program.Name()
		return []Result{r}
	}
	r.Status = StatusWarn
	r.Detail = "the terminal does not advertise 24-bit color, so lyna-tmux quantizes the theme to 256 colors"
	r.Fix = `export COLORTERM=truecolor (only when the terminal renders 24-bit color), or set ui.color = "256" in config.toml`
	return []Result{r}
}

func checkClipboard(_ context.Context, d Deps) []Result {
	r := Result{ID: "clipboard", Title: "Clipboard"}
	if c, ok := termx.DetectClipboard(d.GOOS, d.Getenv, d.LookPath); ok {
		r.Status, r.Detail = StatusOK, fmt.Sprintf("%s at %s", c.Tool, c.Path)
		return []Result{r}
	}
	r.Status = StatusWarn
	r.Detail = "no clipboard tool found (" + strings.Join(termx.ClipboardCandidates(d.GOOS, d.Getenv), ", ") + "), so copy mode cannot write the system clipboard"
	switch {
	case d.GOOS == "darwin":
		r.Fix = "export PATH=\"/usr/bin:$PATH\" (pbcopy ships with macOS in /usr/bin)"
	case termx.Detect(d.Getenv).WSL:
		r.Fix = "set [interop] enabled = true in /etc/wsl.conf, then run wsl --shutdown from PowerShell"
	default:
		name := termx.ClipboardPackage(d.Getenv)
		r.Fix = d.installCommand(same(name))
	}
	return []Result{r}
}

func checkOptionAsMeta(_ context.Context, d Deps) []Result {
	r := Result{ID: "option-meta", Title: "Option as Meta"}
	if !d.AltKeys {
		r.Status, r.Detail = StatusSkip, "Alt key bindings are disabled (ui.alt_keys = false)"
		return []Result{r}
	}
	info := termx.Detect(d.Getenv)
	g := termx.OptionAsMeta(info.Program, d.GOOS)
	if !g.Needed {
		r.Status, r.Detail = StatusOK, g.Setting
		return []Result{r}
	}
	r.Status = StatusWarn
	r.Detail = "Alt key bindings need Option to send Meta in " + info.Program.Name() + "; doctor cannot read the terminal's setting"
	// Every action bound to an Alt key is bound after the prefix as well, so
	// the workspace stays usable while the terminal is set up, and a terminal
	// nobody changes stays usable for good.
	r.Fix = g.Setting + "\nlmux keys lists the same actions after the prefix, which need no terminal setting"
	return []Result{r}
}

// checkShiftEnter reports whether Shift+Enter adds a line to the agent's
// prompt instead of sending it. tmux forwards the key, since the generated
// configuration turns extended keys on, so what is left is the terminal: some
// report the key themselves, some need the binding Claude Code writes with
// /terminal-setup, and Terminal cannot send it at all. Nothing is read from
// the terminal, so the result is a step to take, never an observation, and it
// is a note rather than a warning: the prompt is usable without it.
func checkShiftEnter(_ context.Context, d Deps) []Result {
	r := Result{ID: "shift-enter", Title: "Shift+Enter"}
	info := termx.Detect(d.Getenv)
	g := termx.ShiftEnter(info.Program)
	if g.Works {
		r.Status, r.Detail = StatusOK, "nothing to set up in "+info.Program.Name()+": "+g.Setting
		return []Result{r}
	}
	r.Status = StatusWarn
	r.Detail = "Shift+Enter may send the prompt instead of adding a line in " + info.Program.Name() + "; doctor cannot press a key to find out"
	r.Fix = g.Setting
	return []Result{r}
}

func checkNeovim(ctx context.Context, d Deps) []Result {
	r := Result{ID: "nvim", Title: "Neovim"}
	minimum := Version{Major: 0, Minor: 9}
	path, err := d.LookPath("nvim")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusWarn, "nvim is not on PATH; the review popup needs Neovim 0.9 or newer", d.installCommand(pkgNeovim)
		return []Result{r}
	}
	out, err := d.output(ctx, path, "--version")
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusWarn, fmt.Sprintf("%s --version failed: %v", path, err), d.installCommand(pkgNeovim)
		return []Result{r}
	}
	v, ok := ParseVersion(firstLine(out))
	if !ok {
		r.Status, r.Detail = StatusWarn, fmt.Sprintf("cannot read the Neovim version from %q", firstLine(out))
		return []Result{r}
	}
	if v.Less(minimum) {
		r.Status, r.Detail, r.Fix = StatusWarn, fmt.Sprintf("Neovim %s at %s is older than 0.9; the review popup needs 0.9 or newer", v, path), d.installCommand(pkgNeovim)
		return []Result{r}
	}
	r.Status, r.Detail = StatusOK, fmt.Sprintf("Neovim %s at %s", v, path)
	return []Result{r}
}

func checkDocker(ctx context.Context, d Deps) []Result {
	r := Result{ID: "docker", Title: "Docker"}
	required := d.Isolation == "container"
	degraded := StatusWarn
	if required {
		degraded = StatusFail
	}
	path, err := d.LookPath("docker")
	if err != nil {
		if !required {
			r.Status, r.Detail = StatusSkip, `docker is not on PATH; only sandbox.isolation = "container" needs it`
			return []Result{r}
		}
		r.Status, r.Detail, r.Fix = StatusFail, `docker is not on PATH and sandbox.isolation = "container" needs it`, d.installCommand(pkgDocker)
		return []Result{r}
	}
	out, err := d.output(ctx, path, "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		r.Status = degraded
		r.Detail = fmt.Sprintf("the Docker daemon is not reachable: %v", err)
		switch {
		case strings.Contains(strings.ToLower(err.Error()), "permission denied"):
			r.Fix = `sudo usermod -aG docker "$USER" (then log out and back in)`
		case d.GOOS == "darwin":
			r.Fix = "colima start (or open -a Docker)"
			// The daemon on macOS runs in a virtual machine of its own, and
			// starting colima needs no privileges; a Docker Desktop install
			// has no colima, so the command is offered only when it is there.
			if colima, lookErr := d.LookPath("colima"); lookErr == nil {
				r.Action = CommandFix("Start the Docker virtual machine", colima, "start")
			}
		default:
			r.Fix = "sudo systemctl start docker"
		}
		return []Result{r}
	}
	r.Status = StatusOK
	if v := firstLine(out); v != "" {
		r.Detail = fmt.Sprintf("Docker server %s, client at %s", v, path)
	} else {
		r.Detail = "Docker daemon reachable, client at " + path
	}
	return []Result{r}
}
