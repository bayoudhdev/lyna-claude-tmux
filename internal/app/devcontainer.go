package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
)

// ErrDevcontainerMissing reports a project without rendered dev container files.
var ErrDevcontainerMissing = errors.New("the project has no dev container")

// DevcontainerInit renders the dev container of the project that contains dir
// into its .devcontainer directory, with sandbox.allowed_domains added to the
// firewall allowlist. Existing files are kept unless force is set. It returns
// the written paths.
func DevcontainerInit(h Host, dir string, force bool) ([]string, error) {
	root, err := projectRoot(dir)
	if err != nil {
		return nil, err
	}
	_, cfg, err := LoadConfig(h)
	if err != nil {
		return nil, err
	}
	files, err := devcontainer.Render(devcontainer.Options{
		Project:        devcontainer.ProjectName(root),
		AllowedDomains: cfg.Sandbox.AllowedDomains,
	})
	if err != nil {
		return nil, err
	}
	return devcontainer.Write(root, files, force)
}

// DevcontainerTarget is the dev container of the project that contains dir.
// With needFiles, the rendered Dockerfile must exist, as building the image
// requires it.
func DevcontainerTarget(dir string, needFiles bool) (devcontainer.Target, error) {
	root, err := projectRoot(dir)
	if err != nil {
		return devcontainer.Target{}, err
	}
	t := devcontainer.Target{Dir: root, Project: devcontainer.ProjectName(root), User: devcontainer.DefaultUser}
	if err := t.Validate(); err != nil {
		return devcontainer.Target{}, err
	}
	if needFiles {
		dockerfile := filepath.Join(root, filepath.FromSlash(devcontainer.FileDockerfile))
		info, err := os.Lstat(dockerfile)
		if errors.Is(err, fs.ErrNotExist) {
			return devcontainer.Target{}, fmt.Errorf("%w: %s is missing; run `lyna-tmux sandbox devcontainer init` first", ErrDevcontainerMissing, dockerfile)
		}
		if err != nil {
			return devcontainer.Target{}, err
		}
		if !info.Mode().IsRegular() {
			return devcontainer.Target{}, fmt.Errorf("%w: %s is not a regular file", ErrDevcontainerMissing, dockerfile)
		}
	}
	return t, nil
}

// ContainerCreate is a create request that runs inside a dev container.
type ContainerCreate struct {
	Target devcontainer.Target
	// Args are the lyna-tmux arguments inside the container, starting with
	// the subcommand: create, or team when the launch wants agent teams.
	Args []string
}

// ContainerCreateFor reports whether req belongs in the project's dev
// container rather than on this host's tmux server, and how to create it
// there. It does not when the isolation level is not container, or when this
// process already runs inside a container. The flags of req are passed on,
// the directory is mapped below the /workspace mount, and the sandbox profile
// resolved from this host's configuration is passed explicitly, since the
// container does not share it.
func (s *Server) ContainerCreateFor(req CreateRequest, detach bool) (ContainerCreate, bool, error) {
	isolation, err := sandbox.ParseIsolation(pick(req.Launch.Isolation, s.Config.Sandbox.Isolation))
	if err != nil {
		return ContainerCreate{}, true, err
	}
	if isolation != sandbox.IsolationContainer || isolationInContainer() {
		return ContainerCreate{}, false, nil
	}
	t, err := DevcontainerTarget(req.Dir, false)
	if err != nil {
		return ContainerCreate{}, true, err
	}
	// The project root is found by walking up from the absolute directory,
	// so the directory is always below it.
	dir, err := filepath.Abs(req.Dir)
	if err != nil {
		return ContainerCreate{}, true, err
	}
	rel, err := filepath.Rel(t.Dir, dir)
	if err != nil || !filepath.IsLocal(rel) && rel != "." {
		return ContainerCreate{}, true, fmt.Errorf("%s is not inside the project %s", dir, t.Dir)
	}
	profile := req.Launch.Sandbox
	if profile == "" {
		profile = s.Config.Sandbox.Profile
		if profile == string(sandbox.Off) {
			return ContainerCreate{}, true, fmt.Errorf("%w: pass --sandbox off to launch without a sandbox", ErrSandboxOffInConfig)
		}
	}
	if _, err := sandbox.ParseProfile(profile); err != nil {
		return ContainerCreate{}, true, err
	}
	sub := "create"
	if req.Launch.Teams {
		sub = "team"
	}
	args := []string{sub}
	for _, f := range []struct{ name, value string }{
		{"layout", req.Layout},
		{"name", req.Name},
		{"model", req.Launch.Model},
		{"effort", req.Launch.Effort},
		{"mode", req.Launch.PermissionMode},
		{"sandbox", profile},
		// The inner process has the terminal of a docker exec, or none at
		// all when detached, so the size the auto layout needs is passed on.
		{"width", containerSize(req.Width)},
		{"height", containerSize(req.Height)},
	} {
		if f.value != "" {
			args = append(args, "--"+f.name+"="+f.value)
		}
	}
	args = append(args, "--isolation="+string(sandbox.IsolationContainer))
	if req.Launch.Continue {
		args = append(args, "--continue")
	}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, path.Join(devcontainer.WorkspacePath, filepath.ToSlash(rel)))
	if len(req.Launch.ExtraArgs) > 0 {
		args = append(append(args, "--"), req.Launch.ExtraArgs...)
	}
	return ContainerCreate{Target: t, Args: args}, true, nil
}

// containerSize renders a terminal dimension for the inner command line, and
// nothing for the unknown size zero.
func containerSize(cells int) string {
	if cells <= 0 {
		return ""
	}
	return strconv.Itoa(cells)
}
