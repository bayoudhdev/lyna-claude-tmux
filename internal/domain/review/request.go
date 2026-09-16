// Package review describes a live code review session: what to compare, how
// the :CodeDiff command of codediff.nvim receives it, how Neovim is started
// for it and which pinned plugin build lyna-tmux installs. It decides; the
// internal/review adapter installs, writes files and starts processes.
package review

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Mode is what a review compares.
type Mode string

// Review modes.
const (
	// ModeChanges explores the working tree and the index. It is the default.
	ModeChanges Mode = "changes"
	// ModeStaged explores the index against HEAD or one revision.
	ModeStaged Mode = "staged"
	// ModeRevision compares the working tree with one revision, two revisions
	// with each other, or a merge base with a target ("base...", "base...target").
	ModeRevision Mode = "revision"
	// ModePR reviews a pull request fetched from a remote without checking it out.
	ModePR Mode = "pr"
	// ModeHistory walks the commit list of the repository or of one file.
	ModeHistory Mode = "history"
)

// Modes lists the review modes in the order help text shows them.
func Modes() []Mode {
	return []Mode{ModeChanges, ModeStaged, ModeRevision, ModePR, ModeHistory}
}

// Layout is how a diff is drawn.
type Layout string

// Layouts. LayoutDefault leaves the choice to the plugin configuration.
const (
	LayoutDefault    Layout = ""
	LayoutInline     Layout = "inline"
	LayoutSideBySide Layout = "side-by-side"
)

// ParseLayout parses a layout name; "" and "default" mean LayoutDefault.
func ParseLayout(s string) (Layout, error) {
	switch s {
	case "", "default":
		return LayoutDefault, nil
	case string(LayoutInline):
		return LayoutInline, nil
	case string(LayoutSideBySide):
		return LayoutSideBySide, nil
	}
	return LayoutDefault, fmt.Errorf("review: unknown layout %q (want inline or side-by-side)", s)
}

// PR selects a pull request.
type PR struct {
	// Number is the pull request number, 1 to MaxPRNumber.
	Number int
	// Remote is the remote to fetch from; empty lets the plugin use origin.
	Remote string
	// Base is the target branch; empty lets the plugin ask the remote.
	Base string
}

// History selects a commit list.
type History struct {
	// Range limits the commits ("main..HEAD", "HEAD~20"); empty lists recent commits.
	Range string
	// Reverse lists the oldest commit first.
	Reverse bool
}

// Request is one review.
type Request struct {
	// Mode is what to compare; empty means ModeChanges.
	Mode Mode
	// Revisions are one or two revisions in ModeRevision and at most one
	// revision to compare the index with in ModeStaged.
	Revisions []string
	// PR is used in ModePR only.
	PR PR
	// History is used in ModeHistory only.
	History History
	// Paths limit the review to git pathspecs. ModeHistory accepts one file.
	Paths []string
	// Layout overrides the configured layout for this review.
	Layout Layout
}

// Limits on request values. The byte limits keep the argument list inside
// what an environment variable can carry to Neovim on every platform.
const (
	MaxRevisionBytes = 256
	MaxRemoteBytes   = 256
	MaxPathBytes     = 4096
	MaxPaths         = 256
	MaxPathsTotal    = 32 << 10
	MaxPRNumber      = 1<<31 - 1
)

// ErrInvalidRequest wraps every validation failure.
var ErrInvalidRequest = errors.New("invalid review request")

