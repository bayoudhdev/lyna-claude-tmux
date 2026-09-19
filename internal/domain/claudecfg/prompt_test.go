package claudecfg

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePrompt(t *testing.T) {
	cases := []struct {
		name    string
		prompt  string
		wantErr string
	}{
		{name: "no prompt", prompt: ""},
		{name: "sentence", prompt: "fix the login loop"},
		{name: "leading space keeps a word from naming a command", prompt: " update"},
		{name: "tab separated", prompt: "fix\tit"},
		{name: "option", prompt: "-v now", wantErr: "must not start with '-'"},
		{name: "long option", prompt: "--settings x", wantErr: "must not start with '-'"},
		{name: "subcommand word", prompt: "update", wantErr: "one word"},
		{name: "any single word", prompt: "refactor", wantErr: "one word"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePrompt(tc.prompt)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePrompt(%q) = %v", tc.prompt, err)
				}
				return
			}
			if !errors.Is(err, ErrPrompt) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidatePrompt(%q) = %v, want %q", tc.prompt, err, tc.wantErr)
			}
		})
	}
}
