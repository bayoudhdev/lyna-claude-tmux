package termx

// ShiftEnterGuidance tells the user whether Shift+Enter inserts a newline in
// the agent's prompt instead of sending it, and what to do when it does not.
//
// Nothing here is read from the terminal: a key sequence can only be observed
// while a key is pressed, and doctor never types into the user's terminal. The
// guidance is what the terminal is documented to do, so the report says what
// to try, never what happened.
type ShiftEnterGuidance struct {
	Program Program `json:"program"`
	// Works is true when the terminal sends Shift+Enter as its own key with
	// the settings lyna-tmux applies, so there is nothing to set up.
	Works bool `json:"works"`
	// Setting is the exact step for this terminal, or what the terminal does
	// when there is no step to take.
	Setting string `json:"setting"`
}

// Exact steps, verified against each terminal's documentation and the Claude
// Code terminal setup guidance. /terminal-setup writes the key binding the
// terminal needs; it reads the terminal it is started in, so it has to run in
// the host terminal, before a workspace, not in a pane.
const (
	shiftEnterSetup = "run /terminal-setup in Claude Code, in this terminal and not inside tmux, then restart the terminal"
	// The protocol is the kitty keyboard protocol, which reports Shift+Enter
	// as a key of its own. tmux forwards it because the generated
	// configuration sets extended-keys on and terminal-features *:extkeys.
	shiftEnterProtocol = "the terminal reports Shift+Enter as its own key and tmux passes it through"
	shiftEnterNone     = "this terminal sends Shift+Enter as a plain Enter and has no setting for it; use Option+Enter, which needs Option sent as Meta, or paste text that already has newlines"
	shiftEnterUnknown  = "run /terminal-setup in Claude Code, in this terminal and not inside tmux; a terminal that already sends the key is left as it is"
)

// ShiftEnter returns the guidance for program. The terminals that implement
// the kitty keyboard protocol need nothing; iTerm2 and the VS Code terminal
// need one key binding, which Claude Code writes itself; Terminal has no way
// to send the key at all.
func ShiftEnter(program Program) ShiftEnterGuidance {
	g := ShiftEnterGuidance{Program: program}
	switch program {
	case ProgramGhostty, ProgramKitty, ProgramWezTerm, ProgramAlacritty:
		g.Works, g.Setting = true, shiftEnterProtocol
	case ProgramITerm2, ProgramVSCode:
		g.Setting = shiftEnterSetup
	case ProgramAppleTerminal:
		g.Setting = shiftEnterNone
	default:
		g.Setting = shiftEnterUnknown
	}
	return g
}
