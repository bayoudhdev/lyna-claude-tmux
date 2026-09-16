package review

import (
	"errors"
	"strings"
	"testing"
)

func TestParseNvimVersion(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    NvimVersion
		wantErr bool
	}{
		{name: "release with details", in: "NVIM v0.12.5\nBuild type: Release\nLuaJIT 2.1\n", want: NvimVersion{0, 12, 5, ""}},
		{name: "release alone", in: "NVIM v0.9.0", want: NvimVersion{0, 9, 0, ""}},
		{name: "crlf", in: "NVIM v0.10.4\r\nBuild type: Release\r\n", want: NvimVersion{0, 10, 4, ""}},
		{name: "nightly", in: "NVIM v0.13.0-dev-123+gabc1234\n", want: NvimVersion{0, 13, 0, "-dev-123+gabc1234"}},
		{name: "plus only", in: "NVIM v0.11.1+local", want: NvimVersion{0, 11, 1, "+local"}},
		{name: "major one", in: "NVIM v1.0.0", want: NvimVersion{1, 0, 0, ""}},
		{name: "empty", in: "", wantErr: true},
		{name: "vim", in: "VIM - Vi IMproved 9.1", wantErr: true},
		{name: "missing v", in: "NVIM 0.12.5", wantErr: true},
		{name: "two parts", in: "NVIM v0.12", wantErr: true},
		{name: "four parts", in: "NVIM v0.12.5.1", wantErr: true},
		{name: "letter", in: "NVIM v0.1x.5", wantErr: true},
		{name: "empty part", in: "NVIM v0..5", wantErr: true},
		{name: "huge number", in: "NVIM v0.1234567.0", wantErr: true},
		{name: "bare dash suffix", in: "NVIM v0.12.5-", wantErr: true},
		{name: "space in suffix", in: "NVIM v0.12.5-dev 1", wantErr: true},
		{name: "long suffix", in: "NVIM v0.12.5-" + strings.Repeat("a", maxVersionSuffix), wantErr: true},
		{name: "version on second line", in: "warning\nNVIM v0.12.5", wantErr: true},
		{name: "long garbage", in: strings.Repeat("x", 200), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNvimVersion(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrNvimVersion) {
					t.Fatalf("ParseNvimVersion(%q) = %v, %v; want ErrNvimVersion", tc.in, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseNvimVersion(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestNvimVersionCompare(t *testing.T) {
	cases := []struct {
		name            string
		v               NvimVersion
		wantString      string
		wantSupported   bool
		wantRecommended bool
	}{
		{name: "too old", v: NvimVersion{0, 8, 3, ""}, wantString: "v0.8.3"},
		{name: "minimum", v: NvimVersion{0, 9, 0, ""}, wantString: "v0.9.0", wantSupported: true},
		{name: "last before recommended", v: NvimVersion{0, 9, 5, ""}, wantString: "v0.9.5", wantSupported: true},
		{name: "recommended dev build", v: NvimVersion{0, 10, 0, "-dev"}, wantString: "v0.10.0-dev", wantSupported: true, wantRecommended: true},
		{name: "current", v: NvimVersion{0, 12, 5, ""}, wantString: "v0.12.5", wantSupported: true, wantRecommended: true},
		{name: "next major", v: NvimVersion{1, 0, 0, ""}, wantString: "v1.0.0", wantSupported: true, wantRecommended: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.String(); got != tc.wantString {
				t.Errorf("String() = %q, want %q", got, tc.wantString)
			}
			if got := tc.v.Supported(); got != tc.wantSupported {
				t.Errorf("Supported() = %v, want %v", got, tc.wantSupported)
			}
			if got := tc.v.Recommended(); got != tc.wantRecommended {
				t.Errorf("Recommended() = %v, want %v", got, tc.wantRecommended)
			}
		})
	}
}

// FuzzParseNvimVersion checks that parsing never panics and that an accepted
// version renders to a canonical line that parses back to the same version.
func FuzzParseNvimVersion(f *testing.F) {
	for _, s := range []string{"NVIM v0.12.5\nBuild", "NVIM v0.13.0-dev-1+gabc", "NVIM v00.010.1", "NVIM v1.2", "", "NVIM v0.9.0\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		v, err := ParseNvimVersion(in)
		if err != nil {
			return
		}
		canonical := "NVIM " + v.String()
		again, err := ParseNvimVersion(canonical + "\nBuild type: Release")
		if err != nil || again != v {
			t.Fatalf("%q parsed to %+v, canonical %q parsed to %+v, %v", in, v, canonical, again, err)
		}
		if again.String() != v.String() {
			t.Fatalf("canonical form changed: %q vs %q", again.String(), v.String())
		}
	})
}
