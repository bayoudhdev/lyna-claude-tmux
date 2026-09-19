package claudecfg

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrPrompt reports a prompt claude would not read as a prompt.
var ErrPrompt = errors.New("invalid prompt")

// ValidatePrompt refuses prompts claude parses as something else: a leading
// '-' makes an option, and a single word may name a claude subcommand (such
// as update or install), which would run instead of a session.
//
// It is the rule for a prompt that becomes an argument of the launch. A prompt
// typed at a session that is already running is text and has no rule.
func ValidatePrompt(p string) error {
	switch {
	case p == "":
		return nil
	case strings.HasPrefix(p, "-"):
		return fmt.Errorf("%w: it must not start with '-', which claude reads as an option", ErrPrompt)
	case !strings.ContainsFunc(p, unicode.IsSpace):
		return fmt.Errorf("%w: %q is one word, which claude may run as a subcommand; write the prompt as a sentence", ErrPrompt, p)
	}
	return nil
}
