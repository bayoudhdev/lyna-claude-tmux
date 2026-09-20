package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Exit codes of `review`, part of its contract with scripts and doctor.
const (
	// reviewExitUnavailable: Neovim is missing or too old, or the plugin is
	// not installed or not verified. The editor's own launcher uses the
	// same code when :CodeDiff is missing.
	reviewExitUnavailable = domain.ExitUnavailable
	// reviewExitLaunch: the review could not be prepared or started.
	reviewExitLaunch = domain.ExitFailed
)

// reviewCommands are the code review and live changes commands.
func reviewCommands(d Deps) []*cobra.Command {
	return reviewCommandsWith(d, app.ReviewPlugin{})
}

// reviewCommandsWith builds the commands for a plugin source; tests install
// from a local repository and release server instead of the network.
func reviewCommandsWith(d Deps, src app.ReviewPlugin) []*cobra.Command {
	return []*cobra.Command{reviewCommand(d, src), watchCommand(d), gitCommand(d)}
}

// reviewFlags are the options of `review`.
type reviewFlags struct {
	staged, history, reverse bool
	inline, sideBySide       bool
	userNvim, popup          bool
	pr                       int
	prSet                    bool
	remote, base, dir        string
}

func reviewCommand(d Deps, src app.ReviewPlugin) *cobra.Command {
	var f reviewFlags
	cmd := &cobra.Command{
		Use:   "review [REV [REV2]] [-- paths]",
		Short: "Review the changes of a repository in a live side-by-side diff",
		Long: "Open a live review of a git repository: a side-by-side diff explorer that refreshes as\n" +
			"files change, with stage, unstage and discard per hunk. It runs codediff.nvim in Neovim:\n" +
			"by default a private Neovim setup with the pinned, verified plugin that\n" +
			"`lmux review install` puts in lyna-tmux's data directory; set review.editor = \"user\"\n" +
			"or pass --user-nvim to use your own Neovim configuration and plugins.\n\n" +
			"Without arguments the working tree and the index are reviewed. REV compares the working\n" +
			"tree with a revision, REV REV2 compares two revisions, and base... or base...target\n" +
			"compares with their merge base. Paths after -- limit the review.\n\n" +
			"Exit status 3 means Neovim or the plugin is missing, 4 that the review could not start.",
		Example: "  lmux review\n" +
			"  lmux review main...\n" +
			"  lmux review --staged -- src/\n" +
			"  lmux review --pr 42 --remote upstream\n" +
			"  lmux review --history HEAD~20.. -- README.md\n" +
			"  lmux review install",
		Args: func(cmd *cobra.Command, args []string) error {
			if before, _ := reviewSplitArgs(cmd, args); len(before) > 2 {
				return errors.New("pass at most two revisions; put paths after --")
			}
			return nil
		},
		// Arguments keep the default file completion, which paths after --
		// need: completion always parses the line as if it ended with --, so
		// a revision position cannot be told apart from a path position.
		RunE: func(cmd *cobra.Command, args []string) error {
			f.prSet = cmd.Flags().Changed("pr")
			before, paths := reviewSplitArgs(cmd, args)
			req, err := reviewBuildRequest(f, before, paths)
			if err != nil {
				return err
			}
			h, err := d.Host()
			if err != nil {
				return err
			}
			if req.Dir, err = reviewDir(d, h, f.dir); err != nil {
				return err
			}
			l, err := app.ReviewLaunch(cmd.Context(), h, src, req)
			if err == nil {
				err = d.Exec(l.Path, l.Args, l.Env)
				if err != nil {
					err = &exitError{code: reviewExitLaunch, err: fmt.Errorf("%w: start %s: %w", app.ErrReviewLaunch, sanitize.Line(l.Path), err)}
				}
			}
			if err == nil {
				return nil
			}
			err = reviewExit(err)
			// A popup or a review pane closes as soon as this process exits:
			// keep the reason on screen until it has been read.
			if d.Terminal().Interactive && (f.popup || app.ReviewOwnsPane(cmd.Context(), h, os.Getpid())) {
				reviewHold(cmd.InOrStdin(), cmd.ErrOrStderr(), err)
			}
			return err
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.staged, "staged", false, "review the index against HEAD, or against REV")
	fl.IntVar(&f.pr, "pr", 0, "review pull request `N` without checking it out")
	fl.StringVar(&f.remote, "remote", "", "remote to fetch the pull request from (default: origin)")
	fl.StringVar(&f.base, "base", "", "target branch of the pull request (default: asked from the remote)")
	fl.BoolVar(&f.history, "history", false, "walk the commits, limited to RANGE when given and to one file after --")
	fl.BoolVar(&f.reverse, "reverse", false, "list the --history commits oldest first")
	fl.BoolVar(&f.inline, "inline", false, "draw diffs inline (overrides review.layout)")
	fl.BoolVar(&f.sideBySide, "side-by-side", false, "draw diffs side by side (overrides review.layout)")
	fl.BoolVar(&f.userNvim, "user-nvim", false, "use your own Neovim configuration and plugins (overrides review.editor)")
	fl.BoolVar(&f.popup, "popup", false, "run inside a tmux popup: keep errors on screen until a key is pressed")
	fl.StringVar(&f.dir, "dir", "", "repository `DIR` to review (default: the current directory)")
	_ = cmd.MarkFlagDirname("dir")
	for _, name := range []string{"pr", "remote", "base"} {
		_ = cmd.RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)
	}
	cmd.AddCommand(reviewInstallCommand(d, src), reviewStatusCommand(d, src), reviewUninstallCommand(d))
	return cmd
}

