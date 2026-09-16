package config

import _ "embed"

//go:embed template.toml
var template []byte

// Template returns the documented configuration file written by
// `lyna-tmux config init`. It decodes to Default().
func Template() []byte {
	out := make([]byte, len(template))
	copy(out, template)
	return out
}
