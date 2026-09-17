package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/version"
)

// Errors of the dev container commands.
var (
	// ErrDevcontainerMissing reports a project without rendered dev container files.
	ErrDevcontainerMissing = errors.New("the project has no dev container")
	// ErrDevcontainerUnnamed reports an init whose target is neither the
	// working directory nor a directory the user named.
	ErrDevcontainerUnnamed = errors.New("the dev container directory is not the working directory")
	// ErrDevcontainerNoBinary reports an init that found no lyna-tmux binary
	// for the image.
	ErrDevcontainerNoBinary = errors.New("no lyna-tmux binary for the image")
)

// DevcontainerDir resolves the directory a devcontainer command works on:
// dir when one was named (relative to cwd), the working directory otherwise.
// Unlike the project root of the session commands it never walks up to an
// ancestor: a directory that is not a repository of its own would otherwise
// get its dev container written into whatever repository contains it.
func DevcontainerDir(cwd, dir string) (string, error) {
	if dir == "" {
		dir = cwd
	}
	if dir == "" {
		return "", errors.New("no working directory given")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return dir, nil
}

// DevcontainerInitRequest is what sandbox devcontainer init was asked for.
type DevcontainerInitRequest struct {
	// Dir receives the .devcontainer directory; empty means Cwd.
	Dir string
	// Cwd is the working directory of the process.
	Cwd string
	// Named is whether Dir came from the command line. An unnamed target
	// that is not Cwd is refused: it can only come from a resolution that
	// moved away from where the user stands.
	Named bool
	// Force replaces existing files.
	Force bool
	// Binary is a Linux build of lyna-tmux to stage in the image, and
	// Version a release to download instead; at most one is set. With
	// neither, the source is chosen: this executable when it already is a
	// Linux build for the image, a build of the checkout that contains Dir
	// or Cwd when go is on PATH, or the release this executable came from.
	Binary  string
	Version string
	// OS and Arch are the platform of this process; the image is built for
	// linux/Arch, the default platform of the docker engine on this machine.
	OS, Arch string
	// Identity is the build identity of this executable: its version decides
	// whether a release can be pinned, and a local build is stamped with it.
	Identity version.Info
}

// DevcontainerInitResult reports what init wrote.
type DevcontainerInitResult struct {
	// Dir is the directory the .devcontainer directory was written into.
	Dir string
	// Source says where the lyna-tmux in the image comes from.
	Source string
	// Written lists the written paths.
	Written []string
}

// DevcontainerInit renders the dev container into the .devcontainer directory
// of the requested directory, with sandbox.allowed_domains added to the
// firewall allowlist. Existing files are kept unless Force is set. A staged
// binary is written with the rendered files and added to the directory's
// .gitignore.
func DevcontainerInit(ctx context.Context, h Host, req DevcontainerInitRequest) (DevcontainerInitResult, error) {
	dir, err := DevcontainerDir(req.Cwd, req.Dir)
	if err != nil {
		return DevcontainerInitResult{}, err
	}
	if !req.Named && dir != filepath.Clean(req.Cwd) {
		return DevcontainerInitResult{}, fmt.Errorf("%w: refusing to write %s into %s; name the directory: lyna-tmux sandbox devcontainer init %s",
			ErrDevcontainerUnnamed, devcontainer.Dir, dir, dir)
	}
	_, cfg, err := LoadConfig(h)
	if err != nil {
		return DevcontainerInitResult{}, err
	}
	src, binary, desc, err := devcontainerSource(ctx, h, req, dir)
	if err != nil {
		return DevcontainerInitResult{}, err
	}
	files, err := devcontainer.Render(devcontainer.Options{
		Project:        devcontainer.ProjectName(dir),
		AllowedDomains: cfg.Sandbox.AllowedDomains,
		Source:         src,
	})
	if err != nil {
		return DevcontainerInitResult{}, err
	}
	if binary != nil {
		files[devcontainer.FileBinary] = binary
	}
	written, err := devcontainer.Write(dir, files, req.Force)
	if err != nil {
		return DevcontainerInitResult{}, err
	}
	if binary != nil {
		ignore, changed, err := devcontainer.IgnoreBinary(dir)
		if err != nil {
			return DevcontainerInitResult{}, err
		}
		if changed {
			written = append(written, ignore)
		}
	}
	return DevcontainerInitResult{Dir: dir, Source: desc, Written: written}, nil
}

// devcontainerSource picks where the lyna-tmux of the image comes from and
// returns the source, the bytes to stage (nil for a release) and a line
// describing the choice.
func devcontainerSource(ctx context.Context, h Host, req DevcontainerInitRequest, dir string) (devcontainer.Source, []byte, string, error) {
	switch {
	case req.Binary != "" && req.Version != "":
		return devcontainer.Source{}, nil, "", fmt.Errorf("%w: pass --binary or --version, not both", devcontainer.ErrSourceConflict)
	case req.Binary != "":
		data, err := devcontainerReadBinary(req.Binary, req.Arch)
		if err != nil {
			return devcontainer.Source{}, nil, "", err
		}
		return devcontainer.Source{BinarySHA256: devcontainer.Digest(data)}, data, "staged from " + req.Binary, nil
	case req.Version != "":
		return devcontainer.Source{Version: req.Version}, nil, "release " + req.Version + ", downloaded when the image builds", nil
	}
	// The reasons each default was passed over, for the failure at the end.
	var reasons []string
	if req.OS == "linux" {
		if data, err := devcontainerReadBinary(h.Exe, req.Arch); err == nil {
			return devcontainer.Source{BinarySHA256: devcontainer.Digest(data)}, data, "this executable, " + h.Exe, nil
		}
	} else {
		reasons = append(reasons, "this executable is a "+req.OS+"/"+req.Arch+" build")
	}
	goBin, err := h.lookPath()("go")
	if err != nil {
		reasons = append(reasons, "go is not on PATH")
	} else {
		root, ok := devcontainerCheckout(dir, req.Cwd)
		if !ok {
			reasons = append(reasons, "neither "+dir+" nor "+req.Cwd+" is inside a checkout of "+devcontainer.ModulePath)
		} else {
			data, err := devcontainer.Build(ctx, goBin, root, req.Arch, devcontainer.Identity{
				Version: req.Identity.Version, Commit: req.Identity.Commit, Date: req.Identity.Date,
			})
			if err != nil {
				return devcontainer.Source{}, nil, "", err
			}
			return devcontainer.Source{BinarySHA256: devcontainer.Digest(data)}, data, "built from " + root + " for linux/" + req.Arch, nil
		}
	}
	if devcontainer.IsRelease(req.Identity.Version) {
		return devcontainer.Source{Version: req.Identity.Version}, nil, "release " + req.Identity.Version + ", downloaded when the image builds", nil
	}
	reasons = append(reasons, "version "+strconv.Quote(req.Identity.Version)+" is not a release")
	return devcontainer.Source{}, nil, "", fmt.Errorf("%w: %s. Stage a linux/%s build with --binary <path>, or pin a published release with --version vX.Y.Z",
		ErrDevcontainerNoBinary, strings.Join(reasons, ", "), req.Arch)
}

// devcontainerCheckout finds the checkout to build from: the one containing
// the target directory, or the one the command runs in.
func devcontainerCheckout(dirs ...string) (string, bool) {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if root, ok := devcontainer.ModuleRoot(dir); ok {
			return root, true
		}
	}
	return "", false
}

