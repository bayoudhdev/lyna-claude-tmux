package termx

// MetaGuidance tells the user how to make Option (macOS) or Alt send Meta, which
// the lyna-tmux root key bindings (M-a, M-\ and friends) rely on.
type MetaGuidance struct {
	Program Program `json:"program"`
	// Needed is false where the key already sends Meta by default.
	Needed bool `json:"needed"`
	// Setting is the exact setting to change, with where it lives.
	Setting string `json:"setting"`
}

// Exact setting names, verified against each terminal's documentation:
// Terminal and iTerm2 menu paths, WezTerm send_composed_key_when_*_alt_is_pressed,
// Ghostty macos-option-as-alt, kitty macos_option_as_alt, Alacritty
// window.option_as_alt, VS Code terminal.integrated.macOptionIsMeta.
const (
	settingAppleTerminal = `Terminal > Settings > Profiles > Keyboard: enable "Use Option as Meta Key"`
	settingITerm2        = `iTerm2 > Settings > Profiles > Keys > General: set "Left Option key" and "Right Option key" to "Esc+"`
	settingWezTerm       = `wezterm.lua: config.send_composed_key_when_left_alt_is_pressed = false and config.send_composed_key_when_right_alt_is_pressed = false`
	settingGhostty       = `Ghostty config: macos-option-as-alt = true`
	settingKitty         = `kitty.conf: macos_option_as_alt yes`
	settingAlacritty     = `alacritty.toml: [window] option_as_alt = "Both"`
	settingVSCode        = `VS Code settings.json: "terminal.integrated.macOptionIsMeta": true`
	settingGeneric       = `enable the terminal's "Option as Meta" (or "Option as Alt") setting; the same actions stay available after the tmux prefix`
	settingDefault       = `Alt sends Meta by default on this platform`
)

// OptionAsMeta returns the guidance for program on goos. Outside macOS every
// supported terminal sends Alt as Meta already. On macOS, WezTerm treats the
// left Option key as Alt by default; the others insert composed characters
// until the setting is changed.
func OptionAsMeta(program Program, goos string) MetaGuidance {
	g := MetaGuidance{Program: program, Needed: goos == "darwin"}
	if !g.Needed {
		g.Setting = settingDefault
		return g
	}
	switch program {
	case ProgramAppleTerminal:
		g.Setting = settingAppleTerminal
	case ProgramITerm2:
		g.Setting = settingITerm2
	case ProgramWezTerm:
		g.Needed = false
		g.Setting = settingWezTerm
	case ProgramGhostty:
		g.Setting = settingGhostty
	case ProgramKitty:
		g.Setting = settingKitty
	case ProgramAlacritty:
		g.Setting = settingAlacritty
	case ProgramVSCode:
		g.Setting = settingVSCode
	default:
		g.Setting = settingGeneric
	}
	return g
}
