package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
)

func sandboxCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Inspect the sandbox profiles and run workspaces in a dev container",
		Long: "Every Claude launch gets a sandbox profile (standard, strict or off) and an isolation\n" +
			"level (bash, process or container). These commands show what each profile protects,\n" +
			"the exact settings a launch uses, whether this machine can run them, and manage the\n" +
			"dev container of container isolation.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(sandboxProfilesCommand(d), sandboxShowCommand(d), sandboxStatusCommand(d), devcontainerCommand(d))
	return cmd
}

func sandboxProfilesCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "Show what each sandbox profile and isolation level protects and allows",
		Long: "Show what each sandbox profile protects and allows at the default bash isolation, and\n" +
			"what each isolation level confines. The descriptions are derived from the settings a\n" +
			"launch writes, before the project's ecosystems and your configured additions.",
		Example: "  lyna-tmux sandbox profiles",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			for i, p := range sandbox.Profiles() {
				res, err := sandbox.Resolve(sandbox.Input{Profile: p, Isolation: sandbox.IsolationBash})
				if err != nil {
					return err
				}
				label := string(p)
				if i == 0 {
					label += " (default)"
				}
				fmt.Fprintf(out, "%s\n", label)
				sandboxWriteSummary(out, sandbox.Describe(res), "  ", false, d.Terminal().Width)
				fmt.Fprintln(out)
			}
			fmt.Fprintln(out, "Isolation levels (sandbox.isolation or --isolation)")
			for _, i := range sandbox.Isolations() {
				fmt.Fprintf(out, "  %-10s %s\n", i, i.Boundary())
			}
			_, err := fmt.Fprintf(out, "\nbypassPermissions needs the strict profile or container isolation.\n")
			return err
		},
	}
}

// sandboxWriteSummary prints a sandbox summary as aligned rows. The boundary
// belongs to the isolation level, so profile listings leave it out. termWidth
// is the width of the terminal in cells, or zero when it is not known.
func sandboxWriteSummary(w io.Writer, s sandbox.Summary, indent string, withBoundary bool, termWidth int) {
	list := func(values []string, empty string) string {
		if len(values) == 0 {
			return empty
		}
		clean := make([]string, len(values))
		for i, v := range values {
			clean[i] = sanitize.Line(v)
		}
		return strings.Join(clean, ", ")
	}
	bash := "off"
	switch {
	case s.RequiresSandbox:
		bash = "on, and Bash refuses to run when it cannot start"
	case s.BashSandbox:
		bash = "on"
	}
	env := "inherited by subprocesses"
	if s.ScrubsSubprocessEnv {
		env = "credentials scrubbed from every subprocess (Bash, hooks, MCP servers)"
	}
	envFiles := map[string]string{
		sandbox.EnvFilesAsk:   "ask before reading",
		sandbox.EnvFilesDeny:  "never read",
		sandbox.EnvFilesAllow: "readable",
	}[s.EnvFiles]
	outside := "allowed"
	if s.BlocksReadsOutsideProject {
		outside = "blocked outside the working directories"
	}
	network := map[string]string{
		sandbox.NetworkOpen:      "open",
		sandbox.NetworkAsk:       "allowed hosts pass, other hosts ask",
		sandbox.NetworkAllowlist: "only allowed hosts",
	}[s.Network]
	fallback := map[string]string{
		sandbox.FallbackAsk:         "a blocked command may run outside the sandbox after a prompt",
		sandbox.FallbackNever:       "never",
		sandbox.FallbackUnsandboxed: "every command runs without a sandbox",
	}[s.UnsandboxedFallback]
	bypass := "refused"
	if s.BypassAllowed {
		bypass = "allowed"
	}
	rows := [][2]string{{"Bash sandbox", bash}}
	if withBoundary {
		rows = [][2]string{{"Boundary", s.Boundary}, {"Bash sandbox", bash}}
	}
	rows = append(rows, [][2]string{
		{"Denied files", list(s.DeniedFiles, "none")},
		{"Unset variables", list(s.DeniedEnvVars, "none")},
		{"Environment", env},
		{".env files", envFiles},
		{"Outside reads", outside},
		{"Network", network},
		{"Allowed hosts", list(s.Domains, "none")},
		{"Also writable", list(s.WritablePaths, "nothing outside the project")},
		{"Unsandboxed", fallback},
		{"bypassPermissions", bypass},
	}...)
	// A denied file list or a set of allowed hosts is longer than a terminal,
	// and the terminal would break it wherever the line ends, under no column
	// at all. Wrapped here, every line after the first keeps the value column.
	column := len(indent) + 19
	room := 0
	if termWidth-column >= sandboxMinRoom {
		room = termWidth - column
	}
	for _, r := range rows {
		for i, line := range termx.Wrap(r[1], room) {
			if i == 0 {
				fmt.Fprintf(w, "%s%-18s %s\n", indent, r[0], line)
				continue
			}
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", column), line)
		}
	}
}

