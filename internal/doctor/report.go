package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/termx"
)

// Summary counts results by status.
type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Report is the serializable doctor output.
type Report struct {
	Results []Result `json:"results"`
	Summary Summary  `json:"summary"`
}

// NewReport wraps results with their summary.
func NewReport(results []Result) Report {
	r := Report{Results: results}
	if r.Results == nil {
		r.Results = []Result{}
	}
	for _, res := range results {
		switch res.Status {
		case StatusOK:
			r.Summary.OK++
		case StatusWarn:
			r.Summary.Warn++
		case StatusFail:
			r.Summary.Fail++
		case StatusSkip:
			r.Summary.Skip++
		}
	}
	return r
}

// Failed reports whether any check failed.
func (r Report) Failed() bool { return r.Summary.Fail > 0 }

// WriteJSON writes the report as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// minRoom is the narrowest text column worth wrapping into. Below it the
// terminal wraps the report itself, which at least loses no characters.
const minRoom = 24

// WriteText writes the report as aligned plain text. Details and fixes come
// from command output, paths and environment variables, so every line passes
// through sanitize.Line: a crafted TERM_PROGRAM or tool version must not
// reach the terminal as an escape sequence.
//
// termWidth is the width of the terminal in cells, or zero when it is not
// known. It wraps what a check says on word boundaries under its own column,
// where the terminal would otherwise break it mid-word. A fix written over
// several lines is left exactly as it is: it is a command to copy, not prose.
func WriteText(w io.Writer, r Report, termWidth int) error {
	titles := 0
	for _, res := range r.Results {
		titles = max(titles, utf8.RuneCountInString(sanitize.Line(res.Title)))
	}
	column := 6 + titles + 2
	room := 0
	if termWidth > 0 && termWidth-column >= minRoom {
		room = termWidth - column
	}
	var lines []string
	for _, res := range r.Results {
		title := sanitize.Line(res.Title)
		pad := strings.Repeat(" ", titles-utf8.RuneCountInString(title))
		row := fmt.Sprintf("%-4s  %s%s", sanitize.Line(string(res.Status)), title, pad)
		indent := strings.Repeat(" ", 6+titles)
		for i, line := range termx.WrapLines(cleanLines(res.Detail), room) {
			if i == 0 {
				row += "  " + line
				continue
			}
			lines = append(lines, row)
			row = indent + "  " + line
		}
		lines = append(lines, row)
		if res.Fix != "" && res.Status != StatusOK && res.Status != StatusSkip {
			fix := cleanLines(res.Fix)
			if len(fix) == 1 {
				fix = termx.Wrap(fix[0], room-len("fix:  "))
			}
			for i, line := range fix {
				label := "      "
				if i == 0 {
					label = "fix:  "
				}
				lines = append(lines, indent+"  "+label+line)
			}
		}
	}
	var b strings.Builder
	for _, line := range lines {
		// Padding and empty heredoc lines would otherwise leave trailing blanks.
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	s := r.Summary
	fmt.Fprintf(&b, "\n%d ok, %d warn, %d fail, %d skipped\n", s.OK, s.Warn, s.Fail, s.Skip)
	_, err := io.WriteString(w, b.String())
	return err
}

// cleanLines splits s into sanitized lines, keeping indentation so multi-line
// commands (heredocs) stay readable.
func cleanLines(s string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		out = append(out, strings.Repeat(" ", indent)+sanitize.Line(line))
	}
	return out
}
