package sandbox

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// CredentialModeDeny hides a credential from sandboxed commands. It is the
// only mode lyna-tmux writes: "mask" makes the sandbox proxy send the real
// secret to allowed hosts and needs TLS termination, which is a decision for
// the user, not a default.
const CredentialModeDeny = "deny"

// EnvSubprocessScrub strips credentials from the environment of every
// subprocess Claude starts (Bash, hooks, MCP stdio servers).
const EnvSubprocessScrub = "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"

// Limits on user extras. They keep generated settings far below the 2 MiB cap
// Claude Code applies to a --settings file.
const (
	maxExtras     = 256
	maxValueBytes = 4096
	maxDomainLen  = 253
)

// Settings is the "sandbox" object of Claude Code settings.
type Settings struct {
	Enabled                   bool         `json:"enabled"`
	FailIfUnavailable         bool         `json:"failIfUnavailable,omitempty"`
	AutoAllowBashIfSandboxed  *bool        `json:"autoAllowBashIfSandboxed,omitempty"`
	AllowUnsandboxedCommands  *bool        `json:"allowUnsandboxedCommands,omitempty"`
	ExcludedCommands          []string     `json:"excludedCommands,omitempty"`
	EnableWeakerNestedSandbox bool         `json:"enableWeakerNestedSandbox,omitempty"`
	Filesystem                *Filesystem  `json:"filesystem,omitempty"`
	Network                   *Network     `json:"network,omitempty"`
	Credentials               *Credentials `json:"credentials,omitempty"`
}

// Filesystem is "sandbox.filesystem". Paths are absolute or start with "~/".
type Filesystem struct {
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyRead   []string `json:"denyRead,omitempty"`
	AllowRead  []string `json:"allowRead,omitempty"`
}

// Network is "sandbox.network".
type Network struct {
	AllowedDomains  []string `json:"allowedDomains,omitempty"`
	StrictAllowlist bool     `json:"strictAllowlist,omitempty"`
}

// Credentials is "sandbox.credentials".
type Credentials struct {
	Files   []CredentialFile   `json:"files,omitempty"`
	EnvVars []CredentialEnvVar `json:"envVars,omitempty"`
}

// CredentialFile is one "sandbox.credentials.files" entry.
type CredentialFile struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// CredentialEnvVar is one "sandbox.credentials.envVars" entry.
type CredentialEnvVar struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// Permissions is the part of the "permissions" object a profile controls.
type Permissions struct {
	Ask                                 []string `json:"ask,omitempty"`
	Deny                                []string `json:"deny,omitempty"`
	BlockReadsOutsideWorkingDirectories bool     `json:"blockReadsOutsideWorkingDirectories,omitempty"`
	// DisableBypassPermissionsMode is "disable" when bypassPermissions is not
	// allowed, so Claude itself rejects --dangerously-skip-permissions and
	// never cycles into bypass mode during the session.
	DisableBypassPermissionsMode string `json:"disableBypassPermissionsMode,omitempty"`
}

// IsZero reports whether no permission key is set.
func (p Permissions) IsZero() bool {
	return len(p.Ask) == 0 && len(p.Deny) == 0 && !p.BlockReadsOutsideWorkingDirectories && p.DisableBypassPermissionsMode == ""
}

// Extras are the user additions from the [sandbox] configuration table.
type Extras struct {
	AllowWrite       []string
	DenyRead         []string
	AllowedDomains   []string
	ExcludedCommands []string
}

// Input selects what Resolve produces.
type Input struct {
	Profile    Profile
	Isolation  Isolation
	Ecosystems []Ecosystem
	Extras     Extras
	// WeakerNestedSandbox sets enableWeakerNestedSandbox, which lets the Linux
	// sandbox start inside an unprivileged container by reusing the
	// container's /proc. It weakens isolation, so it is only ever set on an
	// explicit request, never from container detection alone.
	WeakerNestedSandbox bool
}

// Resolution is everything a profile contributes to one Claude launch.
type Resolution struct {
	Profile     Profile
	Isolation   Isolation
	Sandbox     Settings
	Permissions Permissions
	// Env holds variables the Claude process must start with.
	Env map[string]string
	// Runtime is the policy of the sandbox runtime that wraps the whole Claude
	// process; set only at process isolation. Runtime.ForLaunch adds the paths
	// of one launch.
	Runtime *Runtime
}

// credentialFiles are read-denied in every enabled profile. Claude Code has no
// built-in credential list: only listed paths are protected.
var credentialFiles = []string{
	"~/.ssh",
	"~/.aws",
	"~/.gnupg",
	"~/.config/gh",
	"~/.netrc",
	"~/.docker/config.json",
	"~/.kube",
	"~/.npmrc",
	"~/.pypirc",
	"~/.config/gcloud",
	"~/.azure",
	"~/.git-credentials",
}

