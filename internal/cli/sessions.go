package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// lsEntry is one workspace in `ls --json`.
type lsEntry struct {
	Name     string    `json:"name"`
	Windows  int       `json:"windows"`
	Attached int       `json:"attached"`
	Created  time.Time `json:"created"`
	Project  string    `json:"project"`
	Layout   string    `json:"layout"`
	Sandbox  string    `json:"sandbox"`
}

func newLsCmd(d Deps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List workspaces",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, _, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			sessions, err := s.Sessions(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				entries := make([]lsEntry, 0, len(sessions))
				for _, x := range sessions {
					entries = append(entries, lsEntry{
						Name: x.Name, Windows: x.Windows, Attached: x.Attached, Created: x.Created.UTC(),
						Project: x.Project, Layout: x.Layout, Sandbox: x.Sandbox,
					})
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			if len(sessions) == 0 {
				_, err := fmt.Fprintln(out, "No workspaces. Start one with: lyna-tmux create")
				return err
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tWINDOWS\tATTACHED\tLAYOUT\tSANDBOX\tPROJECT")
			for _, x := range sessions {
				fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\n", x.Name, x.Windows, x.Attached,
					dash(sanitize.Line(x.Layout)), dash(sanitize.Line(x.Sandbox)), dash(sanitize.Line(x.Project)))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newAttachCmd(d Deps) *cobra.Command {
	var nested bool
	cmd := &cobra.Command{
		Use:     "attach [name]",
		Aliases: []string{"a"},
		Short:   "Attach this terminal to a workspace",
		Long:    "Attach this terminal to a workspace. Without a name, the only workspace is used.",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// tmux needs a terminal to hand the workspace to; without one it
			// exits with "open terminal failed" after the process was
			// replaced, where nothing of ours can explain it.
			if !d.Terminal().Interactive {
				return errors.New("this is not a terminal to attach; run lyna-tmux ls to list the workspaces instead")
			}
			ctx, h, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			} else if name, err = onlyWorkspace(cmd, s); err != nil {
				return err
			}
			if _, err := s.Sync(ctx); err != nil {
				return err
			}
			return d.attach(cmd, h, s, name, attachOptions(nested)...)
		},
	}
	cmd.Flags().BoolVar(&nested, "nested", false, "attach even from inside another tmux session")
	return cmd
}

// onlyWorkspace names the single running workspace.
func onlyWorkspace(cmd *cobra.Command, s *app.Server) (string, error) {
	sessions, err := s.Sessions(cmd.Context())
	if err != nil {
		return "", err
	}
	switch len(sessions) {
	case 0:
		return "", fmt.Errorf("%w running; start one with: lyna-tmux create", app.ErrNoWorkspace)
	case 1:
		return sessions[0].Name, nil
	}
	names := make([]string, len(sessions))
	for i, x := range sessions {
		names[i] = x.Name
	}
	return "", fmt.Errorf("several workspaces are running (%s): pass a name", strings.Join(names, ", "))
}

func newKillCmd(d Deps) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "kill [name]",
		Short: "End a workspace, or every workspace with --all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return errors.New("pass a workspace name or --all")
			}
			ctx, _, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			if all {
				if err := s.KillAll(ctx); err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Ended every workspace")
				return err
			}
			if err := s.Kill(ctx, args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Ended workspace %s\n", args[0])
			return err
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "end every workspace and stop the lyna-tmux server")
	return cmd
}

func newRenameCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name> <new-name>",
		Short: "Rename a workspace",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, s, err := d.openServer(cmd)
			if err != nil {
				return err
			}
			if err := s.Rename(ctx, args[0], args[1]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Renamed workspace %s to %s\n", args[0], args[1])
			return err
		},
	}
}
