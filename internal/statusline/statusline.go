// Package statusline renders the line `lyna-tmux statusline` prints for Claude
// Code's statusLine command: model and effort, sandbox profile, context usage,
// session cost, directory and git branch, rate limits and output style.
//
// Claude Code runs the command after every assistant message, so rendering
// starts no subprocess and touches the filesystem only to read .git/HEAD.
// Everything taken from the session JSON or the repository is folded through
// sanitize.Line before it reaches the terminal, and the command never fails:
// unreadable input still prints a minimal line.
package statusline

import (
	"errors"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook/githead"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Environment read by the renderer. The managed launch exports
// session.EnvTheme, EnvIcons and EnvColor from the configuration; outside it
// they fall back to the defaults.
const (
	envNoColor = "NO_COLOR"
	// envColumns is the terminal width Claude Code sets before running the
	// command, whose own output is captured rather than a terminal.
	envColumns = "COLUMNS"

	defaultTheme = "lyna"
	// fieldCells caps any single text field so one long value cannot crowd
	// out the rest of the line.
	fieldCells = 32
	// maxColumns bounds the width taken from the environment.
	maxColumns = 4096
)

// Sandbox profiles reported in session.EnvSandbox.
const (
	sandboxStrict   = "strict"
	sandboxStandard = "standard"
	sandboxOff      = "off"
)

// Segment priorities: when the line is wider than the terminal, the lowest
// priority segments are dropped first.
const (
	prioModel     = 100
	prioSandbox   = 95
	prioContext   = 90
	prioBranch    = 70
	prioFiveHour  = 60
	prioSpend     = 60
	prioCost      = 50
	prioDirectory = 40
	prioSevenDay  = 30
	prioEffort    = 20
	prioStyle     = 10
)

// Context and limit usage turns from success to warning to danger at these
// percentages.
const (
	warnPercent   = 50
	dangerPercent = 80
)

// Run reads the session JSON from stdin and writes the rendered line to
// stdout. It returns the exit status, always 0.
func Run(stdin io.Reader, stdout io.Writer, getenv func(string) string, now time.Time) int {
	var data []byte
	if stdin != nil {
		if d, err := fsx.ReadLimited(stdin, MaxStdinBytes); err == nil {
			data = d
		}
	}
	_, _ = io.WriteString(stdout, Render(data, getenv, now)+"\n")
	return 0
}

// Render returns the status line for the session JSON in data, styled for the
// terminal described by getenv. now dates rate limit resets.
func Render(data []byte, getenv func(string) string, now time.Time) string {
	return render(data, getenv, now, githead.ReadFile)
}

func render(data []byte, getenv func(string) string, now time.Time, read githead.ReadFunc) string {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	st := newStyle(getenv)
	s, _ := Decode(data)

	segs := []segment{
		st.model(s.Model),
		st.effort(s.Effort),
		st.sandbox(getenv(session.EnvSandbox)),
		st.context(s.ContextWindow),
		st.cost(s.Cost),
		st.directory(s),
		st.branch(s, read),
		st.limit("5h", s.RateLimits.FiveHour, now, prioFiveHour),
		st.limit("7d", s.RateLimits.SevenDay, now, prioSevenDay),
		st.limit("spend", s.RateLimits.SpendLimit, now, prioSpend),
		st.outputStyle(s.OutputStyle),
	}
	return st.join(segs, columns(getenv))
}

func columns(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv(envColumns)))
	if err != nil || n <= 0 {
		return 0
	}
	return min(n, maxColumns)
}

// style is the resolved palette, icon set and color depth.
type style struct {
	pal   theme.Palette
	icons theme.Icons
	depth theme.Depth
	color bool
	tail  string
}

func newStyle(getenv func(string) string) style {
	pal, err := theme.Get(getenv(session.EnvTheme))
	if err != nil {
		pal, _ = theme.Get(defaultTheme)
	}
	icons, err := theme.GetIcons(theme.ResolveIcons(getenv(session.EnvIcons), getenv))
	if err != nil {
		icons, _ = theme.GetIcons(theme.ResolveIcons("auto", getenv))
	}
	depth, err := theme.ParseDepth(getenv(session.EnvColor))
	if err != nil {
		depth = theme.DetectDepth(getenv)
	}
	tail := "…"
	if icons.Name == "ascii" {
		tail = "..."
	}
	return style{pal: pal, icons: icons, depth: depth, color: getenv(envNoColor) == "", tail: tail}
}

