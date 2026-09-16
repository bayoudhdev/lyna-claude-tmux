package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestManPageDate(t *testing.T) {
	now := func() time.Time { return time.Date(2031, 1, 2, 23, 0, 0, 0, time.FixedZone("x", -5*3600)) }
	env := func(epoch string) func(string) string {
		return func(k string) string {
			if k == "SOURCE_DATE_EPOCH" {
				return epoch
			}
			return ""
		}
	}
	cases := []struct {
		name, build, epoch, want string
	}{
		{name: "release commit date", build: "2026-09-15T23:30:00-02:00", epoch: "0", want: "2026-09-16"},
		{name: "plain day", build: "2026-09-15", want: "2026-09-15"},
		{name: "source date epoch", build: "", epoch: "1767225600", want: "2026-01-01"},
		{name: "unparsable build date falls through", build: "yesterday", epoch: "1767225600", want: "2026-01-01"},
		{name: "invalid epoch uses now in UTC", epoch: "soon", want: "2031-01-03"},
		{name: "nothing set uses now", want: "2031-01-03"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := manPageDate(tc.build, env(tc.epoch), now).Format(time.DateOnly); got != tc.want {
				t.Fatalf("manPageDate = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestManCommand(t *testing.T) {
	cases := []struct {
		name, build string
		wantTitle   string
	}{
		{name: "commit date stamped", build: "2026-09-15T10:00:00Z", wantTitle: `.TH LYNA-TMUX 1 "2026-09-15" "lyna-tmux"`},
		{name: "same date on every run", build: "2027-03-04", wantTitle: `.TH LYNA-TMUX 1 "2027-03-04" "lyna-tmux"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := &cobra.Command{Use: "lyna-tmux", Short: "Claude Code workspaces"}
			root.AddCommand(&cobra.Command{Use: "create", Short: "Open a workspace", Run: func(*cobra.Command, []string) {}})
			root.AddCommand(newManCmd(tc.build, func(string) string { return "" }, time.Now))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs([]string{"man"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(out.String(), "\n")
			if !strings.HasPrefix(first, tc.wantTitle) {
				t.Fatalf("title line %q, want prefix %q", first, tc.wantTitle)
			}
			if !strings.Contains(out.String(), "create") {
				t.Fatalf("page misses the subcommands:\n%s", out.String())
			}
		})
	}
}
