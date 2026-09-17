package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// settingsGrace keeps generated settings files that no pane references for a
// while, so a launch that is still starting never loses its file to a
// concurrent prune.
const settingsGrace = 24 * time.Hour

// maxUserSettings bounds the read of the user's own Claude settings file.
const maxUserSettings = 4 << 20

// LaunchOptions are the per-invocation choices for Claude panes. Empty fields
// take the configuration value.
type LaunchOptions struct {
	Model          string
	Effort         string
	PermissionMode string
	// Sandbox is the profile name. The off profile is honored only here, as an
	// explicit request, never from the configuration file.
	Sandbox   string
	Isolation string
	// Continue resumes the most recent conversation in the project.
	Continue bool
	// Command replaces claude.command. Only the popup path of plugin mode sets
	// it, from the @claude_command option; no command line flag does.
	Command string
	// Args replaces claude.args when it is not nil, an empty list included;
	// ExtraArgs still follow. Only the popup path of plugin mode sets it, from
	// the @claude_args option.
	Args      []string
	ExtraArgs []string
	// Teams turns agent teams on for this launch even when claude.teams is
	// off in the configuration.
	Teams bool
}

// ErrSandboxOffInConfig reports a configuration that turns the sandbox off
// without an explicit request on the command line.
var ErrSandboxOffInConfig = errors.New(`sandbox.profile = "off" in the configuration is not honored`)

// ErrIsolationUnsupported reports an isolation level that cannot run here.
var ErrIsolationUnsupported = errors.New("isolation level unavailable")

// launchPlan holds what every Claude pane of one workspace shares.
type launchPlan struct {
	base     claudecfg.Launch
	settings string
	// display carries the look settings the hook and status line commands
	// read from the Claude process environment.
	display []string
	sandbox sandbox.Resolution
}

// prepareLaunch resolves the claude executable and the sandbox, writes the
// per-launch settings file and returns the shared launch description.
func (s *Server) prepareLaunch(h Host, root, name string, o LaunchOptions) (launchPlan, error) {
	cfg := s.Config
	profileName := o.Sandbox
	if profileName == "" {
		profileName = cfg.Sandbox.Profile
		if profileName == string(sandbox.Off) {
			return launchPlan{}, fmt.Errorf("%w: pass --sandbox off to launch without a sandbox", ErrSandboxOffInConfig)
		}
	}
	profile, err := sandbox.ParseProfile(profileName)
	if err != nil {
		return launchPlan{}, err
	}
	isolationName := o.Isolation
	if isolationName == "" {
		isolationName = cfg.Sandbox.Isolation
	}
	isolation, err := sandbox.ParseIsolation(isolationName)
	if err != nil {
		return launchPlan{}, err
	}
	if err := isolationAvailable(h, isolation); err != nil {
		return launchPlan{}, err
	}
	res, err := sandbox.Resolve(sandbox.Input{
		Profile:   profile,
		Isolation: isolation,
		Ecosystems: sandbox.DetectEcosystems(func(rel string) bool {
			_, err := os.Stat(filepath.Join(root, rel))
			return err == nil
		}),
		Extras: sandbox.Extras{
			AllowWrite:       cfg.Sandbox.AllowWrite,
			DenyRead:         cfg.Sandbox.DenyRead,
			AllowedDomains:   cfg.Sandbox.AllowedDomains,
			ExcludedCommands: cfg.Sandbox.ExcludedCommands,
		},
	})
	if err != nil {
		return launchPlan{}, err
	}
	permissionMode := pick(o.PermissionMode, cfg.Claude.PermissionMode)
	if err := sandbox.CheckBypass(res.Profile, res.Isolation, permissionMode); err != nil {
		return launchPlan{}, err
	}
	claudePath, err := claude.ResolveCommand(pick(o.Command, cfg.Claude.Command), h.lookPath(), h.Getenv, h.Home, claude.IsExecutable)
	if err != nil {
		return launchPlan{}, err
	}
	mode, err := claudecfg.ParseStatusLineMode(cfg.Claude.Statusline)
	if err != nil {
		return launchPlan{}, err
	}
	userHas, err := userHasStatusLine(h)
	if err != nil {
		return launchPlan{}, err
	}
	bell := "1"
	if !cfg.Claude.Bell {
		bell = "0"
	}
	display := []string{
		session.EnvBell + "=" + bell,
		session.EnvColor + "=" + cfg.UI.Color,
		session.EnvIcons + "=" + cfg.UI.Icons,
		session.EnvTheme + "=" + cfg.UI.Theme,
	}
	teams := cfg.Claude.Teams || o.Teams
	launcher := ""
	if teams && claudecfg.TeammateInWorkspace(cfg.Claude.TeammateMode) {
		launcher = s.teammateLauncher(h, claudePath, display)
	}
	data, err := claudecfg.BuildSettings(claudecfg.SettingsInput{
		Bin:               h.Exe,
		StatusLine:        mode,
		UserHasStatusLine: userHas,
		Sandbox:           res,
		Teams:             teams,
		TeammateMode:      cfg.Claude.TeammateMode,
		WorktreeBaseRef:   cfg.Claude.WorktreeBase,
		WorkflowSize:      cfg.Claude.WorkflowSize,
		NotifyChannel:     termx.NotifyChannel(termx.Detect(h.Getenv).Program),
	})
	if err != nil {
		return launchPlan{}, err
	}
	settings, err := claude.WriteSettings(s.Paths.SettingsDir(), data)
	if err != nil {
		return launchPlan{}, err
	}
	args := cfg.Claude.Args
	if o.Args != nil {
		args = o.Args
	}
	extra := append([]string(nil), args...)
	if o.Continue {
		extra = append(extra, "--continue")
	}
	extra = append(extra, o.ExtraArgs...)
	return launchPlan{settings: settings, display: display, sandbox: res, base: claudecfg.Launch{
		ClaudePath:       claudePath,
		SettingsPath:     settings,
		Home:             h.Home,
		SessionName:      name,
		Model:            pick(o.Model, cfg.Claude.Model),
		Effort:           pick(o.Effort, cfg.Claude.Effort),
		PermissionMode:   permissionMode,
		AddDirs:          cfg.Claude.AddDirs,
		MCPConfigs:       cfg.Claude.MCPConfig,
		PluginDirs:       cfg.Claude.PluginDirs,
		Fullscreen:       cfg.Claude.Fullscreen,
		Teams:            teams,
		TeammateLauncher: launcher,
		Sandbox:          res,
		SocketPath:       s.wsSocketPath(h),
		ExtraArgs:        extra,
	}}, nil
}

