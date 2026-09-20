package tmux

import "github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"

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
// The text is typed as one line of printable text and nothing else. A control
// sequence inside a paste is read by the program as keys, and one of them ends
// the bracketed paste, after which the rest would be typed a key at a time; a
// newline would submit what came before it. Line folds and strips all of them,
// and text with nothing left to type is refused.
//
// The target is a pane id and nothing else: a name or a pattern is resolved by
// tmux against whatever is running now, and text typed into the wrong pane is
// text typed at whatever that pane runs. An unidentified pane is refused.
func PasteLine(pane, text string) []Command {
	text = sanitize.Line(text)
	if !ValidPaneID(pane) || text == "" {
		return nil
	}
	return []Command{
		{"set-buffer", "-b", spawnBuffer, "--", text},
		{"paste-buffer", "-d", "-p", "-b", spawnBuffer, "-t", pane},
		{"send-keys", "-t", pane, "Enter"},
	}
}

// copyBuffer names the paste buffer text copied out of a view lands in. It is
// reused, so one copy replaces the one before it.
const copyBuffer = "lyna-tmux-copy"

// CopyText puts one line where the user can paste it: the paste buffer of the
// server, and the clipboard of the terminal, which tmux writes with the escape
// the terminal understands. The text is one line of printable text and nothing
// else, for the reasons PasteLine is, and text with nothing left to copy is
// refused.
func CopyText(text string) []Command {
	text = sanitize.Line(text)
	if text == "" {
		return nil
	}
	return []Command{{"set-buffer", "-w", "-b", copyBuffer, "--", text}}
}
