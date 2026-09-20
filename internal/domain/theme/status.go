package theme

import "fmt"

// Status bar styles: how one segment of the bar is joined to the next.
const (
	// StatusAuto picks powerline when the icon set in use is a patched font,
	// and plain otherwise.
	StatusAuto = "auto"
	// StatusPowerline joins segments with pointed separators; they are drawn
	// from the private use area and need a patched font.
	StatusPowerline = "powerline"
	// StatusPlain joins segments by their background alone.
	StatusPlain = "plain"
)

// Seps are the glyphs that join the segments of the status bar and label a
// pane border. Every field is empty in the plain style, where a segment ends
// where its background does.
type Seps struct {
	Name string
	// Right ends a segment of the left side of the bar, pointing into the
	// background that follows.
	Right string
	// Left ends a segment of the right side, pointing into the background
	// before it.
	Left string
	// Thin separates two segments that share a background.
	Thin string
}

// Powerline reports whether the separators are drawn at all.
func (s Seps) Powerline() bool { return s.Right != "" }

var sepSets = map[string]Seps{
	StatusPowerline: {Name: StatusPowerline, Right: "", Left: "", Thin: ""},
	StatusPlain:     {Name: StatusPlain},
}

// GetSeps returns the separators of a resolved status bar style: powerline or
// plain.
func GetSeps(style string) (Seps, error) {
	s, ok := sepSets[style]
	if !ok {
		return Seps{}, fmt.Errorf("theme: unknown status bar style %q (want powerline or plain)", style)
	}
	return s, nil
}

// ResolveStatusStyle maps the status bar setting to a concrete style. "auto"
// takes the pointed separators only with the nerd icon set, which is the one
// that already asks for a patched font; every other icon set would draw a box
// where a separator should be.
func ResolveStatusStyle(setting string, icons Icons) string {
	if setting != "" && setting != StatusAuto {
		return setting
	}
	if icons.Name == "nerd" {
		return StatusPowerline
	}
	return StatusPlain
}
