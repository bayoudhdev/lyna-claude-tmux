package review

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultPin(t *testing.T) {
	pin := DefaultPin()
	if err := pin.Validate(); err != nil {
		t.Fatalf("DefaultPin().Validate() = %v", err)
	}
	if got, want := pin.ReleaseURL(), "https://github.com/esmuellert/codediff.nvim/releases/download/v4.0.6"; got != want {
		t.Fatalf("ReleaseURL() = %q, want %q", got, want)
	}
	cases := []struct {
		platform  string
		wantNames []string
		wantFiles []string
	}{
		{platform: "darwin/arm64", wantNames: []string{"libvscode_diff_macos_arm64_4.0.6.dylib"}, wantFiles: []string{"libvscode_diff_4.0.6.dylib"}},
		{platform: "darwin/amd64", wantNames: []string{"libvscode_diff_macos_x64_4.0.6.dylib"}, wantFiles: []string{"libvscode_diff_4.0.6.dylib"}},
		{platform: "linux/arm64", wantNames: []string{"libvscode_diff_linux_arm64_4.0.6.so", "libgomp_linux_arm64_4.0.6.so.1"}, wantFiles: []string{"libvscode_diff_4.0.6.so", "libgomp.so.1"}},
		{platform: "linux/amd64", wantNames: []string{"libvscode_diff_linux_x64_4.0.6.so", "libgomp_linux_x64_4.0.6.so.1"}, wantFiles: []string{"libvscode_diff_4.0.6.so", "libgomp.so.1"}},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			goos, goarch, _ := strings.Cut(tc.platform, "/")
			assets, err := pin.AssetsFor(goos, goarch)
			if err != nil {
				t.Fatalf("AssetsFor() error = %v", err)
			}
			if len(assets) != len(tc.wantNames) {
				t.Fatalf("got %d assets, want %d", len(assets), len(tc.wantNames))
			}
			for i, a := range assets {
				if a.Name != tc.wantNames[i] || a.File != tc.wantFiles[i] {
					t.Errorf("asset %d = %s -> %s, want %s -> %s", i, a.Name, a.File, tc.wantNames[i], tc.wantFiles[i])
				}
				if seen[a.SHA256] {
					t.Errorf("digest of %s repeats another asset's", a.Name)
				}
				seen[a.SHA256] = true
			}
		})
	}
}

func TestAssetsForUnsupported(t *testing.T) {
	cases := []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"linux", "386"},
		{"freebsd", "arm64"},
		{"", ""},
	}
	pin := DefaultPin()
	for _, tc := range cases {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			if _, err := pin.AssetsFor(tc.goos, tc.goarch); !errors.Is(err, ErrUnsupportedPlatform) {
				t.Fatalf("AssetsFor() error = %v, want ErrUnsupportedPlatform", err)
			}
		})
	}
}

func TestPinValidate(t *testing.T) {
	good := DefaultPin()
	digest := strings.Repeat("a", 64)
	with := func(edit func(*Pin)) Pin {
		p := DefaultPin()
		edit(&p)
		return p
	}
	cases := []struct {
		name    string
		pin     Pin
		wantErr bool
	}{
		{name: "default", pin: good},
		{name: "no assets", pin: with(func(p *Pin) { p.Assets = nil })},
		{name: "short commit", pin: with(func(p *Pin) { p.Commit = "09d9ebe" }), wantErr: true},
		{name: "uppercase commit", pin: with(func(p *Pin) { p.Commit = strings.ToUpper(good.Commit) }), wantErr: true},
		{name: "empty version", pin: with(func(p *Pin) { p.Version = "" }), wantErr: true},
		{name: "version with slash", pin: with(func(p *Pin) { p.Version = "4/0" }), wantErr: true},
		{name: "dot version", pin: with(func(p *Pin) { p.Version = ".." }), wantErr: true},
		{name: "bad digest", pin: with(func(p *Pin) { p.Assets = map[string][]Asset{"linux/amd64": {{Name: "a", File: "b", SHA256: "xyz"}}} }), wantErr: true},
		{name: "traversal file", pin: with(func(p *Pin) {
			p.Assets = map[string][]Asset{"linux/amd64": {{Name: "a", File: "../b", SHA256: digest}}}
		}), wantErr: true},
		{name: "dot dot name", pin: with(func(p *Pin) { p.Assets = map[string][]Asset{"linux/amd64": {{Name: "..", File: "b", SHA256: digest}}} }), wantErr: true},
		{name: "empty file", pin: with(func(p *Pin) { p.Assets = map[string][]Asset{"linux/amd64": {{Name: "a", File: "", SHA256: digest}}} }), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.pin.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidPin) {
				t.Fatalf("Validate() = %v, want ErrInvalidPin", err)
			}
		})
	}
}

func TestLibraryFiles(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{name: "darwin library", got: LibraryFile("4.0.6", "darwin"), want: "libvscode_diff_4.0.6.dylib"},
		{name: "linux library", got: LibraryFile("4.0.6", "linux"), want: "libvscode_diff_4.0.6.so"},
		{name: "darwin manual build", got: IsUnverifiedFile("libvscode_diff.dylib", "darwin"), want: true},
		{name: "linux manual build", got: IsUnverifiedFile("libvscode_diff.so", "linux"), want: true},
		{name: "other platform extension", got: IsUnverifiedFile("libvscode_diff.so", "darwin"), want: false},
		{name: "watcher", got: IsUnverifiedFile("codediff-watcher", "linux"), want: true},
		{name: "versioned watcher", got: IsUnverifiedFile("codediff-watcher_0.23.2", "darwin"), want: true},
		{name: "verified library", got: IsUnverifiedFile("libvscode_diff_4.0.6.so", "linux"), want: false},
		{name: "plugin file", got: IsUnverifiedFile("VERSION", "linux"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}
