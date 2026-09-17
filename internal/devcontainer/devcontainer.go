// Package devcontainer renders the container isolation level of lyna-tmux: a
// .devcontainer directory with a Debian slim image (non-root user, tmux,
// lyna-tmux and Claude Code), a default-deny egress firewall with a
// domain allowlist resolved at container start, the project bind mounted at
// /workspace and the Claude configuration in a per-project named volume. The
// Docker socket is never mounted. The lyna-tmux inside the image is either a
// binary staged next to the Dockerfile or a release downloaded when the image
// builds (Source).
//
// The same files work with any editor that supports the Dev Containers
// specification and with the docker lifecycle in docker.go.
package devcontainer

import (
	"bytes"
	"cmp"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

//go:embed templates/Dockerfile.tmpl templates/init-firewall.sh
var templates embed.FS

// Paths of the rendered files, relative to the project directory.
const (
	Dir               = ".devcontainer"
	FileJSON          = Dir + "/devcontainer.json"
	FileDockerfile    = Dir + "/Dockerfile"
	FileFirewall      = Dir + "/init-firewall.sh"
	FileAllowedDomain = Dir + "/allowed-domains"
	// FileBinary is the lyna-tmux binary staged next to the Dockerfile when
	// the image copies one in rather than downloading a release.
	FileBinary = Dir + "/lyna-tmux"
	// FileGitignore keeps the staged binary out of the repository.
	FileGitignore = Dir + "/.gitignore"
)

// Paths inside the container.
const (
	WorkspacePath = sandbox.DevContainerWorkspace
	FirewallPath  = "/usr/local/bin/init-firewall.sh"
	// MarkerPath is the file the image writes so that the isolation checks
	// can tell this container from any other one.
	MarkerPath = sandbox.DevContainerMarker
)

// MaxFileBytes bounds every rendered file read back from a project. The
// largest file Render produces is a few kilobytes.
const MaxFileBytes = 1 << 20

// Defaults for Options.
const (
	DefaultUser          = "lyna"
	DefaultTimezone      = "UTC"
	DefaultClaudeVersion = "latest"
)

// DefaultDomains are always allowed: the hosts Claude Code needs
// (sandbox.ClaudeDomains, shared with process isolation), then the changelog
// feed and GitHub for git over HTTPS.
var DefaultDomains = slices.Concat(sandbox.ClaudeDomains(), []string{
	"raw.githubusercontent.com",
	"github.com",
	"api.github.com",
	"codeload.github.com",
})

// Options select what Render produces.
type Options struct {
	// Project names the container, image and Claude volume; see ProjectName.
	Project string
	// User is the non-root account inside the container.
	User string
	// Timezone is an IANA zone name such as Europe/Paris.
	Timezone string
	// AllowedDomains are added to DefaultDomains.
	AllowedDomains []string
	// Source is where the lyna-tmux binary of the image comes from; it is
	// required, as nothing sensible can be rendered without one.
	Source Source
	// ClaudeVersion is passed to the Claude Code native installer: latest,
	// stable or a version number.
	ClaudeVersion string
}

var (
	projectPattern  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`) })
	userPattern     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`) })
	timezonePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,3}$`) })
	versionPattern  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`) })
	claudePattern   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(latest|stable|[0-9]+\.[0-9]+\.[0-9]+)$`) })
	labelPattern    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`) })
)

// maxProjectLen keeps derived names ("lyna-tmux-claude-" + project) well
// inside Docker's limits.
const maxProjectLen = 40

// ProjectName derives a Docker-safe project name from a directory: the base
// name lower-cased, runs of other characters folded into one hyphen, trimmed
// to 40 characters. Image names forbid leading, trailing and repeated
// separators, so only single inner hyphens survive.
func ProjectName(dir string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(dir)))
	var b strings.Builder
	hyphen := false
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
			continue
		}
		hyphen = true
	}
	name := b.String()
	if len(name) > maxProjectLen {
		name = strings.TrimRight(name[:maxProjectLen], "-")
	}
	if name == "" {
		return "workspace"
	}
	return name
}

