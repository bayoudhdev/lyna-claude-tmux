package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
)

// doctorFake is a machine for DoctorRun: programs found through the host's
// PATH lookup and the output of each command line.
type doctorFake struct {
	bins    map[string]string
	outputs map[string]string
	mu      sync.Mutex
	ran     []string
}

func (f *doctorFake) host(t *testing.T, env map[string]string) Host {
	t.Helper()
	return Host{
		Getenv: func(k string) string { return env[k] },
		Home:   env["HOME"],
		LookPath: func(name string) (string, error) {
			if p, ok := f.bins[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
	}
}

func (f *doctorFake) deps() doctor.Deps {
	return doctor.Deps{
		GOOS: "darwin",
		// The host's lookup and environment must replace these.
		LookPath: func(string) (string, error) { return "/sys/should-not-be-used", nil },
		Getenv:   func(string) string { return "sys-should-not-be-used" },
		ReadFile: func(string, int64) ([]byte, error) { return nil, fs.ErrNotExist },
		Exists:   func(string) bool { return false },
		Run: func(ctx context.Context, argv []string) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("command run without a deadline")
			}
			line := strings.Join(argv, " ")
			f.mu.Lock()
			f.ran = append(f.ran, line)
			f.mu.Unlock()
			if out, ok := f.outputs[line]; ok {
				return out, nil
			}
			return "", fmt.Errorf("unexpected command %q", line)
		},
	}
}

func doctorRow(t *testing.T, r doctor.Report, id string) doctor.Result {
	t.Helper()
	for _, res := range r.Results {
		if res.ID == id {
			return res
		}
	}
	t.Fatalf("no %s row in %+v", id, r.Results)
	return doctor.Result{}
}

