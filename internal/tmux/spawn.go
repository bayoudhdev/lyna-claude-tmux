package tmux

// spawnBuffer names the paste buffer a message goes through. It is reused, so
// a message replaces the one before it instead of piling up in the buffer
// stack, and the paste deletes it.
const spawnBuffer = "lyna-tmux-spawn"

// PasteLine types one line into the program running in a pane and submits it,
// the way the user typing at that pane would.
//
// The text goes through a paste buffer rather than through send-keys: a paste
// arrives as one piece of text, which an agent's own input reads as text
// whatever it holds, where a key at a time would be read as key chords. The
// '--' keeps a line that starts with a dash from being read as options by tmux
// itself, and the Enter is sent after the paste because a bracketed paste
// carries no submit.
//
// The target is a pane id and nothing else: a name or a pattern is resolved by
// tmux against whatever is running now, and text typed into the wrong pane is
// text typed at whatever that pane runs. An unidentified pane is refused.
func PasteLine(pane, text string) []Command {
	if !ValidPaneID(pane) {
		return nil
	}
	return []Command{
		{"set-buffer", "-b", spawnBuffer, "--", text},
		{"paste-buffer", "-d", "-p", "-b", spawnBuffer, "-t", pane},
		{"send-keys", "-t", pane, "Enter"},
	}
}
