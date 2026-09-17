package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/version"
)

// devcontainerIdentity is the build identity of this executable, which init
// stamps into a local build and falls back to as the release to pin. Tests
// replace it to stand for a released or a development build.
var devcontainerIdentity = version.Get

func devcontainerCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devcontainer",
		Short: "Create, start, enter and stop the dev container of container isolation",
		Long: "Container isolation runs the whole workspace inside a dev container: tmux, Claude, its\n" +
			"hooks and MCP servers. The container gets the project at /workspace, a firewall that\n" +
			"only lets the allowed domains out (sandbox.allowed_domains adds to them) and a volume\n" +
			"for the Claude configuration. The Docker socket is never mounted. Every command works\n" +
			"on the directory given, or the current directory: never on a parent of it.",
		Example: "  lmux sandbox devcontainer init\n" +
			"  lmux sandbox devcontainer up\n" +
			"  lmux create --isolation container",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(devcontainerInitCommand(d), devcontainerUpCommand(d), devcontainerShellCommand(d), devcontainerDownCommand(d))
	return cmd
}

func devcontainerInitCommand(d Deps) *cobra.Command {
	var (
		force           bool
		binary, release string
	)
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write the .devcontainer files into the directory",
		Long: "Write devcontainer.json, a Dockerfile, the firewall script and the allowed domains into\n" +
			"the .devcontainer directory of dir (the current directory by default, never a parent of\n" +
			"it). Existing files are kept unless --force is given.\n\n" +
			"The lyna-tmux inside the image is, in this order: this executable when it already is a\n" +
			"Linux build for the image architecture; a build of the lyna-tmux checkout that contains\n" +
			"dir or the current directory, when go is on PATH; or the release this executable was\n" +
			"built from, downloaded when the image builds. Without any of these, init fails and names\n" +
			"the two flags that decide instead: --binary stages a Linux build of your own next to the\n" +
			"Dockerfile (mode 0755, ignored by git), --version pins a published release.",
		Example: "  lmux sandbox devcontainer init\n" +
			"  lmux sandbox devcontainer init ~/src/api --force\n" +
			"  lmux sandbox devcontainer init --binary ./dist/lmux-linux-arm64\n" +
			"  lmux sandbox devcontainer init --version v1.2.0",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			cwd, err := d.Getwd()
			if err != nil {
				return err
			}
			req := app.DevcontainerInitRequest{
				Cwd:      cwd,
				Named:    len(args) == 1,
				Force:    force,
				Version:  release,
				OS:       runtime.GOOS,
				Arch:     runtime.GOARCH,
				Identity: devcontainerIdentity(),
			}
			if req.Named {
				if req.Dir, err = expandDir(args[0], h.Home, d.Getwd); err != nil {
					return err
				}
			}
			if binary != "" {
				if req.Binary, err = expandDir(binary, h.Home, d.Getwd); err != nil {
					return err
				}
			}
			target, err := app.DevcontainerDir(cwd, req.Dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// The directory is named before anything is written, so a wrong
			// target is visible even when the write is refused.
			fmt.Fprintf(out, "Dev container directory: %s\n", sanitize.Line(filepath.Join(target, devcontainer.Dir)))
			res, err := app.DevcontainerInit(cmd.Context(), h, req)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "lyna-tmux in the image: %s\n", sanitize.Line(res.Source))
			for _, p := range res.Written {
				fmt.Fprintf(out, "Wrote %s\n", sanitize.Line(p))
			}
			_, err = fmt.Fprintln(out, "Start it with: lmux sandbox devcontainer up")
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace existing files")
	cmd.Flags().StringVar(&binary, "binary", "", "stage this Linux build of lyna-tmux in the image instead of building or downloading one")
	cmd.Flags().StringVar(&release, "version", "", "download this lyna-tmux release (vX.Y.Z) when the image builds")
	cmd.MarkFlagsMutuallyExclusive("binary", "version")
	return cmd
}

func devcontainerUpCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "up [dir]",
		Short: "Build the image, start the container and apply its firewall",
		Long: "Build the image from the .devcontainer directory of dir (the current directory by\n" +
			"default), create or start the container and apply the egress firewall. The image is the\n" +
			"isolation boundary, so the files must be the ones lyna-tmux renders: a build refuses any\n" +
			"other content and names the file to restore with `lmux sandbox devcontainer init\n" +
			"--force`, as it does when the lyna-tmux binary init staged is gone. Add hosts through\n" +
			"sandbox.allowed_domains in the configuration, then init --force and up again.",
		Example: "  lmux sandbox devcontainer up",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, docker, err := d.devcontainerTarget(cmd, args, true)
			if err != nil {
				return err
			}
			if err := docker.Up(cmd.Context(), t); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Container %s is running. Open the workspace in it with: lmux create --isolation container\n", t.Container())
			return err
		},
	}
}

func devcontainerShellCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "shell [dir]",
		Short:   "Open a shell in the running container",
		Example: "  lmux sandbox devcontainer shell",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !d.Terminal().Interactive {
				return errors.New("sandbox devcontainer shell needs a terminal")
			}
			t, docker, err := d.devcontainerTarget(cmd, args, false)
			if err != nil {
				return err
			}
			return docker.Shell(cmd.Context(), t)
		},
	}
}

func devcontainerDownCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "down [dir]",
		Short:   "Stop and remove the container, keeping its image and Claude volume",
		Example: "  lmux sandbox devcontainer down",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, docker, err := d.devcontainerTarget(cmd, args, false)
			if err != nil {
				return err
			}
			if err := docker.Down(cmd.Context(), t); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Container %s is removed; the image %s and the volume %s are kept.\n", t.Container(), t.Image(), t.Volume())
			return err
		},
	}
}

// devcontainerDir resolves the directory a devcontainer command works on: the
// optional argument, or the working directory. It is never an ancestor.
func (d Deps) devcontainerDir(args []string) (app.Host, string, error) {
	h, err := d.Host()
	if err != nil {
		return app.Host{}, "", err
	}
	cwd, err := d.Getwd()
	if err != nil {
		return app.Host{}, "", err
	}
	arg := ""
	if len(args) == 1 {
		if arg, err = expandDir(args[0], h.Home, d.Getwd); err != nil {
			return app.Host{}, "", err
		}
	}
	dir, err := app.DevcontainerDir(cwd, arg)
	return h, dir, err
}

// devcontainerTarget resolves the container of the directory and the docker
// driver for it.
func (d Deps) devcontainerTarget(cmd *cobra.Command, args []string, needFiles bool) (devcontainer.Target, devcontainer.Docker, error) {
	_, dir, err := d.devcontainerDir(args)
	if err != nil {
		return devcontainer.Target{}, devcontainer.Docker{}, err
	}
	t, err := app.DevcontainerTarget(dir, needFiles)
	if err != nil {
		return devcontainer.Target{}, devcontainer.Docker{}, err
	}
	docker, err := d.devcontainerDocker(cmd)
	return t, docker, err
}

// devcontainerDocker finds docker and returns a driver that runs attached
// steps (build, firewall, shell) on the command's streams and captures the
// others. The driver carries the render options of init, so `up` builds the
// files lyna-tmux renders or nothing at all.
func (d Deps) devcontainerDocker(cmd *cobra.Command) (devcontainer.Docker, error) {
	bin, err := d.LookPath("docker")
	if err != nil {
		return devcontainer.Docker{}, fmt.Errorf("container isolation needs Docker, and docker is not on PATH (run lmux doctor for the install command): %w", err)
	}
	h, err := d.Host()
	if err != nil {
		return devcontainer.Docker{}, err
	}
	_, cfg, err := app.LoadConfig(h)
	if err != nil {
		return devcontainer.Docker{}, err
	}
	s := streams(cmd)
	return devcontainer.Docker{
		Bin:     bin,
		Options: devcontainer.Options{AllowedDomains: cfg.Sandbox.AllowedDomains},
		Exec: devcontainer.ExecutorFunc(func(ctx context.Context, c devcontainer.Command) (string, error) {
			if c.Interactive {
				run := s
				if c.Stdin != nil {
					run.In = bytes.NewReader(c.Stdin)
				}
				return "", d.Run(ctx, c.Argv, run)
			}
			return devcontainer.Exec{}.Run(ctx, c)
		}),
	}, nil
}