// ValidateProject checks a project name for image, container and volume use.
func ValidateProject(name string) error {
	if len(name) > maxProjectLen || !projectPattern().MatchString(name) {
		return fmt.Errorf("devcontainer: invalid project name %q (lower-case letters and digits joined by single hyphens, at most %d characters)", name, maxProjectLen)
	}
	return nil
}

// ValidateUser checks a Linux account name; root is refused because Claude
// Code must not run as root in the container.
func ValidateUser(name string) error {
	if name == "root" || !userPattern().MatchString(name) {
		return fmt.Errorf("devcontainer: invalid user %q (a non-root name matching [a-z_][a-z0-9_-]{0,31})", name)
	}
	return nil
}

// NormalizeDomain lower-cases a host name and removes surrounding spaces and a
// trailing root dot.
func NormalizeDomain(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// ValidateDomain checks a normalized host name: at least two labels of
// letters, digits and inner hyphens, 253 characters at most, and a top-level
// label with a letter so an IP address is never taken for a name. Wildcards
// are refused because the firewall resolves names to addresses at start.
func ValidateDomain(d string) error {
	switch {
	case d == "":
		return errors.New("devcontainer: empty domain")
	case strings.Contains(d, "*"):
		return fmt.Errorf("devcontainer: wildcard domain %q cannot be resolved; list each host", d)
	case len(d) > 253:
		return fmt.Errorf("devcontainer: domain %q is longer than 253 characters", d)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("devcontainer: domain %q needs at least two labels", d)
	}
	for _, l := range labels {
		if !labelPattern().MatchString(l) {
			return fmt.Errorf("devcontainer: domain %q has an invalid label %q", d, l)
		}
	}
	if !strings.ContainsFunc(labels[len(labels)-1], func(r rune) bool { return r >= 'a' && r <= 'z' }) {
		return fmt.Errorf("devcontainer: domain %q ends in a numeric label", d)
	}
	return nil
}

// resolved is Options after defaults and validation.
type resolved struct {
	Project, User, Timezone, ClaudeVersion string
	// Version and BinarySHA256 are the validated Source; the template takes
	// the branch of the one that is set.
	Version, BinarySHA256 string
	// Marker, Workspace and BinaryPath are constants the template writes into
	// the image; they travel here so the paths the isolation checks and
	// DetectSource look for and the paths the image creates cannot drift
	// apart.
	Marker, Workspace, BinaryPath string
	Domains                       []string
}

func (o Options) resolve() (resolved, error) {
	r := resolved{
		Project:       o.Project,
		User:          cmp.Or(o.User, DefaultUser),
		Timezone:      cmp.Or(o.Timezone, DefaultTimezone),
		ClaudeVersion: cmp.Or(o.ClaudeVersion, DefaultClaudeVersion),
		Version:       o.Source.Version,
		BinarySHA256:  o.Source.BinarySHA256,
		Marker:        MarkerPath,
		Workspace:     WorkspacePath,
		BinaryPath:    BinaryPath,
	}
	if err := ValidateProject(r.Project); err != nil {
		return resolved{}, err
	}
	if err := ValidateUser(r.User); err != nil {
		return resolved{}, err
	}
	if !timezonePattern().MatchString(r.Timezone) {
		return resolved{}, fmt.Errorf("devcontainer: invalid timezone %q (an IANA name such as Europe/Paris)", r.Timezone)
	}
	if err := o.Source.validate(); err != nil {
		return resolved{}, err
	}
	if !claudePattern().MatchString(r.ClaudeVersion) {
		return resolved{}, fmt.Errorf("devcontainer: invalid Claude Code version %q (latest, stable or X.Y.Z)", r.ClaudeVersion)
	}
	for _, d := range slices.Concat(DefaultDomains, o.AllowedDomains) {
		d = NormalizeDomain(d)
		if err := ValidateDomain(d); err != nil {
			return resolved{}, err
		}
		if !slices.Contains(r.Domains, d) {
			r.Domains = append(r.Domains, d)
		}
	}
	return r, nil
}

// Volume is the named volume holding the Claude configuration of a project.
func Volume(project string) string { return "lyna-tmux-claude-" + project }

// FirewallAllowlistEnv carries the rendered allowlist into the container as an
// environment value. The value travels as one element of the docker argument
// vector and is never interpolated into shell text.
const FirewallAllowlistEnv = "LYNA_TMUX_ALLOWLIST"

// firewallPreamble copies the allowlist FirewallAllowlistEnv carries into a
// private temporary file and points the firewall script at it. It is a fixed
// string, so nothing outside this binary ends up as shell source.
const firewallPreamble = "umask 077\n" +
	"LYNA_TMUX_FIREWALL_ALLOWLIST=$(mktemp) || exit 1\n" +
	"export LYNA_TMUX_FIREWALL_ALLOWLIST\n" +
	"printf '%s' \"$LYNA_TMUX_ALLOWLIST\" > \"$LYNA_TMUX_FIREWALL_ALLOWLIST\"\n" +
	"unset LYNA_TMUX_ALLOWLIST\n"

// FirewallScript is what the firewall step feeds to bash inside the
// container: the preamble, then the firewall script this binary embeds. The
// image supplies neither the script nor the allowlist, so a container started
// from an image built before the current files, or from one somebody else
// built, still gets exactly this firewall.
func FirewallScript() ([]byte, error) {
	script, err := templates.ReadFile("templates/init-firewall.sh")
	if err != nil {
		return nil, err
	}
	return slices.Concat([]byte(firewallPreamble), script), nil
}

// devcontainerJSON mirrors the Dev Containers specification fields used here,
// in the order they are written.
type devcontainerJSON struct {
	Name             string            `json:"name"`
	Build            buildJSON         `json:"build"`
	RunArgs          []string          `json:"runArgs"`
	ContainerUser    string            `json:"containerUser"`
	RemoteUser       string            `json:"remoteUser"`
	WorkspaceMount   string            `json:"workspaceMount"`
	WorkspaceFolder  string            `json:"workspaceFolder"`
	Mounts           []string          `json:"mounts"`
	ContainerEnv     map[string]string `json:"containerEnv"`
	PostStartCommand string            `json:"postStartCommand"`
	WaitFor          string            `json:"waitFor"`
}

type buildJSON struct {
	Dockerfile string `json:"dockerfile"`
	Context    string `json:"context"`
}

// Render produces the .devcontainer files, keyed by slash-separated paths
// relative to the project directory.
func Render(o Options) (map[string][]byte, error) {
	r, err := o.resolve()
	if err != nil {
		return nil, err
	}
	home := "/home/" + r.User
	spec := devcontainerJSON{
		Name:  "lyna-tmux " + r.Project,
		Build: buildJSON{Dockerfile: "Dockerfile", Context: "."},
		// NET_ADMIN lets root program iptables and ipset; the container user
		// holds no capabilities and reaches the firewall only through sudo.
		RunArgs:          []string{"--cap-add=NET_ADMIN", "--init"},
		ContainerUser:    r.User,
		RemoteUser:       r.User,
		WorkspaceMount:   "source=${localWorkspaceFolder},target=" + WorkspacePath + ",type=bind",
		WorkspaceFolder:  WorkspacePath,
		Mounts:           []string{"source=" + Volume(r.Project) + ",target=" + home + "/.claude,type=volume"},
		ContainerEnv:     map[string]string{"CLAUDE_CONFIG_DIR": home + "/.claude"},
		PostStartCommand: "sudo " + FirewallPath,
		WaitFor:          "postStartCommand",
	}
	js, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}

	tmplText, err := templates.ReadFile("templates/Dockerfile.tmpl")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("Dockerfile").Option("missingkey=error").Parse(string(tmplText))
	if err != nil {
		return nil, err
	}
	var dockerfile bytes.Buffer
	if err := tmpl.Execute(&dockerfile, r); err != nil {
		return nil, err
	}

	firewall, err := templates.ReadFile("templates/init-firewall.sh")
	if err != nil {
		return nil, err
	}

	var domains strings.Builder
	domains.WriteString("# Egress allowlist for the lyna-tmux dev container, one host per line.\n")
	domains.WriteString("# Resolved to IPv4 addresses at container start by init-firewall.sh; rebuild after editing.\n")
	for _, d := range r.Domains {
		domains.WriteString(d + "\n")
	}

	return map[string][]byte{
		FileJSON:          append(js, '\n'),
		FileDockerfile:    dockerfile.Bytes(),
		FileFirewall:      firewall,
		FileAllowedDomain: []byte(domains.String()),
	}, nil
}

