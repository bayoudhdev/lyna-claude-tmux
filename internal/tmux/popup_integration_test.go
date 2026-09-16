package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestIntegrationPopupSpec opens a popup on a real client and checks what
// the program receives: its directory and arguments verbatim, a literal
// title, and an invocation that waits until the popup closes.
func TestIntegrationPopupSpec(t *testing.T) {
	dir := hostileDir(t)
	root := filepath.Dir(dir)
	n := tmuxtest.StartNested(t, "/dev/null", root, 120, 30)
	ctx := tmuxtest.Context(t)
	client, err := n.Inner.Client.Run(ctx, "list-clients", "-F", "#{client_name}")
	if err != nil {
		t.Fatal(err)
	}
	client = strings.TrimSpace(client)
	marker := filepath.Join(root, "marker")
	arg := "#{session_name} $HOME ;"
	// The program waits on a channel and then exits with status 3, which
	// the invocation that opened the popup reports as its own.
	script := `pwd > "$1" && printf '%s\n' "$2" >> "$1" && echo popup-ready && "$3" -S "$4" wait-for lt-popup-close; exit 3`
	spec := tmux.PopupSpec{
		Client: client, Width: "60", Height: "12", Dir: dir, Title: "t#{session_name}",
		Argv: []string{"/bin/sh", "-c", script, "sh", marker, arg, n.Inner.Bin, tmuxtest.SocketPath(n.Inner.Name)},
	}
	cmd, err := spec.Command()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := n.Inner.Client.Batch(ctx, cmd)
		done <- err
	}()
	n.WaitScreen(t, "popup-ready", false)
	n.WaitScreen(t, " t#{session_name} ", false)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != dir+"\n"+arg+"\n" {
		t.Fatalf("popup program saw %q, want directory %q and argument %q", got, dir, arg)
	}
	select {
	case err := <-done:
		t.Fatalf("display-popup returned while the popup is open: %v", err)
	default:
	}
	if _, err := n.Inner.Client.Run(ctx, "wait-for", "-S", "lt-popup-close"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var te *tmux.Error
		if !errors.As(err, &te) || te.Code != 3 {
			t.Fatalf("display-popup after the program exited 3: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("display-popup did not return after the popup closed")
	}
	n.WaitScreen(t, "popup-ready", true)
}
