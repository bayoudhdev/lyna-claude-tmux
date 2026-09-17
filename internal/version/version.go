// Package version reports the build identity of the lyna-tmux binary.
//
// Version, Commit and Date are stamped by the release build through -ldflags.
// A binary built with plain `go build` or `go install` falls back to the module
// build info embedded by the Go toolchain.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Stamped at link time: -X github.com/bayoudhdev/lyna-claude-tmux/internal/version.Version=v1.0.0
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// Info is the resolved build identity.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go"`
	Platform  string `json:"platform"`
}

// Get resolves the build identity, preferring link-time values over build info.
func Get() Info {
	return resolve(Version, Commit, Date, readBuildInfo)
}

func readBuildInfo() (*debug.BuildInfo, bool) { return debug.ReadBuildInfo() }

func resolve(v, commit, date string, read func() (*debug.BuildInfo, bool)) Info {
	info := Info{
		Version:   v,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := read(); ok && bi != nil {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if len(info.Commit) > 12 {
		info.Commit = info.Commit[:12]
	}
	return info
}

// String renders a one-line identity, for example "lmux v1.0.0 (abc123 2026-09-15) darwin/arm64".
func (i Info) String() string {
	meta := i.Commit
	if i.Date != "" {
		if meta != "" {
			meta += " "
		}
		meta += i.Date
	}
	if meta == "" {
		return fmt.Sprintf("%s %s %s", xdg.Command, i.Version, i.Platform)
	}
	return fmt.Sprintf("%s %s (%s) %s", xdg.Command, i.Version, meta, i.Platform)
}