// devcontainerReadBinary reads a binary the user points at (links followed,
// as with any path they type) and checks it can run in the image.
func devcontainerReadBinary(path, goarch string) ([]byte, error) {
	data, err := fsx.ReadFileLimited(path, devcontainer.MaxBinaryBytes)
	if err != nil {
		return nil, err
	}
	if err := devcontainer.ValidateBinary(data, goarch); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

// DevcontainerTarget is the dev container of dir, the directory a devcontainer
// command resolved with DevcontainerDir or the project root of a create. With
// needFiles, the rendered Dockerfile must exist, as building the image
// requires it.
func DevcontainerTarget(dir string, needFiles bool) (devcontainer.Target, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return devcontainer.Target{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return devcontainer.Target{}, err
	}
	if !info.IsDir() {
		return devcontainer.Target{}, fmt.Errorf("%s is not a directory", root)
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
// container does not share it. The container is the one of the project root,
// as a session is.
func (s *Server) ContainerCreateFor(req CreateRequest, detach bool) (ContainerCreate, bool, error) {
	isolation, err := sandbox.ParseIsolation(pick(req.Launch.Isolation, s.Config.Sandbox.Isolation))
	if err != nil {
		return ContainerCreate{}, true, err
	}
	if isolation != sandbox.IsolationContainer || isolationInContainer() {
		return ContainerCreate{}, false, nil
	}
	root, err := projectRoot(req.Dir)
	if err != nil {
		return ContainerCreate{}, true, err
	}
	t, err := DevcontainerTarget(root, false)
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
