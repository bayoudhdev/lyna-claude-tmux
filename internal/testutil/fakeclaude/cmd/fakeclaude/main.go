// Command fakeclaude is the test stand-in for the claude executable. See
// package internal/testutil/fakeclaude for its behavior.
package main

import (
	"os"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
)

func main() {
	p, stop := fakeclaude.OSProcess()
	code := fakeclaude.Main(p)
	stop()
	os.Exit(code)
}
