package claudecfg

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
)

// Efforts are the accepted --effort values. ultracode runs at xhigh with
// dynamic workflows planned for every substantive task.
func Efforts() []string { return []string{"low", "medium", "high", "xhigh", "max", "ultracode"} }

// PermissionModes are the accepted --permission-mode values. manual is the
// current name of default; Claude accepts both.
func PermissionModes() []string {
	return []string{"default", "manual", "acceptEdits", "plan", "auto", "dontAsk", sandbox.PermissionModeBypass}
}

var (
	// Model aliases, ids and provider ARNs.
	modelPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`) })
	// Worktree names become a directory under .claude/worktrees and a branch name.
	worktreePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`) })
)

// Launch describes one claude process in a managed pane.
type Launch struct {
	// ClaudePath is the absolute path of the claude executable.
	ClaudePath string
	// SettingsPath is the absolute path of the per-launch settings file.
	SettingsPath string
	// Home expands "~/" in AddDirs, MCPConfigs and PluginDirs: the process is
	// started without a shell, so nothing else would.
	Home string
	// SessionName is the tmux session, also used as the Claude session name.
	SessionName    string
	Model          string
	Effort         string
	PermissionMode string
	AddDirs        []string
	MCPConfigs     []string
	PluginDirs     []string
	// Worktree starts Claude in <repo>/.claude/worktrees/<name> when set.
	Worktree   string
	Fullscreen bool
	Teams      bool
	// Sandbox is the resolution the settings file was built from.
	Sandbox sandbox.Resolution
	// SocketPath is the absolute path of the lyna-tmux server socket.
	SocketPath string
	// ExtraArgs are user arguments appended after the managed flags.
	ExtraArgs []string
}

// Command is an argument vector and environment for exec, without a shell.
type Command struct {
	// Argv starts with the claude path.
	Argv []string
	// Env holds KEY=VALUE entries sorted by key, added to the inherited environment.
	Env []string
}

