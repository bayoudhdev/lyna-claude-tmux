package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

func newConfigCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create, inspect, validate and edit the configuration file",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newConfigPathCmd(d),
		newConfigInitCmd(d),
		newConfigShowCmd(d),
		newConfigValidateCmd(d),
		newConfigEditCmd(d),
	)
	return cmd
}

func newConfigPathCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the configuration file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			paths, err := xdg.Resolve(h.Getenv, h.Home)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), paths.ConfigFile())
			return err
		},
	}
}

func newConfigInitCmd(d Deps) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a documented configuration file with the defaults",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			res, err := app.InitConfig(h, force)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.Backup != "" {
				fmt.Fprintf(out, "Saved the previous file to %s\n", res.Backup)
			}
			_, err = fmt.Fprintf(out, "Wrote %s\n", res.Path)
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing file, keeping a .bak copy")
	return cmd
}

func newConfigShowCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration: the file merged over the defaults",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			paths, err := xdg.Resolve(h.Getenv, h.Home)
			if err != nil {
				return err
			}
			cfg, found, err := config.Load(paths.ConfigFile())
			if err != nil {
				return err
			}
			data, err := config.Marshal(cfg)
			if err != nil {
				return err
			}
			source := "# Effective configuration from " + paths.ConfigFile()
			if !found {
				source = "# Defaults: there is no file at " + paths.ConfigFile()
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n%s", source, data)
			return err
		},
	}
}

func newConfigValidateCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			paths, err := xdg.Resolve(h.Getenv, h.Home)
			if err != nil {
				return err
			}
			return reportConfig(cmd, paths.ConfigFile())
		},
	}
}

func newConfigEditCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the configuration file in your editor, then check it",
		Long: "Open the configuration file in $VISUAL, $EDITOR or vi. A missing file is\n" +
			"created from the documented template first. The file is checked when the\n" +
			"editor exits; changes apply the next time a lyna-tmux command runs.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			res, err := app.InitConfig(h, false)
			if err != nil && !errors.Is(err, app.ErrConfigExists) && !errors.Is(err, app.ErrConfigSymlink) {
				return err
			}
			if err := d.Run(cmd.Context(), app.EditorArgv(h.Getenv, res.Path), streams(cmd)); err != nil {
				return fmt.Errorf("editor: %w", err)
			}
			return reportConfig(cmd, res.Path)
		},
	}
}

// reportConfig loads the file at path and prints whether it is valid.
func reportConfig(cmd *cobra.Command, path string) error {
	_, found, err := config.Load(path)
	if err != nil {
		return err
	}
	msg := path + " is valid"
	if !found {
		msg = "No file at " + path + ", the defaults are in use. Create one with: lmux config init"
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
	return err
}