// ErrExists is returned by Write when a file exists and force is false.
var ErrExists = errors.New("devcontainer: file already exists")

var (
	// ErrMissing is returned by Verify when a rendered file is absent.
	ErrMissing = errors.New("devcontainer: rendered file is missing")
	// ErrModified is returned by Verify when a file on disk is not the one
	// lyna-tmux renders.
	ErrModified = errors.New("devcontainer: file is not the one lyna-tmux renders")
)

// Verify checks that the .devcontainer files under dir are byte for byte the
// ones in files. The image built from that directory is the isolation
// boundary of container isolation, so a repository that ships its own
// Dockerfile or firewall script would otherwise choose the boundary it is
// meant to be confined by. Files are read without following a final symbolic
// link and bounded by MaxFileBytes; the first difference in path order is
// reported, naming the file and how to restore it.
func Verify(dir string, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		target := filepath.Join(dir, filepath.FromSlash(name))
		got, err := fsx.ReadFileNoFollow(target, MaxFileBytes)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s: %w; run `lyna-tmux sandbox devcontainer init`", target, ErrMissing)
		case err != nil:
			return err
		case !bytes.Equal(got, files[name]):
			return fmt.Errorf("%s: %w; run `lyna-tmux sandbox devcontainer init --force` to restore it, or move your changes into the lyna-tmux configuration", target, ErrModified)
		}
	}
	return nil
}