func TestDoctorRun(t *testing.T) {
	cases := []struct {
		name   string
		config string
		noHome bool
		bins   map[string]string
		check  func(t *testing.T, r doctor.Report, f *doctorFake)
	}{
		{
			name: "defaults without a configuration file",
			bins: map[string]string{"claude": "/bin/claude", "nvim": "/bin/nvim"},
			check: func(t *testing.T, r doctor.Report, f *doctorFake) {
				if first := r.Results[0]; first.ID != doctorConfigID || first.Status != doctor.StatusOK || !strings.Contains(first.Detail, "using the defaults") {
					t.Fatalf("first row %+v", first)
				}
				if last := r.Results[len(r.Results)-1]; last.ID != doctorReviewID {
					t.Fatalf("last row %+v", last)
				}
				if c := doctorRow(t, r, "claude"); c.Status != doctor.StatusFail || !strings.Contains(c.Detail, "older than 2.1.257") {
					t.Fatalf("claude row does not apply the minimum release: %+v", c)
				}
				if c := doctorRow(t, r, "tmux"); c.Status != doctor.StatusFail || !strings.Contains(c.Detail, "not on PATH") {
					t.Fatalf("tmux row does not use the host lookup: %+v", c)
				}
				if c := doctorRow(t, r, doctorReviewID); c.Status != doctor.StatusWarn || c.Fix != "lyna-tmux review install" {
					t.Fatalf("review row %+v", c)
				}
				if !strings.Contains(strings.Join(f.ran, "\n"), "/bin/nvim --version") {
					t.Fatalf("review status did not probe Neovim through the doctor runner: %q", f.ran)
				}
			},
		},
		{
			name:   "configured isolation reaches the checks",
			config: "[sandbox]\nisolation = \"process\"\n",
			check: func(t *testing.T, r doctor.Report, _ *doctorFake) {
				if c := doctorRow(t, r, doctorConfigID); c.Status != doctor.StatusOK || !strings.HasSuffix(c.Detail, "config.toml") {
					t.Fatalf("config row %+v", c)
				}
				if c := doctorRow(t, r, "sandbox-runtime"); c.Status != doctor.StatusFail {
					t.Fatalf("sandbox runtime row %+v", c)
				}
			},
		},
		{
			name:   "an invalid configuration is a failed row, not a crash",
			config: "[ui]\ntheme = \"nope\"\n",
			check: func(t *testing.T, r doctor.Report, _ *doctorFake) {
				c := doctorRow(t, r, doctorConfigID)
				if c.Status != doctor.StatusFail || c.Fix != "lyna-tmux config edit" || !strings.Contains(c.Detail, "theme") || !r.Failed() {
					t.Fatalf("config row %+v", c)
				}
				doctorRow(t, r, "git")
			},
		},
		{
			name:   "review editor mode comes from the configuration",
			config: "[review]\neditor = \"user\"\n",
			bins:   map[string]string{"nvim": "/bin/nvim"},
			check: func(t *testing.T, r doctor.Report, _ *doctorFake) {
				if c := doctorRow(t, r, doctorReviewID); c.Status != doctor.StatusOK || !strings.Contains(c.Detail, "your own Neovim v0.11.2") {
					t.Fatalf("review row %+v", c)
				}
			},
		},
		{
			name:   "unknown home",
			noHome: true,
			check: func(t *testing.T, r doctor.Report, _ *doctorFake) {
				if c := doctorRow(t, r, doctorConfigID); c.Status != doctor.StatusFail || !strings.Contains(c.Fix, "LYNA_TMUX_HOME") {
					t.Fatalf("config row %+v", c)
				}
				if c := doctorRow(t, r, doctorReviewID); c.Status != doctor.StatusSkip {
					t.Fatalf("review row %+v", c)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			env := map[string]string{"HOME": root, "LYNA_TMUX_HOME": root}
			if tc.noHome {
				env = map[string]string{}
			}
			if tc.config != "" {
				mkdir(t, filepath.Join(root, "config"))
				if err := os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f := &doctorFake{bins: tc.bins, outputs: map[string]string{
				"/bin/claude --version": "2.1.100 (Claude Code)\n",
				"/bin/nvim --version":   "NVIM v0.11.2\nBuild type: Release\n",
			}}
			r := DoctorRun(t.Context(), f.host(t, env), f.deps(), root, ReviewPlugin{})
			if r.Summary.OK+r.Summary.Warn+r.Summary.Fail+r.Summary.Skip != len(r.Results) {
				t.Fatalf("summary %+v for %d rows", r.Summary, len(r.Results))
			}
			tc.check(t, r, f)
		})
	}
}

func TestDoctorReviewResult(t *testing.T) {
	nvim := domain.NvimVersion{Major: 0, Minor: 11, Patch: 2}
	oldNvim := domain.NvimVersion{Major: 0, Minor: 10, Patch: 0}
	good := review.Report{
		Installed: true, Commit: "0123456789abcdef0123", CommitOK: true, Version: "2.1.0", VersionOK: true,
		Assets:    []review.AssetStatus{{File: "libvscode_diff.dylib", Present: true, ChecksumOK: true}},
		NvimFound: true, NvimVersion: nvim, NvimSupported: true, NvimRecommends: true,
	}
	with := func(change func(r *review.Report)) review.Report {
		r := good
		r.Assets = append([]review.AssetStatus(nil), good.Assets...)
		change(&r)
		return r
	}
	cases := []struct {
		name       string
		editor     domain.EditorMode
		report     review.Report
		wantStatus doctor.Status
		wantFix    string
		detailHas  string
	}{
		{name: "verified", editor: domain.EditorIsolated, report: good, wantStatus: doctor.StatusOK, detailHas: "codediff.nvim 2.1.0 (commit 0123456789ab), its native file matches its checksum; Neovim v0.11.2"},
		{name: "verified with several native files", editor: domain.EditorIsolated, report: with(func(r *review.Report) {
			r.Assets = append(r.Assets, review.AssetStatus{File: "libvscode_diff.so", Present: true, ChecksumOK: true})
		}), wantStatus: doctor.StatusOK, detailHas: "2 native files match their checksums"},
		{name: "not installed", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.Installed, r.Assets = false, nil }), wantStatus: doctor.StatusWarn, wantFix: "lyna-tmux review install", detailHas: "not installed"},
		{name: "unsupported platform", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.PlatformProblem = "no build for plan9/amd64" }), wantStatus: doctor.StatusWarn, detailHas: "plan9"},
		{name: "not a directory", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.DirProblem = "is a symbolic link" }), wantStatus: doctor.StatusFail, wantFix: "lyna-tmux review install", detailHas: "symbolic link"},
		{name: "wrong commit", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.CommitOK = false }), wantStatus: doctor.StatusFail, wantFix: "lyna-tmux review install --force", detailHas: "not the pinned one"},
		{name: "library checksum", editor: domain.EditorIsolated, report: with(func(r *review.Report) {
			r.Assets[0].ChecksumOK, r.Assets[0].Problem = false, "libvscode_diff.dylib does not match its pinned SHA256"
		}), wantStatus: doctor.StatusFail, wantFix: "lyna-tmux review install --force", detailHas: "does not match"},
		{name: "unverified file", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.Unverified = []string{"plugin/evil.lua"} }), wantStatus: doctor.StatusFail, detailHas: "evil.lua", wantFix: "lyna-tmux review install --force"},
		{name: "missing nvim warns", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.NvimFound, r.NvimProblem = false, "nvim is not on PATH" }), wantStatus: doctor.StatusWarn, detailHas: "nvim is not on PATH"},
		{name: "old nvim warns", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.NvimVersion, r.NvimSupported = oldNvim, false }), wantStatus: doctor.StatusWarn, detailHas: "too old"},
		{name: "not the recommended nvim", editor: domain.EditorIsolated, report: with(func(r *review.Report) { r.NvimRecommends = false }), wantStatus: doctor.StatusWarn, detailHas: "recommended"},
		{name: "user editor ignores the plugin", editor: domain.EditorUser, report: with(func(r *review.Report) { r.Installed = false }), wantStatus: doctor.StatusOK, detailHas: "your own Neovim v0.11.2"},
		{name: "user editor without nvim", editor: domain.EditorUser, report: with(func(r *review.Report) { r.NvimFound, r.NvimProblem = false, "nvim is not on PATH" }), wantStatus: doctor.StatusWarn, detailHas: "nvim is not on PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := doctorReviewResult(tc.editor, tc.report)
			if got.ID != doctorReviewID || got.Status != tc.wantStatus || got.Fix != tc.wantFix || !strings.Contains(got.Detail, tc.detailHas) {
				t.Fatalf("got %+v, want status %s fix %q detail containing %q", got, tc.wantStatus, tc.wantFix, tc.detailHas)
			}
		})
	}
}
