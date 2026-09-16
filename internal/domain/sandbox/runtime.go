package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// The sandbox runtime (the @anthropic-ai/sandbox-runtime package) runs a
// command under the platform sandbox with a filesystem policy and an egress
// proxy. Process isolation starts Claude as
//
//	srt --settings <file> -- <claude argv>
//
// The "--" matters: srt parses options anywhere on its command line, so
// Claude's own --settings=<file> would otherwise replace the runtime's
// settings file.
const (
	// RuntimeBinary is the executable the package installs.
	RuntimeBinary = "srt"
	// RuntimeInstall is the command that installs it (Node.js 20.11 or newer).
	RuntimeInstall = "npm install -g @anthropic-ai/sandbox-runtime"
)

// claudeDomains are the hosts Claude Code needs for the API, sign-in, OAuth
// token exchange and native updates
// (https://code.claude.com/docs/en/network-config).
var claudeDomains = []string{
	"api.anthropic.com",
	"claude.ai",
	"claude.com",
	"platform.claude.com",
	"downloads.claude.ai",
}

// ClaudeDomains returns the hosts Claude Code itself must reach.
func ClaudeDomains() []string { return append([]string(nil), claudeDomains...) }

// Runtime is the sandbox runtime settings file. Only fields documented in the
// runtime's README and settings schema are written; allowedDomains,
// deniedDomains, denyRead, allowWrite and denyWrite are required by that
// schema, so they are always present, empty or not.
type Runtime struct {
	Network    RuntimeNetwork    `json:"network"`
	Filesystem RuntimeFilesystem `json:"filesystem"`
}

// RuntimeNetwork is "network". Egress is denied except to AllowedDomains.
type RuntimeNetwork struct {
	AllowedDomains []string `json:"allowedDomains"`
	DeniedDomains  []string `json:"deniedDomains"`
	// AllowUnixSockets lists socket paths the process may connect to. The
	// runtime honors it on macOS; on Linux every Unix socket stays blocked.
	AllowUnixSockets []string `json:"allowUnixSockets,omitempty"`
}

// RuntimeFilesystem is "filesystem": reads are allowed except DenyRead,
// writes are denied except AllowWrite.
type RuntimeFilesystem struct {
	DenyRead   []string `json:"denyRead"`
	AllowWrite []string `json:"allowWrite"`
	DenyWrite  []string `json:"denyWrite"`
}

// RuntimeLaunch holds the machine paths of one launch.
type RuntimeLaunch struct {
	// Home is the home directory of the user Claude runs as. The policy names
	// the credential files the way Claude Code settings do, under "~/", and
	// this is what those entries are written against: what reaches the runtime
	// is a machine path, so a credential file is denied whether or not the
	// runtime expands "~" itself.
	Home string
	// Project is the project root Claude works in.
	Project string
	// Writable are the other paths the Claude process writes: its
	// configuration directory and file and the lyna-tmux state directory.
	Writable []string
	// Sockets are Unix sockets the process connects to: the lyna-tmux tmux
	// server, which the hooks and status line reach.
	Sockets []string
}

// runtimePolicy is the launch-independent part of the runtime settings. Every
// enabled profile gets the same allowlist: Claude's own hosts, GitHub, the
// registries of the detected ecosystems and the configured extras. The
// runtime cannot ask before reaching a new host the way the Bash sandbox of
// the standard profile does, so standard gets the strict list.
func runtimePolicy(ecosystems []Ecosystem, extras Extras) *Runtime {
	domains := append(ClaudeDomains(), gitHubDomains...)
	for _, e := range ecosystems {
		domains = append(domains, EcosystemDomains(e)...)
	}
	domains = dedupe(append(domains, extras.AllowedDomains...))
	return &Runtime{
		Network: RuntimeNetwork{AllowedDomains: domains, DeniedDomains: []string{}},
		Filesystem: RuntimeFilesystem{
			DenyRead:   dedupe(append(CredentialFiles(), extras.DenyRead...)),
			AllowWrite: append([]string{}, extras.AllowWrite...),
			DenyWrite:  []string{},
		},
	}
}

// ForLaunch returns the settings for one launch: the project and the writable
// paths go first in allowWrite, the sockets into allowUnixSockets. Every path
// must be absolute.
func (r Runtime) ForLaunch(l RuntimeLaunch) (Runtime, error) {
	var problems []string
	// clean checks a path and returns the spelling the policy keeps, so that
	// "/project" and "/project/" cannot both reach the file as two entries for
	// one directory.
	clean := func(what, p string) string {
		n, err := NormalizePath(p)
		if err != nil || !strings.HasPrefix(n, "/") {
			problems = append(problems, fmt.Sprintf("%s must be an absolute path without control characters (got %q)", what, p))
			return p
		}
		return n
	}
	project := clean("project", l.Project)
	writable := make([]string, 0, len(l.Writable))
	for _, p := range l.Writable {
		writable = append(writable, clean("writable path", p))
	}
	sockets := make([]string, 0, len(l.Sockets))
	for _, p := range l.Sockets {
		sockets = append(sockets, clean("socket", p))
	}
	// expand rewrites the home-relative entries of a policy list. A launch
	// without a usable home is refused rather than written out with "~" left
	// in it: an entry the runtime does not resolve to the credential file is
	// an entry that protects nothing.
	expand := func(what string, list []string) []string {
		out := make([]string, 0, len(list))
		for _, p := range list {
			rest, ok := strings.CutPrefix(p, "~/")
			if !ok {
				out = append(out, p)
				continue
			}
			home, err := NormalizePath(l.Home)
			if err != nil || !strings.HasPrefix(home, "/") {
				problems = append(problems, fmt.Sprintf("%s %q is written against the home directory, which is not an absolute path (got %q)", what, p, l.Home))
				continue
			}
			out = append(out, path.Join(home, rest))
		}
		return dedupe(out)
	}
	denyRead := expand("denied read path", r.Filesystem.DenyRead)
	allowWrite := expand("writable path", r.Filesystem.AllowWrite)
	denyWrite := expand("denied write path", r.Filesystem.DenyWrite)
	if len(problems) > 0 {
		return Runtime{}, fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	out := Runtime{
		Network: RuntimeNetwork{
			AllowedDomains: append([]string{}, r.Network.AllowedDomains...),
			DeniedDomains:  append([]string{}, r.Network.DeniedDomains...),
		},
		Filesystem: RuntimeFilesystem{
			DenyRead:   denyRead,
			AllowWrite: dedupe(append(append([]string{project}, writable...), allowWrite...)),
			DenyWrite:  denyWrite,
		},
	}
	if len(sockets) > 0 {
		out.Network.AllowUnixSockets = dedupe(sockets)
	}
	return out, nil
}

// MarshalRuntime renders runtime settings: deterministic bytes, two-space
// indentation, a trailing newline, no HTML escaping, and the required lists
// written as [] when empty.
func MarshalRuntime(r Runtime) ([]byte, error) {
	for _, list := range []*[]string{&r.Network.AllowedDomains, &r.Network.DeniedDomains, &r.Filesystem.DenyRead, &r.Filesystem.AllowWrite, &r.Filesystem.DenyWrite} {
		if *list == nil {
			*list = []string{}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("sandbox: encode runtime settings: %w", err)
	}
	return buf.Bytes(), nil
}
