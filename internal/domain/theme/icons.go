package theme

import (
	"fmt"
	"strings"
)

// Icons are the glyphs drawn in status lines, borders and pickers. Every set
// renders each glyph in one terminal cell so layouts line up in any font.
type Icons struct {
	Name      string
	Brand     string // leftmost status block
	Busy      string // agent working
	Waiting   string // agent needs the user
	Idle      string // agent finished
	Unknown   string // no signal yet
	Branch    string // git branch
	Shield    string // sandbox on
	ShieldOff string // sandbox off
	Split     string // split button
	Agents    string // agents button
	Review    string // review button
	Menu      string // menu button
	Clock     string // clock
	Sep       string // separator between status segments
	Claude    string // Claude pane border label
	Shell     string // shell pane border label
	Changes   string // changes pane border label
}

var iconSets = map[string]Icons{
	"unicode": {
		Name: "unicode", Brand: "λ", Busy: "●", Waiting: "◆", Idle: "○", Unknown: "·",
		Branch: "⎇", Shield: "▣", ShieldOff: "▢", Split: "⊞", Agents: "◎", Review: "±",
		Menu: "≡", Clock: "◷", Sep: "│", Claude: "✻", Shell: "›", Changes: "±",
	},
	// nerd requires a patched font; it is never chosen automatically.
	"nerd": {
		Name: "nerd", Brand: "\U000f0626", Busy: "\U000f0765", Waiting: "\uf071", Idle: "\uf00c", Unknown: "\uf128",
		Branch: "\ue725", Shield: "\U000f0565", ShieldOff: "\U000f099e", Split: "\ueb56", Agents: "\U000f06a9", Review: "\uf440",
		Menu: "\uf0c9", Clock: "\uf017", Sep: "\ue621", Claude: "\U000f06a9", Shell: "\uf120", Changes: "\uf440",
	},
	"ascii": {
		Name: "ascii", Brand: "L", Busy: "*", Waiting: "!", Idle: "o", Unknown: "?",
		Branch: "@", Shield: "#", ShieldOff: "-", Split: "+", Agents: "A", Review: "~",
		Menu: "=", Clock: "", Sep: "|", Claude: ">", Shell: "$", Changes: "~",
	},
}

// GetIcons returns an icon set by name: unicode, nerd or ascii.
func GetIcons(name string) (Icons, error) {
	s, ok := iconSets[name]
	if !ok {
		return Icons{}, fmt.Errorf("theme: unknown icon set %q (want unicode, nerd or ascii)", name)
	}
	return s, nil
}

// ResolveIcons maps the icons setting to a concrete set. "auto" picks unicode
// when the locale is UTF-8 and not one of the East Asian languages, and ascii
// otherwise; nerd is only used when asked for, because a missing patched font
// draws boxes.
func ResolveIcons(setting string, getenv func(string) string) string {
	if setting != "" && setting != "auto" {
		return setting
	}
	if UTF8Locale(getenv) && !AmbiguousWideLocale(getenv) {
		return "unicode"
	}
	return "ascii"
}

// UTF8Locale reports whether the effective locale (LC_ALL, then LC_CTYPE,
// then LANG, as the C library resolves it) uses UTF-8.
func UTF8Locale(getenv func(string) string) bool {
	v := Locale(getenv)
	return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
}

// AmbiguousWideLocale reports whether the effective locale is one of the East
// Asian languages, where terminals draw the East Asian Ambiguous characters
// (the geometric shapes and arrows of the unicode set) two cells wide. Those
// glyphs would push a status bar, a border and every box one cell out of line
// per glyph, so the automatic choice avoids them; asking for unicode still
// gets it.
func AmbiguousWideLocale(getenv func(string) string) bool {
	v := Locale(getenv)
	for _, lang := range []string{"ja", "zh", "ko"} {
		if v == lang || strings.HasPrefix(v, lang+"_") || strings.HasPrefix(v, lang+"-") {
			return true
		}
	}
	return false
}

// Locale is the effective locale in lower case, empty when none is set.
func Locale(getenv func(string) string) string {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}
