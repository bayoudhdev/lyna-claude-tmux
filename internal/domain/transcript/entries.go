package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Who says whose an entry of a transcript is.
type Who string

// The authors of an entry.
const (
	// User is a prompt: what the user, or the agent that started a subagent,
	// asked.
	User Who = "user"
	// Assistant is what the agent wrote back.
	Assistant Who = "assistant"
	// Tool is a tool the agent called, or what the call returned.
	Tool Who = "tool"
)

// Bounds of the text of an entry. A message can be as long as a file the
// agent wrote, and a reader shows what happened rather than all of it.
const (
	// MaxText bounds the text of a prompt or an answer, in bytes.
	MaxText = 16 << 10
	// MaxSummary bounds the one line a tool call or a tool result is drawn
	// with, in runes.
	MaxSummary = 160
	// MaxToolName bounds the name of a tool, in runes.
	MaxToolName = 64
)

// Entry is one thing that happened in a transcript, as a reader shows it.
type Entry struct {
	// At is when the line it came from was written; zero when the line says
	// nothing about it.
	At  time.Time
	Who Who
	// Tool is the tool of a call, and empty on the entry of what a call
	// returned, whose line names only the call it answers.
	Tool string
	// Text is what the entry says: a prompt or an answer as it was written,
	// lines and all, the input of a call or what it returned on one line.
	// Control and escape characters are gone from it.
	Text string
	// Failed marks a tool result Claude Code reported as an error.
	Failed bool
}

// Entries reads the entries of whole lines of a transcript. A line of a shape
// it does not know, a thinking block and a prompt Claude Code wrote itself
// rather than the user are skipped rather than failing the rest.
func Entries(data []byte) []Entry {
	var out []Entry
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		out = append(out, lineEntries(line)...)
	}
	return out
}

// Line types and content blocks the reader shows.
const (
	typeUser       = "user"
	typeAssistant  = "assistant"
	blockText      = "text"
	blockToolUse   = "tool_use"
	blockToolReply = "tool_result"
)

// entryLine is the part of a transcript line the reader reads.
type entryLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	// IsMeta marks a prompt Claude Code wrote into the conversation itself,
	// such as the caveat it puts before the output of a local command.
	IsMeta  bool `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one content block of a message.
type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

func lineEntries(line []byte) []Entry {
	var l entryLine
	if json.Unmarshal(line, &l) != nil || l.IsMeta {
		return nil
	}
	var who Who
	switch l.Type {
	case typeUser:
		who = User
	case typeAssistant:
		who = Assistant
	default:
		return nil
	}
	at := parseTime(l.Timestamp)
	// A prompt the user typed is a string; everything else is a list of
	// blocks.
	var text string
	if json.Unmarshal(l.Message.Content, &text) == nil {
		if e, ok := textEntry(at, who, text); ok {
			return []Entry{e}
		}
		return nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return nil
	}
	var out []Entry
	for _, raw := range blocks {
		// A block is decoded on its own, so one of a shape this release
		// does not know leaves the others of the message alone.
		var b block
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		switch {
		case b.Type == blockText:
			if e, ok := textEntry(at, who, b.Text); ok {
				out = append(out, e)
			}
		case b.Type == blockToolUse && who == Assistant:
			// A call is told from a result by the tool it names, so a call
			// that names none is a shape this release does not know.
			if name := cutRunes(sanitize.Line(b.Name), MaxToolName); name != "" {
				out = append(out, Entry{At: at, Who: Tool, Tool: name, Text: summarize(b.Input)})
			}
		case b.Type == blockToolReply && who == User:
			out = append(out, Entry{At: at, Who: Tool, Text: replyText(b.Content), Failed: b.IsError})
		}
	}
	return out
}

// textEntry is a prompt or an answer, unless there is nothing in it to show.
// The text is cut before it is sanitized, so an answer the size of a file is
// not sanitized whole to keep the start of it, and again after, since a tab
// grows into the spaces a terminal draws it with.
func textEntry(at time.Time, who Who, text string) (Entry, bool) {
	text = sanitize.Plain(cutBytes(text, MaxText))
	text = strings.TrimSpace(strings.ReplaceAll(text, "\t", "    "))
	if text == "" {
		return Entry{}, false
	}
	return Entry{At: at, Who: who, Text: cutBytes(text, MaxText)}, true
}

// summaryKeys are the inputs a tool call is summed up by, in the order they
// are looked for: the one thing a call acts on is what tells two calls of the
// same tool apart.
var summaryKeys = []string{
	"command", "file_path", "notebook_path", "pattern", "url", "query", "path", "description", "prompt",
}

// summarize is the one line a tool call is drawn with: the input it acts on
// when it has one of the known kinds, and the whole input otherwise.
func summarize(input json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) == nil {
		for _, k := range summaryKeys {
			var s string
			if json.Unmarshal(fields[k], &s) == nil && strings.TrimSpace(s) != "" {
				return oneLine(s)
			}
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, input) != nil {
		return ""
	}
	return oneLine(compact.String())
}

// replyText is the one line a tool result is drawn with: its text, however
// the result holds it, on one line.
func replyText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return oneLine(s)
	}
	var blocks []json.RawMessage
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, raw := range blocks {
		var b block
		if json.Unmarshal(raw, &b) == nil && b.Type == blockText {
			parts = append(parts, b.Text)
		}
	}
	return oneLine(strings.Join(parts, " "))
}

// oneLine is text sanitized onto one line and cut to MaxSummary runes.
func oneLine(s string) string {
	// Cut before sanitizing too, so a result the size of a file is not
	// sanitized whole to keep one line of it.
	s = cutBytes(s, MaxSummary*utf8.UTFMax*2)
	return cutRunes(strings.Join(strings.Fields(sanitize.Line(s)), " "), MaxSummary)
}

// ellipsis ends a text that was cut. It is plain dots, which draw in every
// terminal and every icon set.
const ellipsis = "..."

// cutRunes shortens s to n runes, the last three of them the ellipsis.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n-len(ellipsis) {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s[:i] + ellipsis
}

// cutBytes shortens s to at most n bytes without splitting a rune, the last
// three of them the ellipsis.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n - len(ellipsis)
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + ellipsis
}
