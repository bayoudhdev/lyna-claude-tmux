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
	Clock   bool
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
// wide, a space), the spaces around the name, and the one that follows. tmux
// truncates status-left at status-left-length, counting expanded columns, so
// the limit is this plus the longest name a workspace can have.
const StatusLeftFixed = 7

// StatusLeft is the brand block and session name.
func (l Look) StatusLeft() string {
	p := l.Palette
	return style("bg="+l.c(p.Accent), "fg="+l.c(p.Bg), "bold") + " " + text(l.Icons.Brand) + " " +
		style("bg="+l.c(p.Surface), "fg="+l.c(p.Text), "nobold") + " #S " +
		style("bg="+l.c(p.Bg), "fg="+l.c(p.Muted)) + " "
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
	return style("bg="+l.c(p.Surface), "fg="+l.c(p.Accent), "bold") + " " + l.agentDot() +
		style("fg="+l.c(p.Accent)) + "#I" + style("fg="+l.c(p.Muted)) + text(":") + style("fg="+l.c(p.Text)) + "#W" +
		cond("#{window_zoomed_flag}", style("fg="+l.c(p.Warning))+text(" [z]"), "") + " " + style("nobold")
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

// StatusRight is the button row, sandbox shield, branch and clock.
func (l Look) StatusRight() string {
	p, i := l.Palette, l.Icons
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
	b.WriteString(l.button(RangeAgents, agents))

	// Sandbox shield, only on workspaces lyna-tmux created.
	sandbox := "#{" + OptSandbox + "}"
	shield := cond("#{==:"+sandbox+",off}",
		style("bg="+l.c(p.Danger), "fg="+l.c(p.Bg), "bold")+text(" "+i.ShieldOff+" sandbox off ")+style("bg="+l.c(p.Bg), "nobold"),
		cond("#{==:"+sandbox+",strict}",
			style("fg="+l.c(p.Success))+text(" "+i.Shield+" strict "),
			style("fg="+l.c(p.Accent2))+text(" "+i.Shield+" std ")))
	shieldBlock := shield
	if l.Buttons {
		shieldBlock = "#[range=user|" + RangeSandbox + "]" + shield + "#[norange]"
	}
	b.WriteString(cond("#{"+OptManaged+"}", shieldBlock, ""))

	// Branch: hooks store it shortened and format-escaped (BranchOption), so
	// it is drawn literally and never truncated inside an escape pair.
	b.WriteString(cond("#{"+OptBranch+"}", style("fg="+l.c(p.Accent2))+text(" "+i.Branch+" ")+"#{"+OptBranch+"} ", ""))

	if l.Clock {
		b.WriteString(style("bg="+l.c(p.Surface), "fg="+l.c(p.Text)) + " ")
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
						cond("#{==:"+role+","+RoleScratch+"}", text(i.Shell+" scratch"), text(i.Shell+" shell")))))))
	state := "#{" + OptState + "}"
	stateText := cond("#{==:"+state+",waiting}", style("fg="+l.c(p.Waiting), "bold")+text(" "+i.Waiting+" needs you"),
		cond("#{==:"+state+",busy}", style("fg="+l.c(p.Busy))+text(" "+i.Busy+" working"),
			cond("#{==:"+state+",idle}", style("fg="+l.c(p.Idle))+text(" "+i.Idle+" idle"), "")))
	subagents := cond("#{&&:#{"+OptSubagents+"},#{!=:#{"+OptSubagents+"},0}}",
		style("fg="+l.c(p.Muted))+text(" +")+"#{"+OptSubagents+"}"+text(" subagents"), "")
	dead := style("fg="+l.c(p.Danger), "bold") + text(" "+i.Waiting+" exited") +
		cond("#{!=:#{pane_dead_status},}", text(" ")+"#{pane_dead_status}", "")
	status := cond("#{pane_dead}", dead, stateText+subagents)
	return cond("#{pane_active}", style("fg="+l.c(p.Accent), "bold"), style("fg="+l.c(p.Muted), "nobold")) +
		" " + label + status + style("default") + " "
}
