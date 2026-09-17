package devcontainer

import (
	"bytes"
	"debug/elf"
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
	files, err := Render(Options{Project: "api", Source: release})
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

// FuzzValidateBinary checks that the ELF check never panics on arbitrary
// bytes and accepts nothing that does not start with the ELF magic.
func FuzzValidateBinary(f *testing.F) {
	for _, seed := range [][]byte{linuxBinary("amd64"), linuxBinary("arm64"), linuxBinary("386"), []byte("MZ"), nil, []byte("\x7fELF")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for goarch := range elfTargets {
			if err := ValidateBinary(data, goarch); err == nil && !bytes.HasPrefix(data, []byte(elf.ELFMAG)) {
				t.Fatalf("accepted %q for %s", data[:min(8, len(data))], goarch)
			}
		}
	})
}

// FuzzDetectSource checks that whatever a Dockerfile holds, the source read
// back is a valid release version or an error: the parser never hands Render
// a version the pattern refuses, and never panics.
func FuzzDetectSource(f *testing.F) {
	files, err := Render(Options{Project: "api", Source: release})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{string(files[FileDockerfile]), "", stagedLine, installScript + " --prefix /usr/local/bin --version v1.2.3\n", installScript + " --prefix /usr/local/bin --version ../x\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		writeFile(t, dir, FileDockerfile, content)
		src, err := DetectSource(dir)
		switch {
		case err != nil:
			if src != (Source{}) {
				t.Fatalf("source %+v returned with error %v", src, err)
			}
		case src.BinarySHA256 != "":
			t.Fatalf("a staged source without a binary on disk: %+v", src)
		case !versionPattern().MatchString(src.Version):
			t.Fatalf("version %q read from the Dockerfile", src.Version)
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
		files, err := Render(Options{Project: "api", Source: release, AllowedDomains: []string{domain}})
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
