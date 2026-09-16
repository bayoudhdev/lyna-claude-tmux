// Package sandbox resolves a sandbox profile and isolation level into the
// sandbox and permissions fragments of the per-launch Claude Code settings
// file, plus the environment the Claude process starts with.
//
// Every key written here was checked against the Claude Code settings
// reference and the schema compiled into the claude binary. Keys that Claude
// honors only from user, managed or --settings sources (network.strictAllowlist
// in particular) are exactly why the fragments travel in a per-launch
// --settings file instead of a project file.
package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// Profile is how tightly Bash commands are confined.
type Profile string

// Profiles.
const (
	// Standard sandboxes Bash, hides credential files and token variables and
	// keeps the unsandboxed retry behind a permission prompt.
	Standard Profile = "standard"
	// Strict adds a deny-by-default network allowlist, no unsandboxed retry,
	// blocked reads outside the working directories and credential scrubbing
	// for every subprocess.
	Strict Profile = "strict"
	// Off disables the sandbox. It is never a default: it must be chosen explicitly.
	Off Profile = "off"
)

// Isolation is what runs inside the isolation boundary.
type Isolation string

// Isolation levels.
const (
	// IsolationBash sandboxes the Bash tool only.
	IsolationBash Isolation = "bash"
	// IsolationProcess wraps the whole Claude process in the sandbox runtime.
	IsolationProcess Isolation = "process"
	// IsolationContainer runs the whole workspace inside a dev container.
	IsolationContainer Isolation = "container"
)

// PermissionModeBypass is the Claude Code permission mode that skips every prompt.
const PermissionModeBypass = "bypassPermissions"

var (
	// ErrInvalid is wrapped by every validation failure of this package.
	ErrInvalid = errors.New("sandbox: invalid value")
	// ErrBypassRefused is returned when bypassPermissions is requested without
	// a boundary that contains file tools, hooks and MCP servers too.
	ErrBypassRefused = errors.New("sandbox: bypassPermissions requires the strict profile or container isolation")
)

// Profiles lists the profiles in display order, default first.
func Profiles() []Profile { return []Profile{Standard, Strict, Off} }

// Isolations lists the isolation levels in display order, default first.
func Isolations() []Isolation {
	return []Isolation{IsolationBash, IsolationProcess, IsolationContainer}
}

// ParseProfile maps a flag or configuration value to a Profile. The empty
// string selects Standard.
func ParseProfile(s string) (Profile, error) {
	if s == "" {
		return Standard, nil
	}
	for _, p := range Profiles() {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: profile must be one of %s (got %q)", ErrInvalid, joinProfiles(), s)
}

// ParseIsolation maps a flag or configuration value to an Isolation. The empty
// string selects IsolationBash.
func ParseIsolation(s string) (Isolation, error) {
	if s == "" {
		return IsolationBash, nil
	}
	for _, i := range Isolations() {
		if string(i) == s {
			return i, nil
		}
	}
	return "", fmt.Errorf("%w: isolation must be one of %s (got %q)", ErrInvalid, joinIsolations(), s)
}

// BypassAllowed reports whether bypassPermissions may be used. The Bash
// sandbox alone leaves file tools, hooks and MCP servers unconfined, so
// skipping every prompt needs either the strict profile or a container.
func BypassAllowed(profile Profile, isolation Isolation) bool {
	return profile == Strict || isolation == IsolationContainer
}

// CheckBypass refuses the bypassPermissions mode unless BypassAllowed.
// Every other mode passes.
func CheckBypass(profile Profile, isolation Isolation, permissionMode string) error {
	if permissionMode != PermissionModeBypass || BypassAllowed(profile, isolation) {
		return nil
	}
	return fmt.Errorf("%w (profile %s, isolation %s)", ErrBypassRefused, profile, isolation)
}

// Paths that tell a lyna-tmux dev container apart from any other container.
const (
	// DevContainerMarker is written into the image by the Dockerfile the
	// devcontainer package renders, and by nothing else. Its presence is what
	// separates "inside the boundary lyna-tmux built" from "inside some
	// container", which a CI job or a container with the Docker socket
	// mounted also satisfies.
	DevContainerMarker = "/etc/lyna-tmux/devcontainer"
	// DevContainerWorkspace is where that image bind mounts the project.
	DevContainerWorkspace = "/workspace"
)

// ContainerMarkers are files container runtimes create at the root of a
// container filesystem: /.dockerenv (Docker) and /run/.containerenv (Podman
// and other OCI runtimes).
func ContainerMarkers() []string { return []string{"/.dockerenv", "/run/.containerenv"} }

// InContainer reports whether any container marker exists. exists receives
// absolute paths. A container alone is not an isolation boundary lyna-tmux
// can vouch for; see DetectContainer.
func InContainer(exists func(path string) bool) bool {
	for _, m := range ContainerMarkers() {
		if exists(m) {
			return true
		}
	}
	return false
}

// ContainerEvidence is what the filesystem says about the container this
// process runs in. Container isolation grants the two most dangerous
// relaxations lyna-tmux has, bypassPermissions (BypassAllowed) and a sandbox
// that no longer fails closed (Resolve), so it takes all three findings, not
// the container marker alone.
type ContainerEvidence struct {
	// Runtime reports a container runtime marker.
	Runtime bool
	// Image reports the marker only the rendered dev container image writes.
	Image bool
	// Workspace reports the directory that image mounts the project at.
	Workspace bool
}

// DetectContainer collects the evidence. exists receives absolute paths.
func DetectContainer(exists func(path string) bool) ContainerEvidence {
	return ContainerEvidence{
		Runtime:   InContainer(exists),
		Image:     exists(DevContainerMarker),
		Workspace: exists(DevContainerWorkspace),
	}
}

// DevContainer reports whether this is the dev container lyna-tmux renders.
func (e ContainerEvidence) DevContainer() bool { return e.Runtime && e.Image && e.Workspace }

// Reason names the missing evidence, empty when DevContainer holds. It is the
// explanation a refused relaxation carries.
func (e ContainerEvidence) Reason() string {
	switch {
	case !e.Runtime:
		return "this process does not run inside a container"
	case !e.Image:
		return DevContainerMarker + " is missing: this container was not built from the dev container lyna-tmux renders"
	case !e.Workspace:
		return DevContainerWorkspace + " is missing: the project is not mounted where the dev container mounts it"
	}
	return ""
}

func joinProfiles() string {
	names := make([]string, 0, 3)
	for _, p := range Profiles() {
		names = append(names, string(p))
	}
	return strings.Join(names, ", ")
}

func joinIsolations() string {
	names := make([]string, 0, 3)
	for _, i := range Isolations() {
		names = append(names, string(i))
	}
	return strings.Join(names, ", ")
}