// sandboxMinRoom is the narrowest value column worth wrapping into.
const sandboxMinRoom = 24

func sandboxShowCommand(d Deps) *cobra.Command {
	var dir, isolation string
	cmd := &cobra.Command{
		Use:   "show [profile]",
		Short: "Print the exact Claude settings a launch would use",
		Long: "Print the settings file a Claude launch in the project receives with --settings, byte\n" +
			"for byte: the sandbox, permissions, hooks and status line. The profile and isolation\n" +
			"level default to the configuration. Nothing is written, and an isolation level this\n" +
			"machine cannot run is shown too.",
		Example: "  lyna-tmux sandbox show\n" +
			"  lyna-tmux sandbox show strict --dir ~/src/api\n" +
			"  lyna-tmux sandbox show --isolation process | jq .sandbox",
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return names(sandbox.Profiles()), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			req := app.SandboxShowRequest{Isolation: isolation}
			if len(args) == 1 {
				req.Profile = args[0]
			}
			if req.Dir, err = d.infraDir(h, dir); err != nil {
				return err
			}
			data, err := app.SandboxShow(h, req)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "a directory of the project (default: the current directory)")
	cmd.Flags().StringVar(&isolation, "isolation", "", "isolation level: bash, process or container")
	_ = cmd.RegisterFlagCompletionFunc("isolation", sandboxCompleteIsolation)
	_ = cmd.MarkFlagDirname("dir")
	return cmd
}

func sandboxCompleteIsolation(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return names(sandbox.Isolations()), cobra.ShellCompDirectiveNoFileComp
}

func sandboxStatusCommand(d Deps) *cobra.Command {
	var (
		dir           string
		asJSON, popup bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the sandbox in effect and whether this machine can run it",
		Long: "Show the sandbox of the current workspace, or of the next launch in the project: the\n" +
			"profile and isolation level, the credential files and variables it hides, the network\n" +
			"allowlist, the unsandboxed fallback and the bypass policy, followed by the platform\n" +
			"checks for it. In a pane or popup of a workspace the profile is the one the workspace\n" +
			"was created with; elsewhere it comes from the configuration.",
		Example: "  lyna-tmux sandbox status\n" +
			"  lyna-tmux sandbox status --json --dir ~/src/api",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			hold := popup && d.Terminal().Interactive
			err := sandboxStatus(cmd, d, dir, asJSON)
			if hold {
				if err != nil {
					fmt.Fprintf(out, "lyna-tmux sandbox status: %s\n", sanitize.Line(err.Error()))
				}
				fmt.Fprint(out, "\nPress any key to close.")
				sandboxWaitKey(cmd.InOrStdin())
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "a directory of the project (default: the current directory)")
	f.BoolVar(&asJSON, "json", false, "print the status as JSON")
	f.BoolVar(&popup, "popup", false, "wait for a key before closing, for a tmux popup")
	_ = cmd.MarkFlagDirname("dir")
	return cmd
}

func sandboxStatus(cmd *cobra.Command, d Deps, dir string, asJSON bool) error {
	h, err := d.Host()
	if err != nil {
		return err
	}
	if dir, err = d.infraDir(h, dir); err != nil {
		return err
	}
	st, err := app.SandboxStatus(cmd.Context(), h, doctorSystem(), dir)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	if st.Source == app.SandboxSourceSession {
		fmt.Fprintf(out, "Sandbox of workspace %s (%s)\n", sanitize.Line(st.Session), sanitize.Line(st.Project))
	} else {
		fmt.Fprintf(out, "Sandbox for %s (from the configuration)\n", sanitize.Line(st.Project))
	}
	fmt.Fprintf(out, "  %-18s %s\n  %-18s %s\n", "Profile", st.Sandbox.Profile, "Isolation", st.Sandbox.Isolation)
	sandboxWriteSummary(out, st.Sandbox, "  ", true, d.Terminal().Width)
	ready := "ready"
	if !st.Ready {
		ready = "not ready"
	}
	fmt.Fprintf(out, "\nReadiness on this machine: %s\n", ready)
	return doctor.WriteText(out, st.Readiness, d.Terminal().Width)
}

// sandboxWaitKey waits for one key. A terminal is put in raw mode so any key
// counts, not only Enter.
func sandboxWaitKey(in io.Reader) {
	if f, ok := in.(*os.File); ok {
		fd := int(f.Fd())
		if term.IsTerminal(fd) {
			if state, err := term.MakeRaw(fd); err == nil {
				defer func() { _ = term.Restore(fd, state) }()
			}
		}
	}
	var b [1]byte
	_, _ = in.Read(b[:])
}
