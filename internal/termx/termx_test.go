package termx

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Info
	}{
		{
			name: "empty environment",
			env:  map[string]string{},
			want: Info{},
		},
		{
			name: "apple terminal without truecolor",
			env:  map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color", "LANG": "en_US.UTF-8"},
			want: Info{Program: ProgramAppleTerminal, Term: "xterm-256color", UTF8: true},
		},
		{
			name: "apple terminal reporting truecolor",
			env:  map[string]string{"TERM_PROGRAM": "Apple_Terminal", "COLORTERM": "truecolor"},
			want: Info{Program: ProgramAppleTerminal, Truecolor: true},
		},
		{
			name: "iterm2 by TERM_PROGRAM",
			env:  map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-256color"},
			want: Info{Program: ProgramITerm2, Term: "xterm-256color", Truecolor: true},
		},
		{
			name: "iterm2 inside tmux through LC_TERMINAL",
			env:  map[string]string{"TERM_PROGRAM": "tmux", "LC_TERMINAL": "iTerm2", "TERM": "tmux-256color", "TMUX": "/tmp/tmux-501/default,1,0"},
			want: Info{Program: ProgramITerm2, Term: "tmux-256color", Truecolor: true, InsideTmux: true},
		},
		{
			name: "tmux TERM_PROGRAM alone marks inside tmux with an unknown outer terminal",
			env:  map[string]string{"TERM_PROGRAM": "tmux", "TERM": "tmux-256color"},
			want: Info{Term: "tmux-256color", InsideTmux: true},
		},
		{
			name: "wezterm",
			env:  map[string]string{"TERM_PROGRAM": "WezTerm", "COLORTERM": "truecolor"},
			want: Info{Program: ProgramWezTerm, Truecolor: true},
		},
		{
			name: "wezterm inside tmux through WEZTERM_PANE",
			env:  map[string]string{"TERM_PROGRAM": "tmux", "WEZTERM_PANE": "0", "TMUX": "x"},
			want: Info{Program: ProgramWezTerm, Truecolor: true, InsideTmux: true},
		},
		{
			name: "ghostty by TERM",
			env:  map[string]string{"TERM": "xterm-ghostty"},
			want: Info{Program: ProgramGhostty, Term: "xterm-ghostty", Truecolor: true},
		},
		{
			name: "ghostty by TERM_PROGRAM",
			env:  map[string]string{"TERM_PROGRAM": "ghostty"},
			want: Info{Program: ProgramGhostty, Truecolor: true},
		},
		{
			name: "ghostty inside tmux through resources dir",
			env:  map[string]string{"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/Contents/Resources/ghostty", "TMUX": "x"},
			want: Info{Program: ProgramGhostty, Truecolor: true, InsideTmux: true},
		},
		{
			name: "kitty by TERM",
			env:  map[string]string{"TERM": "xterm-kitty"},
			want: Info{Program: ProgramKitty, Term: "xterm-kitty", Truecolor: true},
		},
		{
			name: "kitty by window id",
			env:  map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "tmux-256color"},
			want: Info{Program: ProgramKitty, Term: "tmux-256color", Truecolor: true},
		},
		{
			name: "alacritty by TERM",
			env:  map[string]string{"TERM": "alacritty"},
			want: Info{Program: ProgramAlacritty, Term: "alacritty", Truecolor: true},
		},
		{
			name: "alacritty by socket",
			env:  map[string]string{"ALACRITTY_SOCKET": "/tmp/Alacritty.sock"},
			want: Info{Program: ProgramAlacritty, Truecolor: true},
		},
		{
			name: "vscode terminal",
			env:  map[string]string{"TERM_PROGRAM": "vscode", "COLORTERM": "truecolor"},
			want: Info{Program: ProgramVSCode, Truecolor: true},
		},
		{
			name: "unknown TERM_PROGRAM is kept verbatim",
			env:  map[string]string{"TERM_PROGRAM": "SomeTerm", "COLORTERM": "24bit"},
			want: Info{Program: "SomeTerm", Truecolor: true},
		},
		{
			name: "screen TERM_PROGRAM is not a terminal program",
			env:  map[string]string{"TERM_PROGRAM": "screen"},
			want: Info{},
		},
		{
			name: "direct color TERM",
			env:  map[string]string{"TERM": "xterm-direct"},
			want: Info{Term: "xterm-direct", Truecolor: true},
		},
		{
			name: "COLORTERM is case insensitive",
			env:  map[string]string{"COLORTERM": "TrueColor"},
			want: Info{Truecolor: true},
		},
		{
			name: "COLORTERM 256 is not truecolor",
			env:  map[string]string{"COLORTERM": "yes", "TERM": "xterm-256color"},
			want: Info{Term: "xterm-256color"},
		},
		{
			name: "wsl and ssh",
			env:  map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "SSH_CONNECTION": "10.0.0.1 5000 10.0.0.2 22", "LC_ALL": "C.UTF-8"},
			want: Info{WSL: true, SSH: true, UTF8: true},
		},
		{
			name: "wsl interop and ssh tty",
			env:  map[string]string{"WSL_INTEROP": "/run/WSL/1_interop", "SSH_TTY": "/dev/pts/1"},
			want: Info{WSL: true, SSH: true},
		},
		{
			name: "LC_ALL wins over LANG for UTF-8",
			env:  map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"},
			want: Info{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(env(tc.env)); got != tc.want {
				t.Fatalf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProgramName(t *testing.T) {
	cases := []struct {
		program Program
		want    string
	}{
		{ProgramUnknown, "unknown terminal"},
		{ProgramAppleTerminal, "Terminal"},
		{ProgramITerm2, "iTerm2"},
		{ProgramWezTerm, "WezTerm"},
		{ProgramGhostty, "Ghostty"},
		{ProgramKitty, "kitty"},
		{ProgramAlacritty, "Alacritty"},
		{ProgramVSCode, "VS Code terminal"},
		{Program("SomeTerm"), "SomeTerm"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.program.Name(); got != tc.want {
				t.Fatalf("Name() = %q, want %q", got, tc.want)
			}
		})
	}
}

func lookPathIn(found ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(found, name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestDetectClipboard(t *testing.T) {
	cases := []struct {
		name       string
		goos       string
		env        map[string]string
		found      []string
		candidates []string
		want       Clipboard
		ok         bool
	}{
		{
			name:       "macOS pbcopy",
			goos:       "darwin",
			found:      []string{"pbcopy"},
			candidates: []string{"pbcopy"},
			want:       Clipboard{Tool: "pbcopy", Path: "/usr/bin/pbcopy"},
			ok:         true,
		},
		{
			name:       "macOS without pbcopy",
			goos:       "darwin",
			candidates: []string{"pbcopy"},
		},
		{
			name:       "windows clip.exe",
			goos:       "windows",
			found:      []string{"clip.exe"},
			candidates: []string{"clip.exe"},
			want:       Clipboard{Tool: "clip.exe", Path: "/usr/bin/clip.exe"},
			ok:         true,
		},
		{
			name:       "wayland prefers wl-copy over xclip",
			goos:       "linux",
			env:        map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
			found:      []string{"xclip", "wl-copy"},
			candidates: []string{"wl-copy", "xclip", "xsel"},
			want:       Clipboard{Tool: "wl-copy", Path: "/usr/bin/wl-copy"},
			ok:         true,
		},
		{
			name:       "x11 prefers xclip over wl-copy",
			goos:       "linux",
			env:        map[string]string{"DISPLAY": ":0"},
			found:      []string{"wl-copy", "xclip"},
			candidates: []string{"xclip", "xsel", "wl-copy"},
			want:       Clipboard{Tool: "xclip", Path: "/usr/bin/xclip"},
			ok:         true,
		},
		{
			name:       "x11 falls back to xsel",
			goos:       "linux",
			env:        map[string]string{"DISPLAY": ":0"},
			found:      []string{"xsel"},
			candidates: []string{"xclip", "xsel", "wl-copy"},
			want:       Clipboard{Tool: "xsel", Path: "/usr/bin/xsel"},
			ok:         true,
		},
		{
			name:       "wsl prefers clip.exe",
			goos:       "linux",
			env:        map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WAYLAND_DISPLAY": "wayland-0"},
			found:      []string{"wl-copy", "clip.exe"},
			candidates: []string{"clip.exe", "wl-copy", "xclip", "xsel"},
			want:       Clipboard{Tool: "clip.exe", Path: "/usr/bin/clip.exe"},
			ok:         true,
		},
		{
			name:       "headless linux without tools",
			goos:       "linux",
			candidates: []string{"wl-copy", "xclip", "xsel"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := env(tc.env)
			if got := ClipboardCandidates(tc.goos, getenv); !slices.Equal(got, tc.candidates) {
				t.Fatalf("ClipboardCandidates() = %v, want %v", got, tc.candidates)
			}
			got, ok := DetectClipboard(tc.goos, getenv, lookPathIn(tc.found...))
			if got != tc.want || ok != tc.ok {
				t.Fatalf("DetectClipboard() = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDetectClipboardIgnoresEmptyPath(t *testing.T) {
	lookPath := func(string) (string, error) { return "", nil }
	if got, ok := DetectClipboard("darwin", env(nil), lookPath); ok {
		t.Fatalf("an empty path must not count as found, got %+v", got)
	}
}

func TestClipboardPackage(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"wayland", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "wl-clipboard"},
		{"x11", map[string]string{"DISPLAY": ":0"}, "xclip"},
		{"headless", nil, "xclip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClipboardPackage(env(tc.env)); got != tc.want {
				t.Fatalf("ClipboardPackage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOptionAsMeta(t *testing.T) {
	cases := []struct {
		name    string
		program Program
		goos    string
		needed  bool
		setting string
	}{
		{"terminal", ProgramAppleTerminal, "darwin", true, `Terminal > Settings > Profiles > Keyboard: enable "Use Option as Meta Key"`},
		{"iterm2", ProgramITerm2, "darwin", true, `iTerm2 > Settings > Profiles > Keys > General: set "Left Option key" and "Right Option key" to "Esc+"`},
		{"wezterm left alt is already meta", ProgramWezTerm, "darwin", false, "send_composed_key_when_right_alt_is_pressed = false"},
		{"ghostty", ProgramGhostty, "darwin", true, "macos-option-as-alt = true"},
		{"kitty", ProgramKitty, "darwin", true, "kitty.conf: macos_option_as_alt yes"},
		{"alacritty", ProgramAlacritty, "darwin", true, `[window] option_as_alt = "Both"`},
		{"vscode", ProgramVSCode, "darwin", true, `"terminal.integrated.macOptionIsMeta": true`},
		{"unknown on macOS", ProgramUnknown, "darwin", true, `"Option as Meta"`},
		{"custom program on macOS", Program("SomeTerm"), "darwin", true, "tmux prefix"},
		{"linux kitty", ProgramKitty, "linux", false, "Alt sends Meta by default"},
		{"linux unknown", ProgramUnknown, "linux", false, "Alt sends Meta by default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := OptionAsMeta(tc.program, tc.goos)
			if g.Program != tc.program || g.Needed != tc.needed || !strings.Contains(g.Setting, tc.setting) {
				t.Fatalf("OptionAsMeta(%q, %q) = %+v, want needed=%v and setting containing %q", tc.program, tc.goos, g, tc.needed, tc.setting)
			}
		})
	}
}
