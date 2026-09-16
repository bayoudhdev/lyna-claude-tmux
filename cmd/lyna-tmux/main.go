// Command lyna-tmux turns tmux into a preconfigured Claude Code workspace.
package main

import (
	"context"
	"os"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:], cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}
