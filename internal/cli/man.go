package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	mango "github.com/muesli/mango-cobra"
	"github.com/muesli/roff"
	"github.com/spf13/cobra"
)

// manDate matches the date field of the page's .TH title line.
var manDate = regexp.MustCompile(`\A(\.TH \S+ \d+ )"\d{4}-\d{2}-\d{2}"`)

// newManCmd prints the roff man page that release packaging installs. The
// generator stamps the current day into the title line; the page carries the
// build's commit date instead, so archives built from one commit are identical.
func newManCmd(buildDate string, getenv func(string) string, now func() time.Time) *cobra.Command {
	return &cobra.Command{
		Use:                   "man",
		Short:                 "Print the man page",
		Hidden:                true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			page, err := mango.NewManPage(1, cmd.Root())
			if err != nil {
				return err
			}
			day := manPageDate(buildDate, getenv, now).Format(time.DateOnly)
			out := manDate.ReplaceAllString(page.Build(roff.NewDocument()), `${1}"`+day+`"`)
			_, err = fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		},
	}
}

// manPageDate is the build date (RFC 3339 from release builds and version
// control, or a plain day), then SOURCE_DATE_EPOCH, then now, in UTC.
func manPageDate(buildDate string, getenv func(string) string, now func() time.Time) time.Time {
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, buildDate); err == nil {
			return t.UTC()
		}
	}
	if sec, err := strconv.ParseInt(getenv("SOURCE_DATE_EPOCH"), 10, 64); err == nil {
		return time.Unix(sec, 0).UTC()
	}
	return now().UTC()
}