// ReservedWords are the first arguments :CodeDiff reads as subcommands. A
// revision spelled like one would start a different command.
func ReservedWords() []string {
	return []string{"install", "install!", "pr", "file", "dir", "history", "merge"}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// mode resolves the empty mode to the default.
func (r Request) mode() Mode {
	if r.Mode == "" {
		return ModeChanges
	}
	return r.Mode
}

// Validate checks that every value is well formed for the mode and that no
// value belonging to another mode is set, so a wiring mistake surfaces as an
// error instead of a silently different review.
func (r Request) Validate() error {
	mode := r.mode()
	if !slices.Contains(Modes(), mode) {
		return invalid("unknown mode %q", r.Mode)
	}
	if _, err := ParseLayout(string(r.Layout)); err != nil {
		return invalid("unknown layout %q", r.Layout)
	}
	if err := r.validateModeFields(mode); err != nil {
		return err
	}
	switch mode {
	case ModeStaged:
		if len(r.Revisions) > 1 {
			return invalid("staged mode compares the index with at most one revision")
		}
		for _, rev := range r.Revisions {
			if err := ValidateRevision(rev); err != nil {
				return err
			}
			if rangeOperator(rev) != "" {
				return invalid("revision %q: staged mode does not take a range", rev)
			}
		}
	case ModeRevision:
		if err := validateRevisions(r.Revisions); err != nil {
			return err
		}
	case ModePR:
		if err := r.PR.validate(); err != nil {
			return err
		}
	case ModeHistory:
		if r.History.Range != "" {
			if err := ValidateRevision(r.History.Range); err != nil {
				return err
			}
		}
	}
	return validatePaths(mode, r.Paths)
}

func (r Request) validateModeFields(mode Mode) error {
	if len(r.Revisions) > 0 && mode != ModeStaged && mode != ModeRevision {
		return invalid("revisions are only used in staged and revision modes")
	}
	if r.PR != (PR{}) && mode != ModePR {
		return invalid("pull request fields are only used in pr mode")
	}
	if r.History != (History{}) && mode != ModeHistory {
		return invalid("history fields are only used in history mode")
	}
	return nil
}

func validateRevisions(revs []string) error {
	switch len(revs) {
	case 1:
		return ValidateRevision(revs[0])
	case 2:
		for _, rev := range revs {
			if err := ValidateRevision(rev); err != nil {
				return err
			}
			// The plugin reads a range in the first revision and ignores the second.
			if op := rangeOperator(rev); op != "" {
				return invalid("revision %q: a range (%s) cannot be combined with a second revision", rev, op)
			}
		}
		return nil
	}
	return invalid("revision mode takes one or two revisions, got %d", len(revs))
}

// ValidateRevision checks one git revision for use as a :CodeDiff argument.
// Beyond git's own syntax it rules out what the plugin would read as a flag
// or a subcommand, and what Neovim's file name expansion would change: the
// plugin passes revisions through expand() to detect directory comparisons.
func ValidateRevision(rev string) error {
	switch {
	case rev == "":
		return invalid("revision is empty")
	case len(rev) > MaxRevisionBytes:
		return invalid("revision is longer than %d bytes", MaxRevisionBytes)
	case slices.Contains(ReservedWords(), rev):
		return invalid("revision %q is a :CodeDiff subcommand name", rev)
	case rev[0] == '-':
		return invalid("revision %q must not start with '-'", rev)
	case rev[0] == '.' || rev[0] == '/' || rev[0] == '~':
		return invalid("revision %q must not start with %q", rev, rev[0])
	}
	for i := range len(rev) {
		c := rev[i]
		if !revisionByte(c) {
			if c <= ' ' || c >= 0x7f {
				return invalid("revision %q contains whitespace, control or non-ASCII bytes", rev)
			}
			return invalid("revision %q contains %q", rev, c)
		}
	}
	if err := checkDots(rev); err != nil {
		return err
	}
	return checkBraces(rev)
}

func revisionByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("._/~^@{}:+-", c) >= 0
}

// rangeOperator returns ".." or "..." when rev holds a range, else "".
func rangeOperator(rev string) string {
	if strings.Contains(rev, "...") {
		return "..."
	}
	if strings.Contains(rev, "..") {
		return ".."
	}
	return ""
}

// checkDots allows a single ".." or "..." operator and no longer dot runs.
func checkDots(rev string) error {
	runs := 0
	for i := 0; i < len(rev); {
		if rev[i] != '.' {
			i++
			continue
		}
		j := i
		for j < len(rev) && rev[j] == '.' {
			j++
		}
		n := j - i
		if n > 3 {
			return invalid("revision %q contains a run of %d dots", rev, n)
		}
		if n >= 2 {
			runs++
		}
		i = j
	}
	if runs > 1 {
		return invalid("revision %q contains more than one range operator", rev)
	}
	return nil
}

// checkBraces accepts the git forms "@{...}" and "^{...}": balanced, not
// nested, without a range inside. Unbalanced braces make Neovim's expand()
// fail and brace lists would be expanded by the shell it runs.
func checkBraces(rev string) error {
	open := -1
	for i := range len(rev) {
		switch rev[i] {
		case '{':
			if open >= 0 {
				return invalid("revision %q nests braces", rev)
			}
			if i == 0 || (rev[i-1] != '@' && rev[i-1] != '^') {
				return invalid("revision %q: '{' must follow '@' or '^'", rev)
			}
			open = i
		case '}':
			if open < 0 {
				return invalid("revision %q has an unmatched '}'", rev)
			}
			if strings.Contains(rev[open:i], "..") {
				return invalid("revision %q has a range inside braces", rev)
			}
			open = -1
		}
	}
	if open >= 0 {
		return invalid("revision %q has an unmatched '{'", rev)
	}
	return nil
}

func (p PR) validate() error {
	if p.Number < 1 || p.Number > MaxPRNumber {
		return invalid("pull request number %d is outside 1..%d", p.Number, MaxPRNumber)
	}
	if p.Remote != "" {
		if err := validateRemote(p.Remote); err != nil {
			return err
		}
	}
	if p.Base != "" {
		if err := ValidateBranch(p.Base); err != nil {
			return err
		}
	}
	return nil
}

