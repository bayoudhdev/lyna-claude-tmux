package tmux

import (
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// Look is the resolved visual configuration of the status line and borders.
type Look struct {
	Palette theme.Palette
	Depth   theme.Depth
	Icons   theme.Icons
	// Seps joins one segment of the status bar to the next; the zero value
	// draws no separator and lets a background end where the next begins.
	Seps  theme.Seps
	Clock bool
	// Buttons draws clickable status buttons; they need user ranges (tmux 3.4).
	Buttons bool
}

func (l Look) c(c theme.Color) string { return c.Tmux(l.Depth) }

// style renders one attribute block per attribute. Commas inside #[...] would
// end a #{?cond,a,b} branch early, so attributes are never combined.
func style(attrs ...string) string {
	var b strings.Builder
	for _, a := range attrs {
		b.WriteString("#[")
		b.WriteString(a)
		b.WriteString("]")
	}
	return b.String()
}

// text makes literal text safe in a status format: formats are expanded and
// passed through strftime there.
func text(s string) string { return DrawEscapeTime(s) }

// cond renders #{?condition,then,else}.
func cond(condition, then, otherwise string) string {
	return "#{?" + condition + "," + then + "," + otherwise + "}"
}

// segRight opens a segment of the left half of the bar on bg, coming from
// prev: the pointed separator carries prev's color into bg. The plain style
// names the background alone, so a segment ends where the next one starts.
func (l Look) segRight(prev, bg theme.Color) string {
	if !l.Seps.Powerline() {
		return style("bg=" + l.c(bg))
	}
	return style("bg="+l.c(bg), "fg="+l.c(prev)) + text(l.Seps.Right)
}

// segLeft opens a segment of the right half of the bar, where the separators
// point back at the segment before them.
func (l Look) segLeft(prev, bg theme.Color) string {
	if !l.Seps.Powerline() {
		return style("bg=" + l.c(bg))
	}
	return style("bg="+l.c(prev), "fg="+l.c(bg)) + text(l.Seps.Left) + style("bg="+l.c(bg))
}

// thin separates two segments that share a background.
func (l Look) thin() string {
	if !l.Seps.Powerline() {
		return ""
	}
	return style("fg="+l.c(l.Palette.Border)) + text(l.Seps.Thin)
}

// raised is the background the right half of the bar draws on: a segment of
// its own in the powerline style, the bar itself in the plain one.
func (l Look) raised() theme.Color {
	if l.Seps.Powerline() {
		return l.Palette.Surface
	}
	return l.Palette.Bg
}

// paneStateIn expands to one marker per pane of the window in the given state.
func paneStateIn(state string) string {
	return "#{P:#{?#{==:#{" + OptState + "}," + state + "},x,}}"
}

// agentDot draws the most urgent agent state of a window: waiting, then busy,
// then idle; nothing for windows without agent panes.
func (l Look) agentDot() string {
	i := l.Icons
	return cond("#{m:*x*,"+paneStateIn("waiting")+"}", style("fg="+l.c(l.Palette.Waiting))+text(i.Waiting)+" ",
		cond("#{m:*x*,"+paneStateIn("busy")+"}", style("fg="+l.c(l.Palette.Busy))+text(i.Busy)+" ",
			cond("#{m:*x*,"+paneStateIn("idle")+"}", style("fg="+l.c(l.Palette.Idle))+text(i.Idle)+" ", "")))
}

// StatusLeftFixed is how many columns StatusLeft spends on everything that is
// not the session name: the brand block (a space, an icon up to two columns
// wide, a space), the spaces around the name, and the cell that follows, which
// is a space in the plain style and a separator in the powerline one, where
// one more separator ends the brand block. tmux truncates status-left at
// status-left-length, counting expanded columns, so the limit is this plus the
// longest name a workspace can have.
func (l Look) StatusLeftFixed() int {
	if l.Seps.Powerline() {
		return 8
	}
	return 7
}

// StatusLeft is the brand block and session name.
func (l Look) StatusLeft() string {
	p := l.Palette
	b := style("bg="+l.c(p.Accent), "fg="+l.c(p.Bg), "bold") + " " + text(l.Icons.Brand) + " " +
		l.segRight(p.Accent, p.Surface) + style("fg="+l.c(p.Text), "nobold") + " #S " +
		l.segRight(p.Surface, p.Bg) + style("fg="+l.c(p.Muted))
	if !l.Seps.Powerline() {
		b += " "
	}
	return b
}

// WindowFormat is an inactive window tab.
func (l Look) WindowFormat() string {
	p := l.Palette
	return style("bg="+l.c(p.Bg), "fg="+l.c(p.Muted)) + " " + l.agentDot() +
		style("fg="+l.c(p.Muted)) + "#I" + style("fg="+l.c(p.Border)) + text(":") + style("fg="+l.c(p.Muted)) + "#W" +
		cond("#{window_zoomed_flag}", text(" [z]"), "") + " "
}

// WindowCurrentFormat is the active window tab.
func (l Look) WindowCurrentFormat() string {
	p := l.Palette
	return l.segRight(p.Bg, p.Surface) + style("fg="+l.c(p.Accent), "bold") + " " + l.agentDot() +
		style("fg="+l.c(p.Accent)) + "#I" + style("fg="+l.c(p.Muted)) + text(":") + style("fg="+l.c(p.Text)) + "#W" +
		cond("#{window_zoomed_flag}", style("fg="+l.c(p.Warning))+text(" [z]"), "") + " " +
		l.segRight(p.Surface, p.Bg) + style("nobold")
}

// countAll expands to one marker per pane on the server matching condition.
func countAll(condition string) string {
	return "#{n:#{S:#{W:#{P:#{?" + condition + ",x,}}}}}"
}

func (l Look) button(rng, label string) string {
	p := l.Palette
	body := style("fg="+l.c(p.Muted)) + " " + label + " "
	if !l.Buttons {
		return body
	}
	return "#[range=user|" + rng + "]" + body + "#[norange]"
}

// StatusRight is the button row, sandbox shield, branch and clock. The
// buttons stay on the bar; everything after them is one raised segment, and
// the clock closes it in the accent.
func (l Look) StatusRight() string {
	p, i := l.Palette, l.Icons
	raised := l.raised()
	var b strings.Builder

	if l.Buttons {
		b.WriteString(l.button(RangeSplit, text(i.Split+" split")))
		b.WriteString(l.button(RangeReview, text(i.Review+" review")))
		b.WriteString(l.button(RangeMenu, text(i.Menu)))
	}

	// Agents: waiting count in the waiting color when any agent needs the user.
	waiting := countAll("#{==:#{" + OptState + "},waiting}")
	claude := countAll("#{==:#{" + OptRole + "}," + RoleClaude + "}")
	agents := cond("#{!=:"+waiting+",0}",
		style("fg="+l.c(p.Waiting), "bold")+text(i.Waiting+" ")+waiting+text(" waiting")+style("nobold"),
		style("fg="+l.c(p.Muted))+text(i.Agents+" ")+claude)
	b.WriteString(l.segLeft(p.Bg, raised))
	b.WriteString(l.button(RangeAgents, agents))

	// Sandbox shield, only on workspaces lyna-tmux created. Off is loud: a
	// segment of its own that returns to the raised background after it.
	sandbox := "#{" + OptSandbox + "}"
	shield := cond("#{==:"+sandbox+",off}",
		l.segLeft(raised, p.Danger)+style("fg="+l.c(p.Bg), "bold")+text(" "+i.ShieldOff+" sandbox off ")+
			l.segLeft(p.Danger, raised)+style("nobold"),
		l.thin()+cond("#{==:"+sandbox+",strict}",
			style("fg="+l.c(p.Success))+text(" "+i.Shield+" strict "),
			style("fg="+l.c(p.Accent2))+text(" "+i.Shield+" std ")))
	shieldBlock := shield
	if l.Buttons {
		shieldBlock = "#[range=user|" + RangeSandbox + "]" + shield + "#[norange]"
	}
	b.WriteString(cond("#{"+OptManaged+"}", shieldBlock, ""))

	// Branch: hooks store it shortened and format-escaped (BranchOption), so
	// it is drawn literally and never truncated inside an escape pair.
	b.WriteString(cond("#{"+OptBranch+"}", l.thin()+style("fg="+l.c(p.Accent2))+text(" "+i.Branch+" ")+"#{"+OptBranch+"} ", ""))

	if l.Clock {
		clockBg, clockFg := p.Surface, p.Text
		if l.Seps.Powerline() {
			clockBg, clockFg = p.Accent, p.Bg
		}
		b.WriteString(l.segLeft(raised, clockBg) + style("fg="+l.c(clockFg)) + " ")
		if i.Clock != "" {
			b.WriteString(text(i.Clock + " "))
		}
		b.WriteString("%H:%M ")
	}
	return b.String()
}

// BorderFormat labels each pane by role with its agent state, or with the
// status its program exited on. A dead pane is kept on screen so the reason
// stays readable, and the label is what still says so once the pane has been
// scrolled or the program cleared the screen on its way out. tmux leaves
// pane_dead_status empty for some programs, so the number is only added when
// there is one.
func (l Look) BorderFormat() string {
	p, i := l.Palette, l.Icons
	role := "#{" + OptRole + "}"
	// A teammate is labeled with its own name, which the launcher stored
	// prepared for drawing (AgentOption), so it is inserted as it is. A pane
	// that lost the name still says what it is.
	teammate := text(i.Agents+" ") + cond("#{"+OptAgent+"}", "#{"+OptAgent+"}", text("teammate"))
	label := cond("#{==:"+role+","+RoleClaude+"}", text(i.Claude+" claude"),
		cond("#{==:"+role+","+RoleTeammate+"}", teammate,
			cond("#{==:"+role+",review}", text(i.Review+" review"),
				cond("#{==:"+role+","+RoleChanges+"}", text(i.Changes+" changes"),
					cond("#{==:"+role+","+RoleAgents+"}", text(i.Agents+" agents"),
						cond("#{==:"+role+","+RoleGit+"}", text(i.Branch+" git"),
							cond("#{==:"+role+","+RoleScratch+"}", text(i.Shell+" scratch"), text(i.Shell+" shell"))))))))
	state := "#{" + OptState + "}"
	stateText := cond("#{==:"+state+",waiting}", style("fg="+l.c(p.Waiting), "bold")+text(" "+i.Waiting+" needs you"),
		cond("#{==:"+state+",busy}", style("fg="+l.c(p.Busy))+text(" "+i.Busy+" working"),
			cond("#{==:"+state+",idle}", style("fg="+l.c(p.Idle))+text(" "+i.Idle+" idle"), "")))
	subagents := cond("#{&&:#{"+OptSubagents+"},#{!=:#{"+OptSubagents+"},0}}",
		style("fg="+l.c(p.Muted))+text(" +")+"#{"+OptSubagents+"}"+text(" subagents"), "")
	dead := style("fg="+l.c(p.Danger), "bold") + text(" "+i.Waiting+" exited") +
		cond("#{!=:#{pane_dead_status},}", text(" ")+"#{pane_dead_status}", "")
	status := cond("#{pane_dead}", dead, stateText+subagents)
	if l.Seps.Powerline() {
		// The active pane wears its label as a filled block, the way the bar
		// wears the workspace name, and hands the border back its own color.
		active := style("bg="+l.c(p.Accent), "fg="+l.c(p.Bg), "bold") + " " + label + " " +
			style("bg=default", "fg="+l.c(p.Accent)) + text(l.Seps.Right) + style("nobold")
		return cond("#{pane_active}", active, style("fg="+l.c(p.Muted), "nobold")+" "+label) +
			status + style("default") + " "
	}
	return cond("#{pane_active}", style("fg="+l.c(p.Accent), "bold"), style("fg="+l.c(p.Muted), "nobold")) +
		" " + label + status + style("default") + " "
}
