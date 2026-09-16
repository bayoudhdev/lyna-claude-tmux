package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

func devcontainerCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devcontainer",
		Short: "Create, start, enter and stop the dev container of container isolation",
		Long: "Container isolation runs the whole workspace inside a dev container: tmux, Claude, its\n" +
			"hooks and MCP servers. The container gets the project at /workspace, a firewall that\n" +
			"only lets the allowed domains out (sandbox.allowed_domains adds to them) and a volume\n" +
			"for the Claude configuration. The Docker socket is never mounted.",
		Example: "  lyna-tmux sandbox devcontainer init\n" +
			"  lyna-tmux sandbox devcontainer up\n" +
			"  lyna-tmux create --isolation container",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(devcontainerInitCommand(d), devcontainerUpCommand(d), devcontainerShellCommand(d), devcontainerDownCommand(d))
	return cmd
}

func devcontainerInitCommand(d Deps) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write the .devcontainer files into the project",
		Long: "Write devcontainer.json, a Dockerfile, the firewall script and the allowed domains into\n" +
			"the .devcontainer directory of the project that contains dir (the current directory by\n" +
			"default). Existing files are kept unless --force is given.",
		Example: "  lyna-tmux sandbox devcontainer init\n" +
			"  lyna-tmux sandbox devcontainer init ~/src/api --force",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, dir, err := d.devcontainerDir(args)
			if err != nil {
				return err
			}
			written, err := app.DevcontainerInit(h, dir, force)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, p := range written {
				fmt.Fprintf(out, "Wrote %s\n", sanitize.Line(p))
			}
			_, err = fmt.Fprintln(out, "Start it with: lyna-tmux sandbox devcontainer up")
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace existing files")
	return cmd
}

func devcontainerUpCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "up [dir]",
		Short: "Build the image, start the container and apply its firewall",
		Long: "Build the image from the project's .devcontainer directory, create or start the\n" +
			"container and apply the egress firewall. The image is the isolation boundary, so the\n" +
			"files must be the ones lyna-tmux renders: a build refuses any other content and names\n" +
			"the file to restore with `lyna-tmux sandbox devcontainer init --force`. Add hosts\n" +
			"through sandbox.allowed_domains in the configuration, then init --force and up again.",
		Example: "  lyna-tmux sandbox devcontainer up",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, docker, err := d.devcontainerTarget(cmd, args, true)
			if err != nil {
				return err
			}
			if err := docker.Up(cmd.Context(), t); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Container %s is running. Open the workspace in it with: lyna-tmux create --isolation container\n", t.Container())
			return err
		},
	}
}

func devcontainerShellCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "shell [dir]",
		Short:   "Open a shell in the running container",
		Example: "  lyna-tmux sandbox devcontainer shell",
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
		Example: "  lyna-tmux sandbox devcontainer down",
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

// devcontainerDir resolves the host and the optional directory argument.
func (d Deps) devcontainerDir(args []string) (app.Host, string, error) {
	h, err := d.Host()
	if err != nil {
		return app.Host{}, "", err
	}
	arg := ""
	if len(args) == 1 {
		arg = args[0]
	}
	dir, err := d.infraDir(h, arg)
	return h, dir, err
}

// devcontainerTarget resolves the container of the project and the docker
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
		return devcontainer.Docker{}, fmt.Errorf("container isolation needs Docker, and docker is not on PATH (run lyna-tmux doctor for the install command): %w", err)
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
