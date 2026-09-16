package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// Where SandboxStatus found the profile.
const (
	SandboxSourceSession = "session"
	SandboxSourceConfig  = "config"
)

// sandboxPaneTimeout bounds the lookup of the pane's workspace, which runs in
// a popup the user is waiting on.
const sandboxPaneTimeout = 2 * time.Second

// sandboxResolve resolves the sandbox of a launch in root exactly as a
// workspace launch does: the flag values win, the off profile is honored only
// as a flag value, and ecosystems are detected from the project root.
func sandboxResolve(cfg config.Config, root, profileFlag, isolationFlag string) (sandbox.Resolution, error) {
	profileName := profileFlag
	if profileName == "" {
		profileName = cfg.Sandbox.Profile
		if profileName == string(sandbox.Off) {
			return sandbox.Resolution{}, fmt.Errorf("%w: pass --sandbox off to launch without a sandbox", ErrSandboxOffInConfig)
		}
	}
	profile, err := sandbox.ParseProfile(profileName)
	if err != nil {
		return sandbox.Resolution{}, err
	}
	isolation, err := sandbox.ParseIsolation(pick(isolationFlag, cfg.Sandbox.Isolation))
	if err != nil {
		return sandbox.Resolution{}, err
	}
	return sandbox.Resolve(sandbox.Input{
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
}

// SandboxShowRequest selects the launch whose settings SandboxShow returns.
type SandboxShowRequest struct {
	// Profile and Isolation replace the configured values when set.
	Profile, Isolation string
	// Dir is a directory of the project; its root decides the ecosystems.
	Dir string
}

// SandboxShow returns the settings file a Claude launch in req.Dir receives
// with --settings, byte for byte. It writes nothing, and it shows the
// settings of an isolation level this host cannot run.
func SandboxShow(h Host, req SandboxShowRequest) ([]byte, error) {
	root, err := projectRoot(req.Dir)
	if err != nil {
		return nil, err
	}
	_, cfg, err := LoadConfig(h)
	if err != nil {
		return nil, err
	}
	res, err := sandboxResolve(cfg, root, req.Profile, req.Isolation)
	if err != nil {
		return nil, err
	}
	if err := sandbox.CheckBypass(res.Profile, res.Isolation, cfg.Claude.PermissionMode); err != nil {
		return nil, err
	}
	mode, err := claudecfg.ParseStatusLineMode(cfg.Claude.Statusline)
	if err != nil {
		return nil, err
	}
	userHas, err := userHasStatusLine(h)
	if err != nil {
		return nil, err
	}
	return claudecfg.BuildSettings(claudecfg.SettingsInput{
		Bin:               h.Exe,
		StatusLine:        mode,
		UserHasStatusLine: userHas,
		Sandbox:           res,
		Teams:             cfg.Claude.Teams,
		WorktreeBaseRef:   cfg.Claude.WorktreeBase,
		WorkflowSize:      cfg.Claude.WorkflowSize,
	})
}

// SandboxState is the sandbox of the current workspace, or of the next
// launch in a directory.
type SandboxState struct {
	// Source is SandboxSourceSession when the profile was read from the
	// workspace of the current tmux pane, SandboxSourceConfig otherwise.
	Source  string          `json:"source"`
	Session string          `json:"session,omitempty"`
	Project string          `json:"project"`
	Sandbox sandbox.Summary `json:"sandbox"`
	// Readiness holds the platform checks for this profile and isolation.
	Readiness doctor.Report `json:"readiness"`
	// Ready reports that no readiness check failed.
	Ready bool `json:"ready"`
}

// SandboxStatus describes the sandbox in effect. In a pane or popup of a
// lyna-tmux workspace ($TMUX and $TMUX_PANE) the profile is the one the
// workspace was created with; elsewhere it is the configured one for the
// project that contains dir. The isolation level is the dev container when
// this process runs in one, the configured level otherwise. sys supplies the
// system access for the readiness checks.
func SandboxStatus(ctx context.Context, h Host, sys doctor.Deps, dir string) (SandboxState, error) {
	_, cfg, err := LoadConfig(h)
	if err != nil {
		return SandboxState{}, err
	}
	st := SandboxState{Source: SandboxSourceConfig}
	profileFlag, sessionIsolation := "", ""
	if ws, ok := sandboxPaneWorkspace(ctx, h); ok {
		st.Source, st.Session, st.Project, profileFlag = SandboxSourceSession, ws.session, ws.project, ws.profile
		sessionIsolation = ws.isolation
	}
	if !filepath.IsAbs(st.Project) {
		if st.Project, err = projectRoot(dir); err != nil {
			return SandboxState{}, err
		}
	}
	// The workspace records the level it started with; a process inside the
	// lyna-tmux dev container is in one whatever the workspace asked for. Any
	// other container is not the boundary it would claim, so the status keeps
	// reporting the level the workspace actually launched with.
	isolationFlag := sessionIsolation
	if isolationInContainer() {
		isolationFlag = string(sandbox.IsolationContainer)
	}
	res, err := sandboxResolve(cfg, st.Project, profileFlag, isolationFlag)
	if err != nil {
		return SandboxState{}, err
	}
	st.Sandbox = sandbox.Describe(res)
	sys.Getenv = h.Getenv
	sys.LookPath = h.lookPath()
	sys.SandboxProfile = string(res.Profile)
	sys.Isolation = string(res.Isolation)
	st.Readiness = doctor.NewReport(doctor.SandboxChecks(ctx, sys))
	st.Ready = !st.Readiness.Failed()
	return st, nil
}

// sandboxWorkspace is what a lyna-tmux session records about its sandbox.
type sandboxWorkspace struct {
	session, project, profile, isolation string
}

// sandboxPaneWorkspace reads the workspace of the tmux pane this process runs
// in, on the server $TMUX names. It reports false outside tmux, and for a
// session lyna-tmux did not create or a server that does not answer.
func sandboxPaneWorkspace(ctx context.Context, h Host) (sandboxWorkspace, bool) {
	socket, ok := tmux.SocketFromEnv(h.Getenv("TMUX"))
	pane := h.Getenv("TMUX_PANE")
	if !ok || !tmux.ValidPaneID(pane) {
		return sandboxWorkspace{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, sandboxPaneTimeout)
	defer cancel()
	client := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: ServerEnviron(h.Environ)})
	out, err := client.Display(ctx, pane, strings.Join([]string{
		"#{" + tmux.OptManaged + "}", "#{" + tmux.OptSandbox + "}", "#{session_name}", "#{" + tmux.OptProject + "}",
		"#{" + tmux.OptIsolation + "}",
	}, "\t"))
	if err != nil {
		return sandboxWorkspace{}, false
	}
	fields := strings.SplitN(out, "\t", 5)
	if len(fields) != 5 || fields[0] != "1" || fields[1] == "" || session.Validate(fields[2]) != nil {
		return sandboxWorkspace{}, false
	}
	return sandboxWorkspace{profile: fields[1], session: fields[2], project: fields[3], isolation: fields[4]}, true
}
