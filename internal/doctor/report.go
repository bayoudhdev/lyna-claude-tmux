package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
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

// WriteText writes the report as aligned plain text. Details and fixes come
// from command output, paths and environment variables, so every line passes
// through sanitize.Line: a crafted TERM_PROGRAM or tool version must not
// reach the terminal as an escape sequence.
func WriteText(w io.Writer, r Report) error {
	width := 0
	for _, res := range r.Results {
		width = max(width, utf8.RuneCountInString(sanitize.Line(res.Title)))
	}
	var lines []string
	for _, res := range r.Results {
		title := sanitize.Line(res.Title)
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(title))
		row := fmt.Sprintf("%-4s  %s%s", sanitize.Line(string(res.Status)), title, pad)
		indent := strings.Repeat(" ", 6+width)
		for i, line := range cleanLines(res.Detail) {
			if i == 0 {
				row += "  " + line
				continue
			}
			lines = append(lines, row)
			row = indent + "  " + line
		}
		lines = append(lines, row)
		if res.Fix != "" && res.Status != StatusOK && res.Status != StatusSkip {
			for i, line := range cleanLines(res.Fix) {
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
