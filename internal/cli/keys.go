package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
)

// keyEntry is one key in `keys --json`.
type keyEntry struct {
	Key    string `json:"key"`
	Human  string `json:"human"`
	Table  string `json:"table,omitempty"`
	Action string `json:"action,omitempty"`
	Arg    string `json:"arg,omitempty"`
	Group  string `json:"group,omitempty"`
	Help   string `json:"help"`
}

type keysReport struct {
	Prefix    string     `json:"prefix"`
	Workspace []keyEntry `json:"workspace"`
	Claude    []keyEntry `json:"claude"`
	Conflicts []string   `json:"conflicts"`
}

var keyGroups = []string{keys.GroupPanes, keys.GroupWindows, keys.GroupTools, keys.GroupSession}

func newKeysCmd(d Deps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Show the workspace key bindings and the keys left to Claude Code",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := d.Host()
			if err != nil {
				return err
			}
			_, cfg, err := app.LoadConfig(h)
			if err != nil {
				return err
			}
			bindings := keys.Defaults(keys.Options{AltKeys: cfg.UI.AltKeys, Prefix: cfg.Workspace.Prefix})
			report := keysReport{Prefix: cfg.Workspace.Prefix, Workspace: []keyEntry{}, Claude: []keyEntry{}, Conflicts: []string{}}
			for _, b := range bindings {
				report.Workspace = append(report.Workspace, keyEntry{
					Key: b.Key, Human: keys.Human(b.Key), Table: string(b.Table), Action: string(b.Action), Arg: b.Arg, Group: b.Group, Help: b.Help,
				})
			}
			for _, r := range keys.ClaudeReserved {
				report.Claude = append(report.Claude, keyEntry{Key: r.Key, Human: keys.Human(r.Key), Help: r.Claude})
			}
			for _, c := range keys.Conflicts(bindings) {
				report.Conflicts = append(report.Conflicts, c.String())
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			return writeKeys(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func writeKeys(out io.Writer, r keysReport) error {
	root := collapseWindows(r.Workspace, keys.TableRoot)
	prefix := collapseWindows(r.Workspace, keys.TablePrefix)
	width := 0
	for _, list := range [][]keyEntry{root, prefix, r.Claude} {
		for _, e := range list {
			width = max(width, len(e.Human))
		}
	}
	var b strings.Builder
	line := func(e keyEntry) { fmt.Fprintf(&b, "    %-*s   %s\n", width, e.Human, e.Help) }
	section := func(title string, entries []keyEntry) {
		if len(entries) == 0 {
			return
		}
		b.WriteString(title + "\n")
		for _, g := range keyGroups {
			first := true
			for _, e := range entries {
				if e.Group != g {
					continue
				}
				if first {
					b.WriteString("  " + strings.ToUpper(g) + "\n")
					first = false
				}
				line(e)
			}
		}
		b.WriteString("\n")
	}
	section("Workspace keys, no prefix (Alt is Option on macOS; enable Option as Meta in your terminal)", root)
	section("After the prefix "+keys.Human(r.Prefix), prefix)
	b.WriteString("Keys left to Claude Code\n")
	for _, e := range r.Claude {
		if r.Prefix != "" && keys.Normalize(e.Key) == keys.Normalize(r.Prefix) {
			e.Help += " (press " + keys.Human(r.Prefix) + " twice, the prefix takes it first)"
		}
		line(e)
	}
	if len(r.Conflicts) > 0 {
		b.WriteString("\nConflicts\n")
		for _, c := range r.Conflicts {
			b.WriteString("    " + c + "\n")
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// collapseWindows returns the entries of one table with the numbered window
// bindings folded into a single "1..9" line.
func collapseWindows(all []keyEntry, table keys.Table) []keyEntry {
	var out []keyEntry
	merged := -1
	var head keyEntry
	for _, e := range all {
		if e.Table != string(table) {
			continue
		}
		if e.Action != string(keys.ActionWindow) {
			out = append(out, e)
			continue
		}
		if merged < 0 {
			merged, head = len(out), e
			out = append(out, e)
			continue
		}
		out[merged].Human = head.Human + ".." + e.Arg
		out[merged].Help = "Window " + head.Arg + " to " + e.Arg
	}
	return out
}
