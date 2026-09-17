package doctor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// checkCase runs one check against a fake machine and compares every field.
type checkCase struct {
	name string
	sys  fakeSystem
	edit func(*Deps)
	want []Result
}

func runCheckCases(t *testing.T, check func(context.Context, Deps) []Result, cases []checkCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.sys.deps()
			if tc.edit != nil {
				tc.edit(&d)
			}
			got := check(t.Context(), d.withDefaults())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"2.1.273 (Claude Code)", Version{2, 1, 273}, true},
		{"NVIM v0.11.4", Version{0, 11, 4}, true},
		{"NVIM v0.9", Version{0, 9, 0}, true},
		{"git version 2.50.1 (Apple Git-155)", Version{2, 50, 1}, true},
		{"27.3.1", Version{27, 3, 1}, true},
		{"v1.2.3-rc1", Version{1, 2, 3}, true},
		{"version 12", Version{}, false},
		{"", Version{}, false},
		{"1234567890.1.1", Version{234567890, 1, 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseVersion(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ParseVersion(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestVersionLessAndString(t *testing.T) {
	cases := []struct {
		name string
		a, b Version
		less bool
	}{
		{"major", Version{1, 9, 9}, Version{2, 0, 0}, true},
		{"minor", Version{2, 0, 9}, Version{2, 1, 0}, true},
		{"patch", Version{2, 1, 2}, Version{2, 1, 3}, true},
		{"equal", Version{2, 1, 3}, Version{2, 1, 3}, false},
		{"newer major", Version{3, 0, 0}, Version{2, 9, 9}, false},
		{"newer minor", Version{0, 10, 0}, Version{0, 9, 5}, false},
		{"newer patch", Version{0, 9, 5}, Version{0, 9, 4}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Less(tc.b); got != tc.less {
				t.Fatalf("%v.Less(%v) = %v, want %v", tc.a, tc.b, got, tc.less)
			}
		})
	}
	if got := (Version{2, 1, 273}).String(); got != "2.1.273" {
		t.Fatalf("String() = %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"\n\n  NVIM v0.11.4\nBuild type: Release\n", "NVIM v0.11.4"},
		{"one", "one"},
	}
	for _, tc := range cases {
		if got := firstLine(tc.in); got != tc.want {
			t.Fatalf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCheckTmux(t *testing.T) {
	bins := map[string]string{"tmux": "/usr/bin/tmux"}
	ubuntu := map[string]string{osReleasePath: osReleaseUbuntu}
	runCheckCases(t, checkTmux, []checkCase{
		{
			name: "missing on ubuntu",
			sys:  fakeSystem{goos: "linux", files: ubuntu},
			want: []Result{{ID: "tmux", Title: "tmux", Status: StatusFail, Detail: "tmux is not on PATH", Fix: "sudo apt-get install tmux"}},
		},
		{
			name: "missing on macOS",
			sys:  fakeSystem{goos: "darwin"},
			want: []Result{{ID: "tmux", Title: "tmux", Status: StatusFail, Detail: "tmux is not on PATH", Fix: "brew install tmux"}},
		},
		{
			name: "version command fails",
			sys: fakeSystem{goos: "linux", files: ubuntu, bins: bins, outputs: map[string]fakeOutput{
				"/usr/bin/tmux -V": {err: errors.New("exit status 1")},
			}},
			want: []Result{{ID: "tmux", Title: "tmux", Status: StatusFail, Detail: "/usr/bin/tmux -V failed: exit status 1", Fix: "sudo apt-get install tmux"}},
		},
		{
			name: "unparsable version",
			sys:  fakeSystem{goos: "linux", bins: bins, outputs: map[string]fakeOutput{"/usr/bin/tmux -V": {out: "tmux weird\n"}}},
			want: []Result{{ID: "tmux", Title: "tmux", Status: StatusWarn, Detail: `cannot read the tmux version from "tmux weird"`}},
		},
		{
			name: "too old",
			sys:  fakeSystem{goos: "linux", files: ubuntu, bins: bins, outputs: map[string]fakeOutput{"/usr/bin/tmux -V": {out: "tmux 3.2a\n"}}},
			want: []Result{{
				ID: "tmux", Title: "tmux", Status: StatusFail,
				Detail: "tmux 3.2a at /usr/bin/tmux is older than 3.3; if the distribution package is still older, build a release from https://github.com/tmux/tmux/releases",
				Fix:    "sudo apt-get install tmux",
			}},
		},
		{
			name: "minimum supported",
			sys:  fakeSystem{goos: "linux", bins: bins, outputs: map[string]fakeOutput{"/usr/bin/tmux -V": {out: "tmux 3.3a\n"}}},
			want: []Result{{ID: "tmux", Title: "tmux", Status: StatusOK, Detail: "tmux 3.3a at /usr/bin/tmux"}},
		},
		{
			name: "current release",
			sys:  fakeSystem{goos: "darwin", bins: map[string]string{"tmux": "/opt/homebrew/bin/tmux"}, outputs: map[string]fakeOutput{"/opt/homebrew/bin/tmux -V": {out: "tmux 3.7c\n"}}},
			want: []Result{{ID: "tmux", Title: "tmux", Status: StatusOK, Detail: "tmux 3.7c at /opt/homebrew/bin/tmux"}},
		},
	})
}

func TestCheckClaude(t *testing.T) {
	bins := map[string]string{"claude": "/home/u/.local/bin/claude"}
	version := map[string]fakeOutput{"/home/u/.local/bin/claude --version": {out: "2.1.273 (Claude Code)\n"}}
	runCheckCases(t, checkClaude, []checkCase{
		{
			name: "missing",
			sys:  fakeSystem{goos: "linux"},
			want: []Result{{ID: "claude", Title: "Claude Code", Status: StatusFail, Detail: "claude is not on PATH", Fix: "curl -fsSL https://claude.ai/install.sh | bash"}},
		},
		{
			name: "version command fails",
			sys:  fakeSystem{bins: bins, outputs: map[string]fakeOutput{"/home/u/.local/bin/claude --version": {err: errors.New("signal: killed")}}},
			want: []Result{{ID: "claude", Title: "Claude Code", Status: StatusFail, Detail: "/home/u/.local/bin/claude --version failed: signal: killed", Fix: "curl -fsSL https://claude.ai/install.sh | bash"}},
		},
		{
			name: "unparsable version",
			sys:  fakeSystem{bins: bins, outputs: map[string]fakeOutput{"/home/u/.local/bin/claude --version": {out: "unknown\n"}}},
			want: []Result{{ID: "claude", Title: "Claude Code", Status: StatusWarn, Detail: `cannot read the Claude Code version from "unknown"`}},
		},
		{
			name: "no minimum configured",
			sys:  fakeSystem{bins: bins, outputs: version},
			want: []Result{{ID: "claude", Title: "Claude Code", Status: StatusOK, Detail: "Claude Code 2.1.273 at /home/u/.local/bin/claude"}},
		},
		{
			name: "meets minimum",
			sys:  fakeSystem{bins: bins, outputs: version},
			edit: func(d *Deps) { d.ClaudeMinVersion = "2.1.273" },
			want: []Result{{ID: "claude", Title: "Claude Code", Status: StatusOK, Detail: "Claude Code 2.1.273 at /home/u/.local/bin/claude"}},
		},
		{
			name: "below minimum",
			sys:  fakeSystem{bins: bins, outputs: version},
			edit: func(d *Deps) { d.ClaudeMinVersion = "2.2.0" },
			want: []Result{{
				ID: "claude", Title: "Claude Code", Status: StatusFail,
				Detail: "Claude Code 2.1.273 at /home/u/.local/bin/claude is older than 2.2.0", Fix: "claude update",
				// An update doctor can run itself, unlike an install that
				// pipes a downloaded script into a shell.
				Action: CommandFix("Update Claude Code", "/home/u/.local/bin/claude", "update"),
			}},
		},
		{
			name: "unparsable minimum is ignored",
			sys:  fakeSystem{bins: bins, outputs: version},
			edit: func(d *Deps) { d.ClaudeMinVersion = "latest" },
			want: []Result{{ID: "claude", Title: "Claude Code", Status: StatusOK, Detail: "Claude Code 2.1.273 at /home/u/.local/bin/claude"}},
		},
	})
}

func TestCheckGit(t *testing.T) {
	bins := map[string]string{"git": "/usr/bin/git"}
	runCheckCases(t, checkGit, []checkCase{
		{
			name: "missing on fedora",
			sys:  fakeSystem{goos: "linux", files: map[string]string{osReleasePath: osReleaseFedora}},
			want: []Result{{ID: "git", Title: "git", Status: StatusFail, Detail: "git is not on PATH; the changes pane, review and worktrees need it", Fix: "sudo dnf install git"}},
		},
		{
			name: "version fails",
			sys:  fakeSystem{goos: "darwin", bins: bins, outputs: map[string]fakeOutput{"/usr/bin/git --version": {err: errors.New("xcrun: error: invalid active developer path")}}},
			want: []Result{{ID: "git", Title: "git", Status: StatusFail, Detail: "/usr/bin/git --version failed: xcrun: error: invalid active developer path", Fix: "brew install git"}},
		},
		{
			name: "found with version",
			sys:  fakeSystem{bins: bins, outputs: map[string]fakeOutput{"/usr/bin/git --version": {out: "git version 2.50.1 (Apple Git-155)\n"}}},
			want: []Result{{ID: "git", Title: "git", Status: StatusOK, Detail: "git 2.50.1 at /usr/bin/git"}},
		},
		{
			name: "found without a version",
			sys:  fakeSystem{bins: bins, outputs: map[string]fakeOutput{"/usr/bin/git --version": {out: "git\n"}}},
			want: []Result{{ID: "git", Title: "git", Status: StatusOK, Detail: "git at /usr/bin/git"}},
		},
	})
}

func TestCheckTruecolor(t *testing.T) {
	runCheckCases(t, checkTruecolor, []checkCase{
		{
			name: "truecolor",
			sys:  fakeSystem{env: map[string]string{"TERM_PROGRAM": "ghostty", "COLORTERM": "truecolor"}},
			want: []Result{{ID: "truecolor", Title: "Truecolor", Status: StatusOK, Detail: "24-bit color in Ghostty"}},
		},
		{
			name: "256 colors",
			sys:  fakeSystem{env: map[string]string{"TERM": "xterm-256color"}},
			want: []Result{{
				ID: "truecolor", Title: "Truecolor", Status: StatusWarn,
				Detail: "the terminal does not advertise 24-bit color, so lyna-tmux quantizes the theme to 256 colors",
				Fix:    `export COLORTERM=truecolor (only when the terminal renders 24-bit color), or set ui.color = "256" in config.toml`,
			}},
		},
	})
}

func TestCheckClipboard(t *testing.T) {
	runCheckCases(t, checkClipboard, []checkCase{
		{
			name: "pbcopy",
			sys:  fakeSystem{goos: "darwin", bins: map[string]string{"pbcopy": "/usr/bin/pbcopy"}},
			want: []Result{{ID: "clipboard", Title: "Clipboard", Status: StatusOK, Detail: "pbcopy at /usr/bin/pbcopy"}},
		},
		{
			name: "macOS without pbcopy on PATH",
			sys:  fakeSystem{goos: "darwin"},
			want: []Result{{
				ID: "clipboard", Title: "Clipboard", Status: StatusWarn,
				Detail: "no clipboard tool found (pbcopy), so copy mode cannot write the system clipboard",
				Fix:    `export PATH="/usr/bin:$PATH" (pbcopy ships with macOS in /usr/bin)`,
			}},
		},
		{
			name: "wsl without interop",
			sys:  fakeSystem{goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}},
			want: []Result{{
				ID: "clipboard", Title: "Clipboard", Status: StatusWarn,
				Detail: "no clipboard tool found (clip.exe, wl-copy, xclip, xsel), so copy mode cannot write the system clipboard",
				Fix:    "set [interop] enabled = true in /etc/wsl.conf, then run wsl --shutdown from PowerShell",
			}},
		},
		{
			name: "wayland on ubuntu",
			sys:  fakeSystem{goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, files: map[string]string{osReleasePath: osReleaseUbuntu}},
			want: []Result{{
				ID: "clipboard", Title: "Clipboard", Status: StatusWarn,
				Detail: "no clipboard tool found (wl-copy, xclip, xsel), so copy mode cannot write the system clipboard",
				Fix:    "sudo apt-get install wl-clipboard",
			}},
		},
		{
			name: "x11 on fedora",
			sys:  fakeSystem{goos: "linux", env: map[string]string{"DISPLAY": ":0"}, files: map[string]string{osReleasePath: osReleaseFedora}},
			want: []Result{{
				ID: "clipboard", Title: "Clipboard", Status: StatusWarn,
				Detail: "no clipboard tool found (xclip, xsel, wl-copy), so copy mode cannot write the system clipboard",
				Fix:    "sudo dnf install xclip",
			}},
		},
		{
			name: "xsel found",
			sys:  fakeSystem{goos: "linux", env: map[string]string{"DISPLAY": ":0"}, bins: map[string]string{"xsel": "/usr/bin/xsel"}},
			want: []Result{{ID: "clipboard", Title: "Clipboard", Status: StatusOK, Detail: "xsel at /usr/bin/xsel"}},
		},
	})
}

func TestCheckOptionAsMeta(t *testing.T) {
	runCheckCases(t, checkOptionAsMeta, []checkCase{
		{
			name: "alt keys disabled",
			sys:  fakeSystem{goos: "darwin"},
			edit: func(d *Deps) { d.AltKeys = false },
			want: []Result{{ID: "option-meta", Title: "Option as Meta", Status: StatusSkip, Detail: "Alt key bindings are disabled (ui.alt_keys = false)"}},
		},
		{
			name: "linux needs nothing",
			sys:  fakeSystem{goos: "linux", env: map[string]string{"TERM": "xterm-kitty"}},
			want: []Result{{ID: "option-meta", Title: "Option as Meta", Status: StatusOK, Detail: "Alt sends Meta by default on this platform"}},
		},
		{
			name: "iterm2 on macOS",
			sys:  fakeSystem{goos: "darwin", env: map[string]string{"TERM_PROGRAM": "iTerm.app"}},
			want: []Result{{
				ID: "option-meta", Title: "Option as Meta", Status: StatusWarn,
				Detail: "Alt key bindings need Option to send Meta in iTerm2; doctor cannot read the terminal's setting",
				Fix: "iTerm2 > Settings > Profiles > Keys > General: set \"Left Option key\" and \"Right Option key\" to \"Esc+\"" +
					"\nlyna-tmux keys lists the same actions after the prefix, which need no terminal setting",
			}},
		},
		{
			name: "wezterm on macOS",
			sys:  fakeSystem{goos: "darwin", env: map[string]string{"TERM_PROGRAM": "WezTerm"}},
			want: []Result{{
				ID: "option-meta", Title: "Option as Meta", Status: StatusOK,
				Detail: "wezterm.lua: config.send_composed_key_when_left_alt_is_pressed = false and config.send_composed_key_when_right_alt_is_pressed = false",
			}},
		},
	})
}

func TestCheckNeovim(t *testing.T) {
	bins := map[string]string{"nvim": "/usr/bin/nvim"}
	ubuntu := map[string]string{osReleasePath: osReleaseUbuntu}
	runCheckCases(t, checkNeovim, []checkCase{
		{
			name: "missing",
			sys:  fakeSystem{goos: "linux", files: ubuntu},
			want: []Result{{ID: "nvim", Title: "Neovim", Status: StatusWarn, Detail: "nvim is not on PATH; the review popup needs Neovim 0.9 or newer", Fix: "sudo apt-get install neovim"}},
		},
		{
			name: "version fails",
			sys:  fakeSystem{goos: "darwin", bins: bins, outputs: map[string]fakeOutput{"/usr/bin/nvim --version": {err: errors.New("exit status 2")}}},
			want: []Result{{ID: "nvim", Title: "Neovim", Status: StatusWarn, Detail: "/usr/bin/nvim --version failed: exit status 2", Fix: "brew install neovim"}},
		},
		{
			name: "unparsable",
			sys:  fakeSystem{bins: bins, outputs: map[string]fakeOutput{"/usr/bin/nvim --version": {out: "NVIM dev\n"}}},
			want: []Result{{ID: "nvim", Title: "Neovim", Status: StatusWarn, Detail: `cannot read the Neovim version from "NVIM dev"`}},
		},
		{
			name: "too old",
			sys:  fakeSystem{goos: "linux", files: ubuntu, bins: bins, outputs: map[string]fakeOutput{"/usr/bin/nvim --version": {out: "NVIM v0.7.2\nBuild type: Release\n"}}},
			want: []Result{{ID: "nvim", Title: "Neovim", Status: StatusWarn, Detail: "Neovim 0.7.2 at /usr/bin/nvim is older than 0.9; the review popup needs 0.9 or newer", Fix: "sudo apt-get install neovim"}},
		},
		{
			name: "minimum",
			sys:  fakeSystem{bins: bins, outputs: map[string]fakeOutput{"/usr/bin/nvim --version": {out: "NVIM v0.9.5\nBuild type: Release\nLuaJIT 2.1.1692716794\n"}}},
			want: []Result{{ID: "nvim", Title: "Neovim", Status: StatusOK, Detail: "Neovim 0.9.5 at /usr/bin/nvim"}},
		},
	})
}

func TestCheckDocker(t *testing.T) {
	bins := map[string]string{"docker": "/usr/bin/docker"}
	info := "/usr/bin/docker info --format {{.ServerVersion}}"
	ubuntu := map[string]string{osReleasePath: osReleaseUbuntu}
	container := func(d *Deps) { d.Isolation = "container" }
	runCheckCases(t, checkDocker, []checkCase{
		{
			name: "missing and not needed",
			sys:  fakeSystem{goos: "linux"},
			want: []Result{{ID: "docker", Title: "Docker", Status: StatusSkip, Detail: `docker is not on PATH; only sandbox.isolation = "container" needs it`}},
		},
		{
			name: "missing and required on macOS",
			sys:  fakeSystem{goos: "darwin"},
			edit: container,
			want: []Result{{
				ID: "docker", Title: "Docker", Status: StatusFail,
				Detail: `docker is not on PATH and sandbox.isolation = "container" needs it`,
				Fix:    "brew install colima docker && colima start",
			}},
		},
		{
			name: "missing and required on ubuntu",
			sys:  fakeSystem{goos: "linux", files: ubuntu},
			edit: container,
			want: []Result{{
				ID: "docker", Title: "Docker", Status: StatusFail,
				Detail: `docker is not on PATH and sandbox.isolation = "container" needs it`,
				Fix:    "sudo apt-get install docker.io",
			}},
		},
		{
			name: "daemon down on linux, not required",
			sys:  fakeSystem{goos: "linux", bins: bins, outputs: map[string]fakeOutput{info: {err: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock")}}},
			want: []Result{{
				ID: "docker", Title: "Docker", Status: StatusWarn,
				Detail: "the Docker daemon is not reachable: Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
				Fix:    "sudo systemctl start docker",
			}},
		},
		{
			name: "daemon down on macOS, required",
			sys:  fakeSystem{goos: "darwin", bins: bins, outputs: map[string]fakeOutput{info: {err: errors.New("Cannot connect to the Docker daemon")}}},
			edit: container,
			want: []Result{{
				ID: "docker", Title: "Docker", Status: StatusFail,
				Detail: "the Docker daemon is not reachable: Cannot connect to the Docker daemon",
				Fix:    "colima start (or open -a Docker)",
			}},
		},
		{
			name: "socket permission denied",
			sys: fakeSystem{goos: "linux", bins: bins, outputs: map[string]fakeOutput{info: {
				err: errors.New("permission denied while trying to connect to the Docker daemon socket"),
			}}},
			want: []Result{{
				ID: "docker", Title: "Docker", Status: StatusWarn,
				Detail: "the Docker daemon is not reachable: permission denied while trying to connect to the Docker daemon socket",
				Fix:    `sudo usermod -aG docker "$USER" (then log out and back in)`,
			}},
		},
		{
			name: "reachable",
			sys:  fakeSystem{goos: "linux", bins: bins, outputs: map[string]fakeOutput{info: {out: "27.3.1\n"}}},
			edit: container,
			want: []Result{{ID: "docker", Title: "Docker", Status: StatusOK, Detail: "Docker server 27.3.1, client at /usr/bin/docker"}},
		},
		{
			name: "reachable without a version",
			sys:  fakeSystem{goos: "linux", bins: bins, outputs: map[string]fakeOutput{info: {out: "\n"}}},
			want: []Result{{ID: "docker", Title: "Docker", Status: StatusOK, Detail: "Docker daemon reachable, client at /usr/bin/docker"}},
		},
	})
}