// BuildLaunch validates a launch and returns its command.
//
// Value flags use the --flag=value form. Claude's option parser lets a
// repeatable option such as --add-dir keep consuming the following
// non-option arguments, so the separate form would swallow a prompt given in
// ExtraArgs; the = form takes exactly one value, including values that start
// with '-'.
func BuildLaunch(l Launch) (Command, error) {
	if err := l.validate(); err != nil {
		return Command{}, err
	}
	argv := []string{l.ClaudePath, "--settings=" + l.SettingsPath, "--name=" + l.SessionName}
	if l.Model != "" {
		argv = append(argv, "--model="+l.Model)
	}
	if l.Effort != "" {
		argv = append(argv, "--effort="+l.Effort)
	}
	if l.PermissionMode != "" {
		argv = append(argv, "--permission-mode="+l.PermissionMode)
	}
	for _, group := range []struct {
		flag  string
		paths []string
	}{{"--add-dir", l.AddDirs}, {"--mcp-config", l.MCPConfigs}, {"--plugin-dir", l.PluginDirs}} {
		for _, p := range group.paths {
			argv = append(argv, group.flag+"="+expandHome(p, l.Home))
		}
	}
	if l.Worktree != "" {
		argv = append(argv, "--worktree="+l.Worktree)
	}
	argv = append(argv, l.ExtraArgs...)

	env := map[string]string{
		session.EnvManaged:             "1",
		session.EnvSocket:              l.SocketPath,
		session.EnvSession:             l.SessionName,
		session.EnvSandbox:             string(l.Sandbox.Profile),
		session.EnvClaudeTmuxTruecolor: "1",
	}
	if l.Fullscreen {
		env[session.EnvClaudeNoFlicker] = "1"
	}
	if l.Teams {
		env[session.EnvClaudeTeams] = "1"
	}
	for k, v := range l.Sandbox.Env {
		if prev, ok := env[k]; ok && prev != v {
			return Command{}, fmt.Errorf("%w: sandbox environment variable %s conflicts with a managed variable", ErrInvalid, k)
		}
		env[k] = v
	}
	if _, err := mergeEnv(env, nil); err != nil {
		return Command{}, err
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := Command{Argv: argv, Env: make([]string, 0, len(keys))}
	for _, k := range keys {
		out.Env = append(out.Env, k+"="+env[k])
	}
	return out, nil
}

func (l Launch) validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	for _, p := range []struct{ what, path string }{
		{"claude path", l.ClaudePath}, {"settings path", l.SettingsPath}, {"socket path", l.SocketPath},
	} {
		if !absolute(p.path) {
			add("%s must be an absolute path without control characters (got %q)", p.what, p.path)
		}
	}
	if err := session.Validate(l.SessionName); err != nil {
		add("session name: %v", err)
	}
	if l.Model != "" && !modelPattern().MatchString(l.Model) {
		add("model must be an alias or id such as opus or claude-opus-5 (got %q)", l.Model)
	}
	if l.Effort != "" && !slices.Contains(Efforts(), l.Effort) {
		add("effort must be one of %s (got %q)", strings.Join(Efforts(), ", "), l.Effort)
	}
	if l.PermissionMode != "" && !slices.Contains(PermissionModes(), l.PermissionMode) {
		add("permission mode must be one of %s (got %q)", strings.Join(PermissionModes(), ", "), l.PermissionMode)
	}
	if _, err := sandbox.ParseProfile(string(l.Sandbox.Profile)); err != nil || l.Sandbox.Profile == "" {
		add("sandbox resolution has no valid profile (got %q)", l.Sandbox.Profile)
	}
	if err := sandbox.CheckBypass(l.Sandbox.Profile, l.Sandbox.Isolation, l.PermissionMode); err != nil {
		add("%v", err)
	}
	if l.Worktree != "" && (!worktreePattern().MatchString(l.Worktree) || l.Worktree == "." || l.Worktree == "..") {
		add("worktree name uses letters, digits, '.', '_' and '-', up to 64 characters, not starting with '-' (got %q)", l.Worktree)
	}
	needsHome := false
	for _, group := range []struct {
		what  string
		paths []string
	}{{"add dir", l.AddDirs}, {"mcp config", l.MCPConfigs}, {"plugin dir", l.PluginDirs}} {
		for _, p := range group.paths {
			switch {
			case strings.HasPrefix(p, "~/") && printable(p):
				needsHome = true
			case !absolute(p):
				add("%s must be an absolute path or start with ~/ (got %q)", group.what, p)
			}
		}
	}
	if needsHome && !absolute(l.Home) {
		add("home directory must be absolute to expand ~/ paths (got %q)", l.Home)
	}
	problems = append(problems, checkExtraArgs(l.ExtraArgs, sandbox.BypassAllowed(l.Sandbox.Profile, l.Sandbox.Isolation))...)

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// extraArgDenied maps a flag that would undo what a launch guarantees to the
// reason it is refused. The status line keeps reporting the profile whatever
// these do, so an accepted one would make the report a lie: --settings
// replaces the per-launch file (hooks, status line and sandbox all disappear),
// --mcp-config adds servers the sandbox rules were not resolved for,
// --plugin-dir and --plugin-url add hooks and commands from outside the
// boundary, and --add-dir widens the working directories the strict profile
// confines reads and writes to.
var extraArgDenied = map[string]string{
	"--settings":        "lyna-tmux supplies the settings file",
	"--permission-mode": "use the permission mode option",
	"--mcp-config":      "MCP servers run outside the resolved sandbox; list them in claude.mcp_config",
	"--plugin-dir":      "plugins run hooks outside the resolved sandbox; list them in claude.plugin_dirs",
	"--plugin-url":      "plugins run hooks outside the resolved sandbox; install them from Claude Code itself",
	"--add-dir":         "extra working directories widen the sandbox; list them in claude.add_dirs",
}

// checkExtraArgs refuses user arguments that would undo what the launch
// guarantees: the flags of extraArgDenied, and the permission flags that would
// bypass the sandbox rule for bypassPermissions. Everything after a bare "--"
// is the prompt Claude receives, not an option, and is left alone.
func checkExtraArgs(args []string, bypassAllowed bool) []string {
	var problems []string
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			problems = append(problems, "extra arguments cannot contain NUL bytes")
			continue
		}
		if a == "--" {
			break
		}
		name, _, _ := strings.Cut(a, "=")
		if why, denied := extraArgDenied[name]; denied {
			problems = append(problems, "extra arguments cannot pass "+name+": "+why)
			continue
		}
		if (name == "--dangerously-skip-permissions" || name == "--allow-dangerously-skip-permissions") && !bypassAllowed {
			problems = append(problems, name+" requires the strict sandbox profile or container isolation")
		}
	}
	return problems
}

func expandHome(p, home string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

func absolute(p string) bool { return strings.HasPrefix(p, "/") && printable(p) }