// credentialEnvVars are unset for sandboxed commands in every enabled profile.
var credentialEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AZURE_CLIENT_SECRET",
	"CARGO_REGISTRY_TOKEN",
	"CLOUDFLARE_API_TOKEN",
	"DIGITALOCEAN_ACCESS_TOKEN",
	"DOCKER_PASSWORD",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GITLAB_TOKEN",
	"GOOGLE_API_KEY",
	"HF_TOKEN",
	"NODE_AUTH_TOKEN",
	"NPM_TOKEN",
	"OPENAI_API_KEY",
	"PYPI_API_TOKEN",
	"SLACK_BOT_TOKEN",
	"STRIPE_SECRET_KEY",
	"TWINE_PASSWORD",
	"VAULT_TOKEN",
}

// envFileRules match .env files at any depth under the working directory
// (bare file names follow gitignore semantics in Read rules).
var envFileRules = []string{"Read(.env)", "Read(.env.*)"}

// strictAllowRead re-opens git's own configuration, which the strict read
// block would otherwise hide from sandboxed git (commit identity, aliases).
// Credential stores stay denied: a deny entry holds inside a wider allow.
var strictAllowRead = []string{"~/.gitconfig", "~/.config/git"}

// CredentialFiles returns the credential paths every enabled profile denies.
func CredentialFiles() []string { return append([]string(nil), credentialFiles...) }

// CredentialEnvVars returns the token variables every enabled profile unsets
// for sandboxed commands.
func CredentialEnvVars() []string { return append([]string(nil), credentialEnvVars...) }

// Resolve builds the settings fragments and environment for a launch.
//
// Standard keeps sandboxed Bash prompt-free (autoAllowBashIfSandboxed) and
// leaves subprocess environments alone, so MCP servers that read tokens from
// the environment keep working; token variables are still unset for sandboxed
// commands through credentials.envVars. Strict adds
// CLAUDE_CODE_SUBPROCESS_ENV_SCRUB, which Claude documents as turning
// auto-allow off, so strict writes autoAllowBashIfSandboxed false to say what
// actually happens.
//
// The isolation level moves the boundary:
//   - process: the sandbox runtime confines the whole Claude process. A
//     Seatbelt profile cannot be applied inside another deny-default one
//     (sandbox_apply fails with EPERM), so the Bash sandbox is turned off and
//     its protections move to the runtime: credential files and extra read
//     denies, an egress allowlist, and CLAUDE_CODE_SUBPROCESS_ENV_SCRUB for
//     the token variables, which must stay visible to Claude itself.
//   - container: the dev container is the boundary. Its image ships no
//     bubblewrap or socat, so a missing Bash sandbox must not stop Bash
//     (failIfUnavailable is left out); the credential denies stay for images
//     that add them.
func Resolve(in Input) (Resolution, error) {
	profile, err := ParseProfile(string(in.Profile))
	if err != nil {
		return Resolution{}, err
	}
	isolation, err := ParseIsolation(string(in.Isolation))
	if err != nil {
		return Resolution{}, err
	}
	extras, err := in.Extras.normalize()
	if err != nil {
		return Resolution{}, err
	}
	for _, e := range in.Ecosystems {
		if EcosystemDomains(e) == nil {
			return Resolution{}, fmt.Errorf("%w: unknown ecosystem %q", ErrInvalid, e)
		}
	}

	res := Resolution{Profile: profile, Isolation: isolation, Env: map[string]string{}}
	if !BypassAllowed(profile, isolation) {
		res.Permissions.DisableBypassPermissionsMode = "disable"
	}
	if profile == Off {
		if isolation == IsolationProcess {
			return Resolution{}, fmt.Errorf("%w: the off profile runs Claude without a sandbox, so it cannot be combined with process isolation", ErrInvalid)
		}
		res.Sandbox = Settings{Enabled: false}
		return res, nil
	}

	sb := Settings{
		Enabled:                   true,
		FailIfUnavailable:         true,
		ExcludedCommands:          extras.ExcludedCommands,
		EnableWeakerNestedSandbox: in.WeakerNestedSandbox,
		Credentials:               defaultCredentials(),
	}
	fs := Filesystem{AllowWrite: extras.AllowWrite, DenyRead: extras.DenyRead}
	domains := extras.AllowedDomains

	switch profile {
	case Standard:
		sb.AutoAllowBashIfSandboxed = boolPtr(true)
		res.Permissions.Ask = append([]string(nil), envFileRules...)
		if len(domains) > 0 {
			sb.Network = &Network{AllowedDomains: domains}
		}
	case Strict:
		sb.AutoAllowBashIfSandboxed = boolPtr(false)
		sb.AllowUnsandboxedCommands = boolPtr(false)
		fs.AllowRead = append([]string(nil), strictAllowRead...)
		allow := append([]string(nil), gitHubDomains...)
		for _, e := range in.Ecosystems {
			allow = append(allow, EcosystemDomains(e)...)
		}
		sb.Network = &Network{AllowedDomains: dedupe(append(allow, domains...)), StrictAllowlist: true}
		res.Permissions.Deny = append([]string(nil), envFileRules...)
		res.Permissions.BlockReadsOutsideWorkingDirectories = true
		res.Env[EnvSubprocessScrub] = "1"
	}
	if len(fs.AllowWrite) > 0 || len(fs.DenyRead) > 0 || len(fs.AllowRead) > 0 {
		sb.Filesystem = &fs
	}
	switch isolation {
	case IsolationProcess:
		res.Runtime = runtimePolicy(in.Ecosystems, extras)
		res.Sandbox = Settings{Enabled: false}
		res.Env[EnvSubprocessScrub] = "1"
		return res, nil
	case IsolationContainer:
		// The container is the boundary, so the platform sandbox no longer has
		// to be available for the launch to be safe. Resolve answers for the
		// level it is asked about; whether this process really runs in the dev
		// container that grants the relaxation is decided from
		// ContainerEvidence before a launch reaches this level.
		sb.FailIfUnavailable = false
	}
	res.Sandbox = sb
	return res, nil
}