// paint wraps text in an SGR foreground color, bold when asked.
func (st style) paint(c theme.Color, bold bool, text string) string {
	if !st.color || text == "" {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + 24)
	b.WriteString("\x1b[")
	if bold {
		b.WriteString("1;")
	}
	b.WriteString(Foreground(c, st.depth))
	b.WriteByte('m')
	b.WriteString(text)
	b.WriteString(sgrReset)
	return b.String()
}

const sgrReset = "\x1b[0m"

// Foreground returns the SGR parameters selecting c as the foreground color at
// depth d. Palette colors keep their index at every depth so they follow the
// terminal's scheme.
func Foreground(c theme.Color, d theme.Depth) string {
	if c.Indexed {
		return basic(int(c.ANSI))
	}
	switch d {
	case theme.DepthTrue:
		return "38;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
	case theme.Depth256:
		return "38;5;" + strconv.Itoa(theme.To256(c))
	}
	return basic(theme.To16(c))
}

// basic maps a 16-color index onto the normal (30-37) and bright (90-97)
// foreground codes, which every color terminal understands.
func basic(i int) string {
	if i < 8 {
		return strconv.Itoa(30 + i)
	}
	return strconv.Itoa(90 + i - 8)
}

// clip sanitizes an untrusted value and caps its width.
func (st style) clip(s string) string {
	return ansi.Truncate(sanitize.Line(s), fieldCells, st.tail)
}

// level picks the usage color for a percentage.
func (st style) level(pct float64) theme.Color {
	switch {
	case pct >= dangerPercent:
		return st.pal.Danger
	case pct >= warnPercent:
		return st.pal.Warning
	}
	return st.pal.Success
}

// segment is one rendered part of the line.
type segment struct {
	text     string
	priority int
	// glue joins the segment to the one before it with a space instead of a
	// separator, as long as that one is shown.
	glue bool
}

func (st style) model(m Model) segment {
	name := st.clip(m.DisplayName)
	if name == "" {
		name = "Claude"
	}
	return segment{text: st.paint(st.pal.Accent, true, st.icons.Claude+" "+name), priority: prioModel}
}

func (st style) effort(e Effort) segment {
	return segment{text: st.paint(st.pal.Muted, false, st.clip(e.Level)), priority: prioEffort, glue: true}
}

func (st style) sandbox(profile string) segment {
	switch profile {
	case sandboxStrict:
		return segment{text: st.paint(st.pal.Success, false, st.icons.Shield+" strict"), priority: prioSandbox}
	case sandboxStandard:
		return segment{text: st.paint(st.pal.Muted, false, st.icons.Shield+" sandbox"), priority: prioSandbox}
	case sandboxOff:
		return segment{text: st.paint(st.pal.Danger, true, st.icons.ShieldOff+" sandbox off"), priority: prioSandbox}
	}
	return segment{}
}

func (st style) context(c ContextWindow) segment {
	if !c.UsedPercentage.Valid {
		return segment{}
	}
	pct := clampPercent(c.UsedPercentage.Value)
	return segment{
		text:     st.paint(st.pal.Muted, false, "ctx ") + st.paint(st.level(pct), false, formatPercent(pct)),
		priority: prioContext,
	}
}

func (st style) cost(c Cost) segment {
	if !c.TotalCostUSD.Valid {
		return segment{}
	}
	return segment{text: st.paint(st.pal.Text, false, formatCost(c.TotalCostUSD.Value)), priority: prioCost}
}

// currentDir is the session directory: workspace.current_dir, or cwd from
// older releases.
func currentDir(s Status) string {
	if s.Workspace.CurrentDir != "" {
		return s.Workspace.CurrentDir
	}
	return s.Cwd
}

func (st style) directory(s Status) segment {
	dir := currentDir(s)
	if dir == "" {
		return segment{}
	}
	return segment{text: st.paint(st.pal.Text, false, st.clip(filepath.Base(dir))), priority: prioDirectory}
}

// branch reads the checked-out branch from the repository holding the
// session directory, so it is current even when Claude Code has not sent an
// update since the last checkout. A worktree session's reported branch is
// used when the repository metadata cannot be read.
func (st style) branch(s Status, read githead.ReadFunc) segment {
	label := ""
	if dir := currentDir(s); filepath.IsAbs(dir) {
		b, err := githead.Branch(dir, read)
		switch {
		case err == nil:
			label = b
		case !errors.Is(err, githead.ErrNoRepository):
			label = s.Worktree.Branch
		}
	} else {
		label = s.Worktree.Branch
	}
	label = st.clip(label)
	if label == "" {
		return segment{}
	}
	return segment{text: st.paint(st.pal.Accent2, false, st.icons.Branch+" "+label), priority: prioBranch, glue: true}
}

