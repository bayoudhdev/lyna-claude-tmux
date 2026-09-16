package termx

// Clipboard is a command-line tool that writes the system clipboard.
type Clipboard struct {
	Tool string `json:"tool"`
	Path string `json:"path"`
}

// ClipboardCandidates lists the tools worth trying on goos, best first.
// Wayland and X11 tools are ordered by the display server the environment
// names, so a Wayland session does not pick an X11 tool that writes a
// clipboard nobody reads. WSL prefers clip.exe, which reaches the Windows
// clipboard without a display server.
func ClipboardCandidates(goos string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		return []string{"pbcopy"}
	case "windows":
		return []string{"clip.exe"}
	}
	var out []string
	if getenv("WSL_DISTRO_NAME") != "" || getenv("WSL_INTEROP") != "" {
		out = append(out, "clip.exe")
	}
	wayland := getenv("WAYLAND_DISPLAY") != ""
	x11 := getenv("DISPLAY") != ""
	switch {
	case wayland:
		out = append(out, "wl-copy", "xclip", "xsel")
	case x11:
		out = append(out, "xclip", "xsel", "wl-copy")
	default:
		out = append(out, "wl-copy", "xclip", "xsel")
	}
	return out
}

// DetectClipboard returns the first candidate found on PATH.
func DetectClipboard(goos string, getenv func(string) string, lookPath func(string) (string, error)) (Clipboard, bool) {
	for _, tool := range ClipboardCandidates(goos, getenv) {
		if path, err := lookPath(tool); err == nil && path != "" {
			return Clipboard{Tool: tool, Path: path}, true
		}
	}
	return Clipboard{}, false
}

// ClipboardPackage names the package that provides a clipboard tool for the
// environment, for install hints: wl-clipboard under Wayland, xclip otherwise.
func ClipboardPackage(getenv func(string) string) string {
	if getenv("WAYLAND_DISPLAY") != "" {
		return "wl-clipboard"
	}
	return "xclip"
}