func defaultCredentials() *Credentials {
	c := &Credentials{}
	for _, p := range credentialFiles {
		c.Files = append(c.Files, CredentialFile{Path: p, Mode: CredentialModeDeny})
	}
	for _, n := range credentialEnvVars {
		c.EnvVars = append(c.EnvVars, CredentialEnvVar{Name: n, Mode: CredentialModeDeny})
	}
	return c
}

func boolPtr(b bool) *bool { return &b }

var domainPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^(?:\*\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
})

// Validate reports every invalid extra at once.
func (e Extras) Validate() error {
	_, err := e.normalize()
	return err
}

// normalize validates the extras and returns them deduplicated in first-seen
// order: hosts lowercased, trailing slashes removed from paths.
func (e Extras) normalize() (Extras, error) {
	var problems []error
	out := Extras{
		AllowWrite:       normalizeList("allow_write", e.AllowWrite, NormalizePath, &problems),
		DenyRead:         normalizeList("deny_read", e.DenyRead, NormalizePath, &problems),
		AllowedDomains:   normalizeList("allowed_domains", e.AllowedDomains, NormalizeDomain, &problems),
		ExcludedCommands: normalizeList("excluded_commands", e.ExcludedCommands, normalizeCommand, &problems),
	}
	if len(problems) > 0 {
		return Extras{}, errors.Join(problems...)
	}
	return out, nil
}

func normalizeList(key string, values []string, norm func(string) (string, error), problems *[]error) []string {
	if len(values) > maxExtras {
		*problems = append(*problems, fmt.Errorf("%w: %s lists at most %d entries (got %d)", ErrInvalid, key, maxExtras, len(values)))
		return nil
	}
	out := make([]string, 0, len(values))
	for i, v := range values {
		n, err := norm(v)
		if err != nil {
			*problems = append(*problems, fmt.Errorf("%w: %s[%d]: %w", ErrInvalid, key, i+1, err))
			continue
		}
		out = append(out, n)
	}
	out = dedupe(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizePath checks a sandbox filesystem path: absolute or under the home
// directory ("~/"), bounded, printable. A trailing slash is removed, as Claude
// Code does, so "~/.aws/" and "~/.aws" dedupe.
func NormalizePath(p string) (string, error) {
	if err := printable(p); err != nil {
		return "", err
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
		return "", fmt.Errorf("path must be absolute or start with ~/ (got %q)", p)
	}
	for len(p) > 1 && strings.HasSuffix(p, "/") && p != "~/" {
		p = p[:len(p)-1]
	}
	return p, nil
}

// NormalizeDomain checks a network allowlist entry: a hostname or a "*."
// wildcard over one, lowercased.
func NormalizeDomain(d string) (string, error) {
	if err := printable(d); err != nil {
		return "", err
	}
	lower := strings.ToLower(d)
	if len(lower) > maxDomainLen || !domainPattern().MatchString(lower) {
		return "", fmt.Errorf("must be a hostname such as registry.npmjs.org or *.example.com (got %q)", d)
	}
	return lower, nil
}

func normalizeCommand(c string) (string, error) {
	if err := printable(c); err != nil {
		return "", err
	}
	if strings.TrimSpace(c) == "" {
		return "", errors.New("must not be blank")
	}
	return c, nil
}

// printable accepts non-empty, bounded, valid UTF-8 text without control
// characters, so values reach JSON and argv unchanged.
func printable(s string) error {
	switch {
	case s == "":
		return errors.New("must not be empty")
	case len(s) > maxValueBytes:
		return errors.New("longer than " + strconv.Itoa(maxValueBytes) + " bytes")
	case !utf8.ValidString(s):
		return errors.New("not valid UTF-8")
	case strings.ContainsFunc(s, unicode.IsControl):
		return fmt.Errorf("contains control characters (got %q)", s)
	}
	return nil
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0:0]
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