// Write stores rendered files under dir. Nothing is written when any target
// already exists (unless force) or when any path component under dir is a
// symbolic link, so a refused write never leaves a half-updated directory.
// Scripts and the staged binary get mode 0755, other files 0644. It returns
// the written paths.
func Write(dir string, files map[string][]byte, force bool) ([]string, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := checkTarget(dir, name, force); err != nil {
			return nil, err
		}
	}
	written := make([]string, 0, len(names))
	for _, name := range names {
		target := filepath.Join(dir, filepath.FromSlash(name))
		// Project files are committed and read by docker build, so they
		// keep conventional permissions rather than the private state modes.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: repository directory, not private state
			return written, err
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") || name == FileBinary {
			mode = 0o755
		}
		if err := fsx.WriteFileAtomic(target, files[name], mode); err != nil {
			return written, err
		}
		written = append(written, target)
	}
	return written, nil
}

// checkTarget refuses names that escape dir, links anywhere below dir and
// existing files unless force.
func checkTarget(dir, name string, force bool) error {
	if name == "" || path.IsAbs(name) || strings.Contains(name, `\`) || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("devcontainer: invalid file name %q", name)
	}
	current := dir
	parts := strings.Split(path.Clean(name), "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", current, fsx.ErrSymlink)
		}
		last := i == len(parts)-1
		switch {
		case !last && !info.IsDir():
			return fmt.Errorf("%s: %w", current, fsx.ErrNotDir)
		case last && !info.Mode().IsRegular():
			return fmt.Errorf("devcontainer: %s is not a regular file", current)
		case last && !force:
			return fmt.Errorf("%s: %w (use --force to overwrite)", current, ErrExists)
		}
	}
	return nil
}