func validateRemote(remote string) error {
	switch {
	case len(remote) > MaxRemoteBytes:
		return invalid("remote name is longer than %d bytes", MaxRemoteBytes)
	case remote[0] == '-':
		return invalid("remote %q must not start with '-'", remote)
	case remote == "." || remote == "..":
		return invalid("remote %q is not a remote name", remote)
	}
	for i := range len(remote) {
		c := remote[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return invalid("remote %q contains %q (allowed: letters, digits, '.', '_', '-')", remote, c)
		}
	}
	return nil
}

// ValidateBranch checks a branch name. The plugin builds a fetch refspec
// "refs/heads/<branch>:<destination>" from it, so on top of the revision rules
// it excludes revision operators and anything git rejects in a ref name.
func ValidateBranch(branch string) error {
	if err := ValidateRevision(branch); err != nil {
		return err
	}
	for _, bad := range []string{":", "~", "^", "{", "}", "..", "//", "/."} {
		if strings.Contains(branch, bad) {
			return invalid("branch %q contains %q", branch, bad)
		}
	}
	switch {
	case branch == "@":
		return invalid("branch %q is not a branch name", branch)
	case strings.HasSuffix(branch, "/"), strings.HasSuffix(branch, "."), strings.HasSuffix(branch, ".lock"):
		return invalid("branch %q must not end with '/', '.' or '.lock'", branch)
	}
	return nil
}

func validatePaths(mode Mode, paths []string) error {
	if len(paths) > MaxPaths {
		return invalid("at most %d paths, got %d", MaxPaths, len(paths))
	}
	if mode == ModeHistory && len(paths) > 1 {
		return invalid("history mode follows one file, got %d paths", len(paths))
	}
	total := 0
	for _, p := range paths {
		if err := ValidatePath(p); err != nil {
			return err
		}
		if mode == ModeHistory {
			if err := checkExpandSafe(p); err != nil {
				return err
			}
		}
		total += len(p)
	}
	if total > MaxPathsTotal {
		return invalid("paths add up to %d bytes, more than %d", total, MaxPathsTotal)
	}
	return nil
}

// ValidatePath checks one pathspec. Spaces and shell punctuation are fine:
// pathspecs reach git as separate arguments.
func ValidatePath(p string) error {
	switch {
	case p == "":
		return invalid("path is empty")
	case len(p) > MaxPathBytes:
		return invalid("path is longer than %d bytes", MaxPathBytes)
	case !utf8.ValidString(p):
		return invalid("path %q is not valid UTF-8", p)
	case p[0] == '-':
		return invalid("path %q must not start with '-'", p)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return invalid("path %q contains the control character %s", p, strconv.QuoteRune(r))
		}
	}
	return nil
}

// checkExpandSafe rejects what Neovim's expand() rewrites. The history
// subcommand passes its file argument through expand(), which substitutes
// environment variables, the current file name and globs, and runs the
// contents of backticks as a shell command.
func checkExpandSafe(p string) error {
	if strings.IndexByte("~%#<", p[0]) >= 0 {
		return invalid("history path %q must not start with %q", p, p[0])
	}
	if i := strings.IndexAny(p, "`$\\{}*?[]"); i >= 0 {
		return invalid("history path %q contains %q, which Neovim would expand", p, p[i])
	}
	return nil
}

// ExitOnClose is the global :CodeDiff flag that quits Neovim when the review
// closes, so the pane or popup running it closes too.
const ExitOnClose = "--exit-on-close"

// Fargs returns the exact :CodeDiff argument list for the request.
func (r Request) Fargs() ([]string, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var args []string
	switch r.mode() {
	case ModeChanges:
		args = r.globalFlags()
		args = r.appendPathspecs(args)
	case ModeStaged:
		args = append(r.globalFlags(), "--staged")
		args = append(args, r.Revisions...)
		args = r.appendPathspecs(args)
	case ModeRevision:
		args = append(r.globalFlags(), r.Revisions...)
		args = r.appendPathspecs(args)
	case ModePR:
		// Values use the --name=value form so no value is ever read as a
		// separate token.
		args = []string{"pr", strconv.Itoa(r.PR.Number)}
		if r.PR.Remote != "" {
			args = append(args, "--remote="+r.PR.Remote)
		}
		if r.PR.Base != "" {
			args = append(args, "--base="+r.PR.Base)
		}
		args = append(args, r.globalFlags()...)
		args = r.appendPathspecs(args)
	case ModeHistory:
		args = []string{"history"}
		if r.History.Range != "" {
			args = append(args, r.History.Range)
		}
		args = append(args, r.Paths...)
		if r.History.Reverse {
			args = append(args, "--reverse")
		}
		args = append(args, r.globalFlags()...)
	}
	return args, nil
}

func (r Request) globalFlags() []string {
	flags := []string{ExitOnClose}
	if r.Layout != LayoutDefault {
		flags = append(flags, "--"+string(r.Layout))
	}
	return flags
}

func (r Request) appendPathspecs(args []string) []string {
	if len(r.Paths) == 0 {
		return args
	}
	return append(append(args, "--"), r.Paths...)
}