// reviewSplitArgs separates the revisions from the paths after --.
func reviewSplitArgs(cmd *cobra.Command, args []string) (before, paths []string) {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return args[:n], args[n:]
	}
	return args, nil
}

// reviewBuildRequest turns flags and arguments into a review request. The
// domain validates the values; this rejects combinations no request can
// express, with the flags named as the user typed them.
func reviewBuildRequest(f reviewFlags, revs, paths []string) (app.ReviewRequest, error) {
	var modes []string
	for _, m := range []struct {
		on   bool
		flag string
	}{{f.staged, "--staged"}, {f.prSet, "--pr"}, {f.history, "--history"}} {
		if m.on {
			modes = append(modes, m.flag)
		}
	}
	switch {
	case len(modes) > 1:
		return app.ReviewRequest{}, fmt.Errorf("%s select different reviews: pass only one of --staged, --pr and --history", strings.Join(modes, " and "))
	case f.inline && f.sideBySide:
		return app.ReviewRequest{}, errors.New("--inline and --side-by-side are different layouts: pass only one")
	case !f.prSet && (f.remote != "" || f.base != ""):
		return app.ReviewRequest{}, errors.New("--remote and --base select a pull request: pass them with --pr")
	case f.reverse && !f.history:
		return app.ReviewRequest{}, errors.New("--reverse orders the commit list: pass it with --history")
	}

	r := domain.Request{Paths: paths}
	switch {
	case f.prSet:
		if len(revs) > 0 {
			return app.ReviewRequest{}, errors.New("--pr takes no revisions: the pull request selects them")
		}
		r.Mode, r.PR = domain.ModePR, domain.PR{Number: f.pr, Remote: f.remote, Base: f.base}
	case f.history:
		if len(revs) > 1 {
			return app.ReviewRequest{}, errors.New("--history takes at most one RANGE")
		}
		r.Mode, r.History.Reverse = domain.ModeHistory, f.reverse
		if len(revs) == 1 {
			r.History.Range = revs[0]
		}
	case f.staged:
		r.Mode, r.Revisions = domain.ModeStaged, revs
	case len(revs) > 0:
		r.Mode, r.Revisions = domain.ModeRevision, revs
	}
	switch {
	case f.inline:
		r.Layout = domain.LayoutInline
	case f.sideBySide:
		r.Layout = domain.LayoutSideBySide
	}
	req := app.ReviewRequest{Request: r}
	if f.userNvim {
		req.Editor = domain.EditorUser
	}
	return req, nil
}

