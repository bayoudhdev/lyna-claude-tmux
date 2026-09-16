package sandbox

import "slices"

// Network modes reported by Describe.
const (
	// NetworkOpen leaves egress unrestricted.
	NetworkOpen = "open"
	// NetworkAsk allows the listed hosts and asks before any other.
	NetworkAsk = "ask"
	// NetworkAllowlist allows only the listed hosts.
	NetworkAllowlist = "allowlist"
)

// Fallbacks reported by Describe: what happens to a Bash command the sandbox
// blocks.
const (
	// FallbackAsk retries the command outside the sandbox after a permission prompt.
	FallbackAsk = "ask"
	// FallbackNever never runs a command outside the boundary.
	FallbackNever = "never"
	// FallbackUnsandboxed runs every command without a sandbox.
	FallbackUnsandboxed = "unsandboxed"
)

// Access to .env files reported by Describe.
const (
	EnvFilesAsk   = "ask"
	EnvFilesDeny  = "deny"
	EnvFilesAllow = "allow"
)

// Summary is what a resolution protects and allows, in terms a person reads.
// It is derived from the settings a launch writes, so it cannot drift from
// them.
type Summary struct {
	Profile   Profile   `json:"profile"`
	Isolation Isolation `json:"isolation"`
	// Boundary names what runs inside the isolation boundary.
	Boundary string `json:"boundary"`
	// BashSandbox reports whether Claude's own Bash sandbox is on.
	BashSandbox bool `json:"bash_sandbox"`
	// RequiresSandbox reports that Bash refuses to run when the sandbox
	// cannot start (failIfUnavailable).
	RequiresSandbox bool `json:"requires_sandbox"`
	// DeniedFiles are credential and user paths nothing inside the boundary reads.
	DeniedFiles []string `json:"denied_files"`
	// DeniedEnvVars are unset for sandboxed commands.
	DeniedEnvVars []string `json:"denied_env_vars"`
	// ScrubsSubprocessEnv reports that Claude strips credentials from the
	// environment of every subprocess (Bash, hooks, MCP servers).
	ScrubsSubprocessEnv bool `json:"scrubs_subprocess_env"`
	// EnvFiles is how file tools treat .env files: ask, deny or allow.
	EnvFiles string `json:"env_files"`
	// BlocksReadsOutsideProject reports that file tools cannot read outside
	// the working directories.
	BlocksReadsOutsideProject bool `json:"blocks_reads_outside_project"`
	// Network is open, ask or allowlist; Domains are the hosts allowed without asking.
	Network string   `json:"network"`
	Domains []string `json:"domains"`
	// WritablePaths are paths outside the project that sandboxed commands may write.
	WritablePaths []string `json:"writable_paths"`
	// UnsandboxedFallback is ask, never or unsandboxed.
	UnsandboxedFallback string `json:"unsandboxed_fallback"`
	// BypassAllowed reports whether bypassPermissions may be used.
	BypassAllowed bool `json:"bypass_allowed"`
}

// Boundary describes what an isolation level confines.
func (i Isolation) Boundary() string {
	switch i {
	case IsolationProcess:
		return "the whole Claude process, in the sandbox runtime"
	case IsolationContainer:
		return "the whole workspace, in the dev container"
	default:
		return "Bash commands, in the Claude Code sandbox"
	}
}

// Describe summarizes a resolution.
func Describe(r Resolution) Summary {
	s := Summary{
		Profile:                   r.Profile,
		Isolation:                 r.Isolation,
		Boundary:                  r.Isolation.Boundary(),
		BashSandbox:               r.Sandbox.Enabled,
		RequiresSandbox:           r.Sandbox.Enabled && r.Sandbox.FailIfUnavailable,
		ScrubsSubprocessEnv:       r.Env[EnvSubprocessScrub] == "1",
		BlocksReadsOutsideProject: r.Permissions.BlockReadsOutsideWorkingDirectories,
		BypassAllowed:             BypassAllowed(r.Profile, r.Isolation),
		EnvFiles:                  EnvFilesAllow,
		Network:                   NetworkOpen,
		UnsandboxedFallback:       FallbackUnsandboxed,
		DeniedFiles:               []string{},
		DeniedEnvVars:             []string{},
		Domains:                   []string{},
		WritablePaths:             []string{},
	}
	// What the summary says about .env files has to come from the rules that
	// name them, not from the lists being non-empty: another rule in either
	// list would otherwise be read as a decision about .env.
	switch {
	case hasEnvFileRule(r.Permissions.Deny):
		s.EnvFiles = EnvFilesDeny
	case hasEnvFileRule(r.Permissions.Ask):
		s.EnvFiles = EnvFilesAsk
	}
	switch {
	case r.Runtime != nil:
		s.Network = NetworkAllowlist
		s.Domains = append(s.Domains, r.Runtime.Network.AllowedDomains...)
		s.DeniedFiles = append(s.DeniedFiles, r.Runtime.Filesystem.DenyRead...)
		s.WritablePaths = append(s.WritablePaths, r.Runtime.Filesystem.AllowWrite...)
		s.UnsandboxedFallback = FallbackNever
	case r.Sandbox.Enabled:
		sb := r.Sandbox
		s.Network = NetworkAsk
		if sb.Network != nil {
			s.Domains = append(s.Domains, sb.Network.AllowedDomains...)
			if sb.Network.StrictAllowlist {
				s.Network = NetworkAllowlist
			}
		}
		if sb.Credentials != nil {
			for _, f := range sb.Credentials.Files {
				s.DeniedFiles = append(s.DeniedFiles, f.Path)
			}
			for _, v := range sb.Credentials.EnvVars {
				s.DeniedEnvVars = append(s.DeniedEnvVars, v.Name)
			}
		}
		if sb.Filesystem != nil {
			s.DeniedFiles = append(s.DeniedFiles, sb.Filesystem.DenyRead...)
			s.WritablePaths = append(s.WritablePaths, sb.Filesystem.AllowWrite...)
		}
		s.UnsandboxedFallback = FallbackAsk
		if sb.AllowUnsandboxedCommands != nil && !*sb.AllowUnsandboxedCommands {
			s.UnsandboxedFallback = FallbackNever
		}
	}
	// A deny_read extra may repeat a credential path.
	s.DeniedFiles = dedupe(s.DeniedFiles)
	return s
}

// hasEnvFileRule reports whether rules carry one of the .env read rules.
func hasEnvFileRule(rules []string) bool {
	for _, rule := range rules {
		if slices.Contains(envFileRules, rule) {
			return true
		}
	}
	return false
}
