// Package termx describes the terminal lyna-tmux runs in: which program draws
// it, whether it renders 24-bit color, whether the session is already inside
// tmux, whether the locale is UTF-8, which clipboard tool is available and how
// to make the Option or Alt key send Meta.
//
// Detection reads only environment variables and PATH lookups passed in by the
// caller, so it never queries the terminal (a query would block or leak escape
// sequences into a pane) and every case is testable without a real terminal.
package termx

import (
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// Program identifies a terminal emulator. Known programs use their
// TERM_PROGRAM spelling; a program that does not set TERM_PROGRAM (kitty,
// Alacritty) uses its usual name.
type Program string

// Terminal programs with specific guidance.
const (
	ProgramUnknown       Program = ""
	ProgramAppleTerminal Program = "Apple_Terminal"
	ProgramITerm2        Program = "iTerm.app"
	ProgramWezTerm       Program = "WezTerm"
	ProgramGhostty       Program = "ghostty"
	ProgramKitty         Program = "kitty"
	ProgramAlacritty     Program = "Alacritty"
	ProgramVSCode        Program = "vscode"
)

// Name is a human label for the program.
func (p Program) Name() string {
	switch p {
	case ProgramUnknown:
		return "unknown terminal"
	case ProgramAppleTerminal:
		return "Terminal"
	case ProgramITerm2:
		return "iTerm2"
	case ProgramGhostty:
		return "Ghostty"
	case ProgramVSCode:
		return "VS Code terminal"
	default:
		return string(p)
	}
}

// Info is what the environment says about the terminal.
type Info struct {
	Program    Program `json:"program"`
	Term       string  `json:"term"`
	Truecolor  bool    `json:"truecolor"`
	InsideTmux bool    `json:"inside_tmux"`
	UTF8       bool    `json:"utf8"`
	WSL        bool    `json:"wsl"`
	SSH        bool    `json:"ssh"`
}

// knownPrograms maps TERM_PROGRAM values to programs. Multiplexers are absent
// on purpose: inside tmux TERM_PROGRAM names tmux, not the outer terminal.
var knownPrograms = map[string]Program{
	"Apple_Terminal": ProgramAppleTerminal,
	"iTerm.app":      ProgramITerm2,
	"WezTerm":        ProgramWezTerm,
	"ghostty":        ProgramGhostty,
	"vscode":         ProgramVSCode,
	"kitty":          ProgramKitty,
	"Alacritty":      ProgramAlacritty,
}

// multiplexers set TERM_PROGRAM to their own name for the programs they run.
var multiplexers = map[string]bool{"tmux": true, "screen": true, "zellij": true}

// markers are variables a terminal exports to its child processes. They
// survive inside tmux (the server inherits them), unlike TERM_PROGRAM, which
// tmux overwrites.
var markers = []struct {
	env     string
	program Program
}{
	{"KITTY_WINDOW_ID", ProgramKitty},
	{"ALACRITTY_WINDOW_ID", ProgramAlacritty},
	{"ALACRITTY_SOCKET", ProgramAlacritty},
	{"WEZTERM_PANE", ProgramWezTerm},
	{"WEZTERM_EXECUTABLE", ProgramWezTerm},
	{"GHOSTTY_RESOURCES_DIR", ProgramGhostty},
}

// termPrograms maps TERM values that only one program uses.
var termPrograms = map[string]Program{
	"xterm-kitty":   ProgramKitty,
	"alacritty":     ProgramAlacritty,
	"xterm-ghostty": ProgramGhostty,
	"wezterm":       ProgramWezTerm,
}

// truecolorPrograms render 24-bit color even when COLORTERM was lost, for
// example across ssh or sudo.
var truecolorPrograms = map[Program]bool{
	ProgramITerm2:    true,
	ProgramWezTerm:   true,
	ProgramGhostty:   true,
	ProgramKitty:     true,
	ProgramAlacritty: true,
	ProgramVSCode:    true,
}

// Detect reads the terminal description from the environment.
func Detect(getenv func(string) string) Info {
	term := getenv("TERM")
	info := Info{
		Program:    detectProgram(getenv, term),
		Term:       term,
		InsideTmux: getenv("TMUX") != "" || getenv("TERM_PROGRAM") == "tmux",
		UTF8:       theme.UTF8Locale(getenv),
		WSL:        getenv("WSL_DISTRO_NAME") != "" || getenv("WSL_INTEROP") != "",
		SSH:        getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "",
	}
	info.Truecolor = truecolor(getenv("COLORTERM"), term, info.Program)
	return info
}

func detectProgram(getenv func(string) string, term string) Program {
	tp := getenv("TERM_PROGRAM")
	if p, ok := knownPrograms[tp]; ok {
		return p
	}
	// iTerm2 exports LC_TERMINAL, which ssh forwards and tmux keeps.
	if getenv("LC_TERMINAL") == "iTerm2" {
		return ProgramITerm2
	}
	for _, m := range markers {
		if getenv(m.env) != "" {
			return m.program
		}
	}
	if p, ok := termPrograms[term]; ok {
		return p
	}
	if tp != "" && !multiplexers[tp] {
		return Program(tp)
	}
	return ProgramUnknown
}

func truecolor(colorterm, term string, p Program) bool {
	switch strings.ToLower(colorterm) {
	case "truecolor", "24bit":
		return true
	}
	if strings.HasSuffix(term, "-direct") {
		return true
	}
	return truecolorPrograms[p]
}