// teammateLauncher writes the script Claude Code runs in place of the agent
// when it opens a teammate, and returns its path. display is the look the
// workspace was opened with, which the script exports so a teammate draws its
// status line and its notifications the way the lead does.
//
// A workspace that cannot have one still opens, and its team still runs: the
// agent falls back to the launcher it would have used without us, which on a
// workspace is the tmux backend, so the teammates land in the window as they
// always did, without our label and our layout. The reason is in the
// diagnostic log, which is what doctor reports.
func (s *Server) teammateLauncher(h Host, claudePath string, display []string) string {
	// The look of the workspace is exported by the launcher rather than read
	// from the pane, because a teammate is started by the tmux server and
	// inherits nothing from the lead that started the team.
	env := append([]string{session.EnvClaudeTmuxTruecolor + "=1"}, display...)
	script, err := claudecfg.TeammateLauncher(claudecfg.Launcher{Lmux: h.Exe, Claude: claudePath, Env: env})
	if err == nil {
		var path string
		if path, err = claude.WriteLauncher(s.Paths.LaunchersDir(), script); err == nil {
			return path
		}
	}
	hook.Log(s.Paths.LogFile(), time.Now(), "teammate", "no launcher, teammates open the way the agent opens them: %v", err)
	return ""
}

// paneProcs returns what each pane of plan runs.
func (lp launchPlan) paneProcs(h Host, plan layout.Plan, root, name string) ([]tmux.PaneProcess, error) {
	procs := make([]tmux.PaneProcess, len(plan.Panes))
	for i, pane := range plan.Panes {
		switch pane.Role {
		case layout.RoleClaude:
			l := lp.base
			l.Worktree = pane.Worktree
			cmd, err := claudecfg.BuildLaunch(l)
			if err != nil {
				return nil, err
			}
			if cmd, err = isolate(h, lp.sandbox, root, cmd); err != nil {
				return nil, err
			}
			env := append(slices.Clip(cmd.Env), lp.display...)
			procs[i] = tmux.PaneProcess{Argv: cmd.Argv, Env: env, Options: map[string]string{
				tmux.OptSettings: lp.settings,
				// The agent's notifications and progress bar are escape
				// sequences for the outer terminal. The server keeps them off,
				// so a pane running the user's own programs cannot drive their
				// terminal or clipboard; the pane running the agent allows
				// them, because that is where they are meant to come from.
				tmux.OptPassthrough: "on",
			}}
		case layout.RoleShell:
		case layout.RoleChanges:
			procs[i] = tmux.PaneProcess{Argv: []string{h.Exe, "watch", "--session", name, "--dir", root}}
		case layout.RoleReview:
			procs[i] = tmux.PaneProcess{Argv: []string{h.Exe, "review", "--dir", root}}
		case layout.RoleCommand:
			procs[i] = tmux.PaneProcess{Shell: pane.Command}
		default:
			return nil, fmt.Errorf("pane %d: unknown role %q", i+1, pane.Role)
		}
	}
	return procs, nil
}

// pruneSettings removes generated settings files that no pane on the server
// references and that were not written recently.
func (s *Server) pruneSettings(ctx context.Context, current string) error {
	out, err := s.Client.Run(ctx, "list-panes", "-a", "-F", "#{"+tmux.OptSettings+"}")
	if err != nil && !errors.Is(err, tmux.ErrNoServer) {
		return err
	}
	keep := []string{current}
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			keep = append(keep, line)
		}
	}
	_, err = claude.PruneSettings(s.Paths.SettingsDir(), claude.PruneOptions{Keep: keep, MaxAge: settingsGrace})
	return err
}

// userHasStatusLine reports whether the user's own Claude settings define a
// status line. The file is only read.
func userHasStatusLine(h Host) (bool, error) {
	path := filepath.Join(xdg.ClaudeHome(h.Getenv, h.Home), "settings.json")
	data, err := fsx.ReadFileLimited(path, maxUserSettings)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	has, err := claudecfg.UserHasStatusLine(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return has, nil
}

// wsSocketPath is the absolute socket path of the server s talks to: the path
// of a client addressed by socket path (a tmux server the user runs, in plugin
// mode), else the path of the -L socket name.
func (s *Server) wsSocketPath(h Host) string {
	if s.Client != nil && s.Client.Socket().Path != "" {
		return s.Client.Socket().Path
	}
	return SocketPath(h.Getenv, s.SocketName)
}

// SocketPath is the absolute socket path tmux uses for a -L socket name: the
// resolved TMUX_TMPDIR (or /tmp), then tmux-<uid>.
func SocketPath(getenv func(string) string, name string) string {
	dir := getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), name)
}

func (h Host) lookPath() claude.LookPathFunc {
	if h.LookPath != nil {
		return h.LookPath
	}
	return exec.LookPath
}

func pick(flag, configured string) string {
	if flag != "" {
		return flag
	}
	return configured
}
