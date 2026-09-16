package tmux

import (
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
)

// Env is what generated commands need to know about the installation.
type Env struct {
	// Bin is the absolute path of the lyna-tmux binary that tmux runs for
	// popups, hooks and menu actions needing application logic.
	Bin string
	// ConfPath is the generated configuration file, reloaded by the reload action.
	ConfPath string
	// PopupWidth and PopupHeight size tool popups ("90%" or a cell count).
	PopupWidth, PopupHeight string
	// Bindings are the installed key bindings, listed by the menu action.
	Bindings []keys.Binding
}

// binCommand renders a lyna-tmux invocation for a shell started by tmux
// (display-popup, run-shell). tmux expands formats in these strings before
// the shell sees them, so the quoted command is also format-escaped.
func (e Env) binCommand(args ...string) string {
	return FormatEscape(ShellJoin(append([]string{e.Bin}, args...)...))
}

// currentPath is the directory of the pane a command runs for.
const currentPath = "#{pane_current_path}"

// projectPath is the session's project directory when set, else the current
// pane's directory.
const projectPath = "#{?#{" + OptProject + "}," + "#{" + OptProject + "},#{pane_current_path}}"

// claudePaneList expands to the pane ids of the Claude panes in the current
// window, each followed by a space.
const claudePaneList = "#{P:#{?#{==:#{" + OptRole + "}," + RoleClaude + "},#{pane_id} ,}}"

// SplitSeq splits the current pane and marks the new pane as a shell.
func SplitSeq(right bool) Seq {
	flag := "-v"
	if right {
		flag = "-h"
	}
	return Cmd("split-window", flag, "-c", currentPath).Then(Cmd("set-option", "-p", OptRole, RoleShell))
}

// PopupSeq opens a tool popup running a lyna-tmux subcommand in the current
// pane's directory.
func (e Env) PopupSeq(title string, width, height string, args ...string) Seq {
	return Cmd("display-popup", "-E", "-w", width, "-h", height, "-d", currentPath,
		"-T", " "+title+" ", e.binCommand(args...))
}

// AgentsSeq opens the agents picker.
func (e Env) AgentsSeq() Seq {
	return e.PopupSeq("agents", e.PopupWidth, e.PopupHeight, "agents", "--popup")
}

// ReviewSeq opens the review workspace for the current pane's directory.
func (e Env) ReviewSeq() Seq { return e.PopupSeq("review", "95%", "95%", "review", "--popup") }

// ChangesSeq opens the live changes view for the current pane's directory.
func (e Env) ChangesSeq() Seq {
	return e.PopupSeq("changes", e.PopupWidth, e.PopupHeight, "watch", "--popup")
}

// SandboxSeq opens the sandbox status of the current pane's workspace.
func (e Env) SandboxSeq() Seq {
	return e.PopupSeq("sandbox", e.PopupWidth, e.PopupHeight, "sandbox", "status", "--popup")
}

// ScratchSeq opens a throwaway shell popup in the current pane's directory.
func (e Env) ScratchSeq() Seq {
	return Cmd("display-popup", "-E", "-w", "80%", "-h", "70%", "-d", currentPath, "-T", " scratch ")
}

// FocusClaudeSeq selects the first Claude pane of the current window. tmux
// does not expand formats in -t targets, so the command is built by run-shell -C,
// which expands its argument and runs the result as a tmux command.
func FocusClaudeSeq() Seq {
	return Cmd("run-shell", "-C",
		"#{?"+claudePaneList+",select-pane -t #{s/ .*//:"+claudePaneList+"},display-message 'no Claude pane in this window'}")
}

// Registered commands. Menus and the status line click handler nest commands
// inside commands; quoting each level again grows the text exponentially, so
// nested commands are stored once as global user options and run through
// DoSeq, which needs one quoting level wherever it is used.
const (
	DoSplitRight  = "split_right"
	DoAgents      = "agents"
	DoReview      = "review"
	DoSandbox     = "sandbox"
	DoMenuKeys    = "menu_keys"
	DoMenuSession = "menu_session"
	DoMenuWindow  = "menu_window"
	DoMenuPane    = "menu_pane"
	DoMenuClaude  = "menu_claude"
)

// DoOption is the global user option holding a registered command.
func DoOption(name string) string { return "@lt_do_" + name }

// DoSeq runs a registered command. run-shell -C expands the option into the
// stored command text without expanding that text again, then parses and runs
// it with the caller's target and mouse event, so the stored commands expand
// their own format arguments exactly once.
func DoSeq(name string) Seq {
	return Cmd("run-shell", "-C", "#{"+DoOption(name)+"}")
}

// Registered is one registered command.
type Registered struct {
	Name string
	Seq  Seq
}

// Registry returns every registered command. Menus opened by mouse bindings
// target the pane or window under the mouse.
func (e Env) Registry() []Registered {
	return []Registered{
		{DoSplitRight, SplitSeq(true)},
		{DoAgents, e.AgentsSeq()},
		{DoReview, e.ReviewSeq()},
		{DoSandbox, e.SandboxSeq()},
		{DoMenuKeys, e.KeysMenu().Seq()},
		{DoMenuSession, withMouseTarget(e.SessionMenu().Seq())},
		{DoMenuWindow, withMouseTarget(e.WindowMenu().Seq())},
		{DoMenuPane, withMouseTarget(e.PaneMenu().Seq())},
		{DoMenuClaude, ClaudeMenu().Seq()},
	}
}

// ActionSeq maps a binding to its tmux commands.
func (e Env) ActionSeq(b keys.Binding) Seq {
	switch b.Action {
	case keys.ActionPanePrev:
		return Cmd("select-pane", "-t", ":.-")
	case keys.ActionPaneNext:
		return Cmd("select-pane", "-t", ":.+")
	case keys.ActionResizeLeft:
		return Cmd("resize-pane", "-L", "5")
	case keys.ActionResizeRight:
		return Cmd("resize-pane", "-R", "5")
	case keys.ActionResizeUp:
		return Cmd("resize-pane", "-U", "3")
	case keys.ActionResizeDown:
		return Cmd("resize-pane", "-D", "3")
	case keys.ActionSplitRight:
		return SplitSeq(true)
	case keys.ActionSplitDown:
		return SplitSeq(false)
	case keys.ActionZoom:
		return Cmd("resize-pane", "-Z")
	case keys.ActionClosePane:
		return Cmd("confirm-before", "-p", "Close pane #P? (y/n)", "kill-pane")
	case keys.ActionFocusClaude:
		return FocusClaudeSeq()
	case keys.ActionNewWindow:
		return Cmd("new-window", "-c", projectPath).Then(Cmd("set-option", "-p", OptRole, RoleShell))
	case keys.ActionWindow:
		return Cmd("select-window", "-t", ":"+b.Arg)
	case keys.ActionTree:
		return Cmd("choose-tree", "-Zs")
	case keys.ActionAgents:
		return e.AgentsSeq()
	case keys.ActionReview:
		return e.ReviewSeq()
	case keys.ActionScratch:
		return e.ScratchSeq()
	case keys.ActionMenu:
		return DoSeq(DoMenuKeys)
	case keys.ActionSendPrefix:
		return Cmd("send-prefix")
	case keys.ActionDetach:
		return Cmd("detach-client")
	case keys.ActionReload:
		return Cmd("source-file", GlobEscape(e.ConfPath)).Then(Cmd("display-message", "lyna-tmux: configuration reloaded"))
	}
	return Cmd("display-message", "lyna-tmux: unknown action "+DrawEscapeTime(string(b.Action)))
}