// limit renders one rate limit window with the time left until it resets. A
// window whose reset time has passed no longer applies and is left out.
func (st style) limit(label string, w RateWindow, now time.Time, priority int) segment {
	if !w.UsedPercentage.Valid {
		return segment{}
	}
	left := ""
	if w.ResetsAt.Valid {
		d, ok := untilReset(w.ResetsAt.Value, now)
		if !ok {
			return segment{}
		}
		left = " " + st.paint(st.pal.Muted, false, formatDuration(d))
	}
	pct := clampPercent(w.UsedPercentage.Value)
	return segment{
		text:     st.paint(st.pal.Muted, false, label+" ") + st.paint(st.level(pct), false, formatPercent(pct)) + left,
		priority: priority,
	}
}

func (st style) outputStyle(o OutputStyle) segment {
	name := st.clip(o.Name)
	if name == "" || strings.EqualFold(name, "default") {
		return segment{}
	}
	return segment{text: st.paint(st.pal.Muted, false, "style ") + st.paint(st.pal.Text, false, name), priority: prioStyle}
}

// join lays the segments out on one line no wider than limit cells (0 means
// unlimited), dropping the lowest priority segments first and truncating the
// last one standing.
func (st style) join(segs []segment, limit int) string {
	kept := make([]bool, len(segs))
	widths := make([]int, len(segs))
	for i, s := range segs {
		kept[i] = s.text != ""
		widths[i] = ansi.StringWidth(s.text)
	}
	// Muted rather than Border: the line is drawn on the terminal's own
	// background, where a border tone can vanish (it maps to black at 16
	// colors).
	sep := " " + st.paint(st.pal.Muted, false, st.icons.Sep) + " "
	sepWidth := ansi.StringWidth(sep)

	width := func() (total, shown int) {
		prev := -1
		for i := range segs {
			if !kept[i] {
				continue
			}
			if shown > 0 {
				if segs[i].glue && prev == i-1 {
					total++
				} else {
					total += sepWidth
				}
			}
			total += widths[i]
			shown++
			prev = i
		}
		return total, shown
	}

	if limit > 0 {
		for {
			total, shown := width()
			if total <= limit || shown <= 1 {
				break
			}
			drop := -1
			for i := range segs {
				if kept[i] && (drop < 0 || segs[i].priority <= segs[drop].priority) {
					drop = i
				}
			}
			kept[drop] = false
		}
	}

	var b strings.Builder
	prev := -1
	for i, s := range segs {
		if !kept[i] {
			continue
		}
		if prev >= 0 {
			if s.glue && prev == i-1 {
				b.WriteByte(' ')
			} else {
				b.WriteString(sep)
			}
		}
		b.WriteString(s.text)
		prev = i
	}
	line := b.String()
	if limit > 0 && ansi.StringWidth(line) > limit {
		line = ansi.Truncate(line, limit, st.tail)
		// The cut can fall inside a colored span.
		if st.color && !strings.HasSuffix(line, sgrReset) {
			line += sgrReset
		}
	}
	return line
}

// clampPercent bounds a reported percentage; spend limits may exceed 100.
func clampPercent(v float64) float64 {
	return math.Min(math.Max(v, 0), 9999)
}

func formatPercent(v float64) string {
	return strconv.FormatFloat(math.Round(v), 'f', 0, 64) + "%"
}

// formatCost renders dollars with cents, and whole dollars from $1000 on.
func formatCost(v float64) string {
	v = math.Max(v, 0)
	switch {
	case v >= 1e6:
		return "$999999+"
	case v >= 1000:
		return "$" + strconv.FormatFloat(math.Floor(v), 'f', 0, 64)
	}
	return "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

// untilReset returns the time left before resetsAt (Unix seconds), and false
// once it has passed.
func untilReset(resetsAt float64, now time.Time) (time.Duration, bool) {
	left := resetsAt - float64(now.Unix())
	if left <= 0 || math.IsNaN(left) {
		return 0, false
	}
	// Anything beyond a year is shown as a year rather than overflowing.
	const year = 365 * 24 * 3600
	return time.Duration(math.Min(left, year)) * time.Second, true
}

// formatDuration renders a countdown compactly: 45m, 3h05m, 2d04h. It rounds
// up so a window never shows 0m while it still applies.
func formatDuration(d time.Duration) string {
	mins := int64(math.Ceil(d.Minutes()))
	switch {
	case mins < 60:
		return strconv.FormatInt(mins, 10) + "m"
	case mins < 24*60:
		return strconv.FormatInt(mins/60, 10) + "h" + pad2(mins%60) + "m"
	}
	hours := mins / 60
	return strconv.FormatInt(hours/24, 10) + "d" + pad2(hours%24) + "h"
}

func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}