// reviewDir resolves --dir against the working directory and the home
// directory; without it the review opens in the working directory, which is
// the pane's directory in a popup.
func reviewDir(d Deps, h app.Host, dir string) (string, error) {
	return expandDir(dir, h.Home, d.Getwd)
}

// reviewExit attaches the exit code a review error carries.
func reviewExit(err error) error {
	if _, ok := errors.AsType[*exitError](err); ok {
		return err
	}
	switch {
	case errors.Is(err, app.ErrReviewNeovim), errors.Is(err, app.ErrReviewPlugin):
		return &exitError{code: reviewExitUnavailable, err: err}
	case errors.Is(err, app.ErrReviewLaunch):
		return &exitError{code: reviewExitLaunch, err: err}
	}
	return err
}

// reviewHold shows the error and waits for Enter, q, Ctrl+C, Ctrl+D or the
// end of input. A terminal is put in raw mode so a single q is enough.
func reviewHold(in io.Reader, out io.Writer, err error) {
	fmt.Fprintf(out, "lmux review: %s\n\nPress Enter or q to close.\n", sanitize.Line(err.Error()))
	if f, ok := in.(*os.File); ok {
		fd := int(f.Fd())
		if term.IsTerminal(fd) {
			if state, rawErr := term.MakeRaw(fd); rawErr == nil {
				defer func() { _ = term.Restore(fd, state) }()
			}
		}
	}
	reviewWaitKey(in)
}

