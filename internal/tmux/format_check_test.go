package tmux

import (
	"fmt"
	"strings"
	"testing"
)

// checkFormat parses a generated format the way tmux splits it and reports
// structure that would expand wrongly: unclosed #{ or #[, conditionals and
// comparisons with the wrong number of branches (a stray ',' or '}' in drawn
// text shifts them), commas inside #[...] and unknown variables.
func checkFormat(s string) error { return checkText(s, false) }

// skipTo mirrors tmux's format_skip: it returns the index of the first byte of
// end found at bracket depth zero, honoring #-escapes, or -1.
func skipTo(s, end string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+1 < len(s) {
			if s[i+1] == '{' {
				depth++
			}
			if strings.IndexByte(",#{}:", s[i+1]) >= 0 {
				i++
				continue
			}
		}
		if s[i] == '}' {
			depth--
		}
		if strings.IndexByte(end, s[i]) >= 0 && depth == 0 {
			return i
		}
	}
	return -1
}

// splitTop splits s at commas outside any #{...}.
func splitTop(s string) []string {
	var parts []string
	for {
		i := skipTo(s, ",")
		if i < 0 {
			return append(parts, s)
		}
		parts = append(parts, s[:i])
		s = s[i+1:]
	}
}

func checkText(s string, nested bool) error {
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '#' && i+1 < len(s) && s[i+1] == '{':
			// As tmux does, the search starts at "#{" so the block's own
			// brace is counted.
			end := skipTo(s[i:], "}")
			if end < 0 {
				return fmt.Errorf("unclosed #{ at %d in %q", i, s)
			}
			if err := checkBlock(s[i+2 : i+end]); err != nil {
				return err
			}
			i += end
		case s[i] == '#' && i+1 < len(s) && s[i+1] == '[':
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				return fmt.Errorf("unclosed #[ at %d in %q", i, s)
			}
			if strings.Contains(s[i:i+end], ",") {
				return fmt.Errorf("comma inside style %q", s[i:i+end+1])
			}
			i += end
		case s[i] == '#' && i+1 < len(s) && strings.IndexByte(",#}:", s[i+1]) >= 0:
			i++
		case s[i] == '}' && nested:
			return fmt.Errorf("stray } in %q", s)
		}
	}
	return nil
}

func isVariableRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '@'
}

func checkParts(kind, body string, want int) error {
	parts := splitTop(body)
	if len(parts) != want {
		return fmt.Errorf("%s has %d parts, want %d: %q", kind, len(parts), want, body)
	}
	for _, p := range parts {
		if err := checkText(p, true); err != nil {
			return err
		}
	}
	return nil
}

func checkBlock(inner string) error {
	if strings.HasPrefix(inner, "?") {
		return checkParts("conditional", inner[1:], 3)
	}
	colon := skipTo(inner, ":")
	if colon < 0 {
		for _, r := range inner {
			if !isVariableRune(r) {
				return fmt.Errorf("bad variable %q", inner)
			}
		}
		if inner == "" {
			return fmt.Errorf("empty #{}")
		}
		return nil
	}
	mod, rest := inner[:colon], inner[colon+1:]
	switch {
	case mod == "==" || mod == "!=" || mod == "m" || mod == "&&" || mod == "||":
		return checkParts(mod, rest, 2)
	case mod == "P" || mod == "S" || mod == "W" || mod == "n" || mod == "q" || strings.HasPrefix(mod, "s/"):
		return checkParts(mod, rest, 1)
	}
	return fmt.Errorf("unknown modifier %q in %q", mod, inner)
}

func TestCheckFormat(t *testing.T) {
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"plain", "hello #S %H:%M", true},
		{"variable", "#{pane_id}", true},
		{"conditional", "#{?#{pane_active},a,b}", true},
		{"nested conditional", "#{?#{==:#{@lt_state},busy},#[fg=red]x,#{?#{m:*x*,#{P:#{?#{pane_active},x,}}},y,}}", true},
		{"escaped comma and hash", "#{?#{pane_active},a#,b ## c,d}", true},
		{"substitution", "#{s/ .*//:#{pane_id} }", true},
		{"count", "#{n:#{S:x}}", true},
		{"comma in style", "#[fg=red,bold]", false},
		{"comma in style inside conditional", "#{?#{pane_active},#[fg=red,bold]x,y}", false},
		{"comma in text inside conditional", "#{?#{pane_active},a,b,c}", false},
		{"missing branch", "#{?#{pane_active},a}", false},
		{"unclosed block", "#{pane_id", false},
		{"unclosed style", "#[fg=red", false},
		{"stray brace in branch", "#{?#{pane_active},a},b}", false},
		{"bad variable", "#{pane id}", false},
		{"empty block", "#{}", false},
		{"unknown modifier", "#{zz:x}", false},
		{"comparison arity", "#{==:a}", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFormat(tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("checkFormat(%q) = %v, want ok=%v", tc.in, err, tc.ok)
			}
		})
	}
}
