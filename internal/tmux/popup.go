package tmux

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// PopupSpec is a display-popup running one program on a client, closed when
// the program exits.
type PopupSpec struct {
	// Client is the name of the client the popup is shown on.
	Client string
	// Pane is the pane the popup is shown over, for a command that knows the
	// pane it runs in ($TMUX_PANE) and not the name of the client showing it.
	// One of Client and Pane is enough; tmux takes both.
	Pane string
	// Width and Height size the popup ("90%" or a cell count); empty keeps
	// tmux's default.
	Width, Height string
	// Dir is the absolute start directory; empty keeps tmux's default.
	Dir string
	// Title is drawn on the popup border.
	Title string
	// Argv is executed without a shell. It needs at least two elements:
	// display-popup hands a single argument to the default shell.
	Argv []string
}

// Command renders the display-popup command. The invocation that runs it
// returns only when the popup closes, with the program's exit status.
//
// tmux expands formats in -d and -T but not in the program arguments, which
// are executed as given.
func (p PopupSpec) Command() (Command, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	cmd := Command{"display-popup"}
	if p.Client != "" {
		cmd = append(cmd, "-c", p.Client)
	}
	if p.Pane != "" {
		cmd = append(cmd, "-t", p.Pane)
	}
	cmd = append(cmd, "-E")
	if p.Width != "" {
		cmd = append(cmd, "-w", p.Width)
	}
	if p.Height != "" {
		cmd = append(cmd, "-h", p.Height)
	}
	if p.Dir != "" {
		cmd = append(cmd, "-d", FormatEscape(p.Dir))
	}
	if p.Title != "" {
		cmd = append(cmd, "-T", " "+DrawEscape(p.Title)+" ")
	}
	return append(append(cmd, "--"), p.Argv...), nil
}

func (p PopupSpec) validate() error {
	if p.Client == "" && p.Pane == "" {
		return errors.New("popup: no client and no pane")
	}
	if p.Pane != "" && !ValidPaneID(p.Pane) {
		return fmt.Errorf("popup: pane %q is not a pane id such as %%3", p.Pane)
	}
	if len(p.Argv) < 2 || p.Argv[0] == "" {
		return fmt.Errorf("popup: program %q needs a path and at least one argument", p.Argv)
	}
	if p.Dir != "" && !filepath.IsAbs(p.Dir) {
		return fmt.Errorf("popup: directory %q is not absolute", p.Dir)
	}
	for _, v := range append([]string{p.Client, p.Pane, p.Width, p.Height, p.Dir, p.Title}, p.Argv...) {
		if strings.ContainsRune(v, 0) {
			return errors.New("popup: values must not contain NUL")
		}
	}
	return nil
}