// reviewWaitKey reads until a closing key or the end of input.
func reviewWaitKey(in io.Reader) {
	var b [1]byte
	for {
		n, err := in.Read(b[:])
		if n == 1 {
			switch b[0] {
			case '\r', '\n', 'q', 'Q', 0x03, 0x04:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func reviewInstallCommand(d Deps, src app.ReviewPlugin) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the pinned codediff.nvim plugin for reviews",
		Long: "Install codediff.nvim at its pinned commit into lyna-tmux's data directory, with its\n" +
			"native diff library downloaded over HTTPS and checked against the pinned SHA256.\n" +
			"Your own Neovim configuration and plugin directories are never touched. An installation\n" +
			"that already matches the pin is kept unless --force is given.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			res, err := app.ReviewInstall(cmd.Context(), h, src, force)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			dir := sanitize.Line(res.PluginDir)
			if res.Reused {
				fmt.Fprintf(out, "codediff.nvim %s is already installed in %s (reinstall with --force)\n", res.Version, dir)
			} else {
				fmt.Fprintf(out, "Installed codediff.nvim %s (commit %s) in %s\n", res.Version, reviewShortCommit(res.Commit), dir)
			}
			// The plugin is useless without a supported Neovim: say so now
			// rather than at the first review.
			state, err := app.ReviewStatus(cmd.Context(), h, src)
			if err != nil {
				return err
			}
			for _, w := range append(state.Report.NvimProblems(), state.Report.Warnings()...) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even when a verified installation exists")
	return cmd
}

func reviewShortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// reviewStatusReport is `review status --json`. Lists are never null.
type reviewStatusReport struct {
	Editor   string             `json:"editor"`
	Ready    bool               `json:"ready"`
	Platform string             `json:"platform"`
	Plugin   reviewPluginReport `json:"plugin"`
	Neovim   reviewNvimReport   `json:"neovim"`
	Problems []string           `json:"problems"`
	Warnings []string           `json:"warnings"`
}

type reviewPluginReport struct {
	Dir           string             `json:"dir"`
	Installed     bool               `json:"installed"`
	Verified      bool               `json:"verified"`
	PinnedCommit  string             `json:"pinned_commit"`
	PinnedVersion string             `json:"pinned_version"`
	Commit        string             `json:"commit"`
	Version       string             `json:"version"`
	Files         []reviewFileReport `json:"files"`
	Unverified    []string           `json:"unverified"`
}

type reviewFileReport struct {
	File       string `json:"file"`
	Present    bool   `json:"present"`
	ChecksumOK bool   `json:"checksum_ok"`
}

type reviewNvimReport struct {
	Path        string `json:"path"`
	Found       bool   `json:"found"`
	Version     string `json:"version"`
	Supported   bool   `json:"supported"`
	Recommended bool   `json:"recommended"`
}

func reviewNewStatusReport(s app.ReviewState) reviewStatusReport {
	r := s.Report
	rep := reviewStatusReport{
		Editor:   string(s.Editor),
		Ready:    s.Ready(),
		Platform: r.Platform,
		Plugin: reviewPluginReport{
			Dir: r.PluginDir, Installed: r.Installed, Verified: r.PluginOK(),
			PinnedCommit: s.Pin.Commit, PinnedVersion: s.Pin.Version, Commit: r.Commit, Version: r.Version,
			Files: []reviewFileReport{}, Unverified: append([]string{}, r.Unverified...),
		},
		Neovim:   reviewNvimReport{Path: r.Nvim, Found: r.NvimFound, Supported: r.NvimSupported, Recommended: r.NvimRecommends},
		Problems: append([]string{}, s.Problems()...),
		Warnings: append([]string{}, r.Warnings()...),
	}
	if r.NvimFound {
		rep.Neovim.Version = r.NvimVersion.String()
	}
	for _, a := range r.Assets {
		rep.Plugin.Files = append(rep.Plugin.Files, reviewFileReport{File: a.File, Present: a.Present, ChecksumOK: a.ChecksumOK})
	}
	return rep
}

func reviewStatusCommand(d Deps, src app.ReviewPlugin) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether reviews can start: plugin, native library and Neovim",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			state, err := app.ReviewStatus(cmd.Context(), h, src)
			if err != nil {
				return err
			}
			rep := reviewNewStatusReport(state)
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			return reviewWriteStatus(out, rep)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func reviewWriteStatus(out io.Writer, rep reviewStatusReport) error {
	yesNo := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Editor\t%s\n", rep.Editor)
	fmt.Fprintf(w, "Plugin\tcodediff.nvim %s (commit %s)\n", rep.Plugin.PinnedVersion, reviewShortCommit(rep.Plugin.PinnedCommit))
	fmt.Fprintf(w, "Location\t%s\n", sanitize.Line(rep.Plugin.Dir))
	fmt.Fprintf(w, "Installed\t%s\n", yesNo(rep.Plugin.Installed, yesNo(rep.Plugin.Verified, "yes, verified", "yes, not verified"), "no"))
	for _, f := range rep.Plugin.Files {
		fmt.Fprintf(w, "File\t%s: %s\n", sanitize.Line(f.File), yesNo(f.ChecksumOK, "checksum ok", yesNo(f.Present, "checksum mismatch", "missing")))
	}
	nvim := "not found"
	if rep.Neovim.Found {
		nvim = sanitize.Line(rep.Neovim.Path) + " " + rep.Neovim.Version
	}
	fmt.Fprintf(w, "Neovim\t%s\n", nvim)
	fmt.Fprintf(w, "Ready\t%s\n", yesNo(rep.Ready, "yes", "no"))
	if err := w.Flush(); err != nil {
		return err
	}
	for _, section := range []struct {
		title string
		lines []string
	}{{"Problems", rep.Problems}, {"Warnings", rep.Warnings}} {
		if len(section.lines) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", section.title)
		for _, l := range section.lines {
			fmt.Fprintf(out, "  %s\n", sanitize.Line(l))
		}
	}
	return nil
}

func reviewUninstallCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the review plugin, its native library and the review editor files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			removed, err := app.ReviewUninstall(h)
			if err != nil {
				return err
			}
			if !removed {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "The review editor is not installed")
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Removed the review editor")
			return err
		},
	}
}
