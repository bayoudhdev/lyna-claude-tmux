package devcontainer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzMountField checks that any directory survives docker's CSV parsing of
// --mount unchanged, so a path can never inject mount options.
func FuzzMountField(f *testing.F) {
	for _, seed := range []string{"/home/u/api", "/tmp/a,readonly=false,source=/", `/tmp/"q"`, "/tmp/a b", "/tmp/é"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, dir string) {
		if strings.ContainsAny(dir, "\x00\r\n") || !strings.HasPrefix(dir, "/") {
			// Target.Validate refuses these before a mount is built.
			return
		}
		assertMountRoundTrip(t, dir)
	})
}

// FuzzProjectName checks that every directory name maps to a valid project.
func FuzzProjectName(f *testing.F) {
	for _, seed := range []string{"/src/My Project", "", "/", "---", "/a/Été_2026", strings.Repeat("ab-", 30)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, dir string) {
		name := ProjectName(dir)
		if err := ValidateProject(name); err != nil {
			t.Fatalf("ProjectName(%q) = %q: %v", dir, name, err)
		}
	})
}

// FuzzVerify checks the rule the build rests on: a project passes only when
// its file holds the rendered bytes, whatever else the content looks like.
func FuzzVerify(f *testing.F) {
	files, err := Render(Options{Project: "api"})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"", "FROM debian:13-slim\n", "\x00\n", string(files[FileDockerfile]), strings.Repeat("a", 4096)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		if _, err := Write(dir, files, false); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(FileDockerfile)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		accepted := Verify(dir, files) == nil
		if identical := content == string(files[FileDockerfile]); accepted != identical {
			t.Fatalf("Verify accepted = %v for a Dockerfile identical = %v", accepted, identical)
		}
	})
}

// FuzzRender checks that an accepted domain can only appear as its own line of
// the allowlist, and that rendering never panics.
func FuzzRender(f *testing.F) {
	for _, seed := range []string{"registry.npmjs.org", "a\nb.com", "x.com # y", "EXAMPLE.org.", "1.2.3.4", "*.github.com"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, domain string) {
		files, err := Render(Options{Project: "api", AllowedDomains: []string{domain}})
		if err != nil {
			return
		}
		d := NormalizeDomain(domain)
		lines := bytes.Split(files[FileAllowedDomain], []byte("\n"))
		found := false
		for _, line := range lines {
			if len(line) > 0 && line[0] != '#' && strings.ContainsAny(string(line), " \t#;$`") {
				t.Fatalf("unsafe allowlist line %q", line)
			}
			found = found || string(line) == d
		}
		if !found {
			t.Fatalf("domain %q missing from allowlist", d)
		}
	})
}
