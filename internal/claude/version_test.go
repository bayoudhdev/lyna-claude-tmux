package claude

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Version
		bad  bool
	}{
		{name: "release output", in: "2.1.272 (Claude Code)\n", want: Version{2, 1, 272, ""}},
		{name: "bare number", in: "2.1.257", want: Version{2, 1, 257, ""}},
		{name: "surrounding whitespace and CRLF", in: "\n  3.0.0 (Claude Code)\r\n", want: Version{3, 0, 0, ""}},
		{name: "prerelease", in: "2.2.0-beta.1 (Claude Code)", want: Version{2, 2, 0, "beta.1"}},
		{name: "only first line", in: "2.1.1 (Claude Code)\nsomething else", want: Version{2, 1, 1, ""}},
		{name: "leading zeros", in: "02.01.009", want: Version{2, 1, 9, ""}},
		{name: "empty", in: "", bad: true},
		{name: "two components", in: "2.1 (Claude Code)", bad: true},
		{name: "four components", in: "2.1.1.1", bad: true},
		{name: "letters", in: "v2.1.1", bad: true},
		{name: "negative", in: "2.-1.1", bad: true},
		{name: "plus sign", in: "2.+1.1", bad: true},
		{name: "huge component", in: "2.1.1234567890", bad: true},
		{name: "empty prerelease", in: "2.1.1-", bad: true},
		{name: "bad prerelease", in: "2.1.1-be_ta", bad: true},
		{name: "trailing text without parenthesis", in: "2.1.1 Claude", bad: true},
		{name: "error message", in: "error: unknown option '--version'", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVersion(tc.in)
			if tc.bad {
				if !errors.Is(err, ErrVersionFormat) {
					t.Fatalf("ParseVersion(%q) = %v, %v; want ErrVersionFormat", tc.in, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseVersion(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			}
		})
	}
	long := strings.Repeat("x", 200)
	if _, err := ParseVersion(long); err == nil || len(err.Error()) > 160 {
		t.Fatalf("long garbage error not clipped: %v", err)
	}
}

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b Version
		want int
	}{
		{a: Version{2, 1, 257, ""}, b: Version{2, 1, 257, ""}, want: 0},
		{a: Version{2, 1, 256, ""}, b: Version{2, 1, 257, ""}, want: -1},
		{a: Version{2, 2, 0, ""}, b: Version{2, 1, 999, ""}, want: 1},
		{a: Version{1, 9, 9, ""}, b: Version{2, 0, 0, ""}, want: -1},
		{a: Version{3, 0, 0, ""}, b: Version{2, 9, 9, ""}, want: 1},
		{a: Version{2, 1, 257, "rc.1"}, b: Version{2, 1, 257, ""}, want: -1},
		{a: Version{2, 1, 257, ""}, b: Version{2, 1, 257, "rc.1"}, want: 1},
		{a: Version{2, 1, 257, "alpha"}, b: Version{2, 1, 257, "beta"}, want: -1},
		{a: Version{2, 1, 257, "beta"}, b: Version{2, 1, 257, "alpha"}, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.a.String()+"_vs_"+tc.b.String(), func(t *testing.T) {
			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Fatalf("Compare = %d, want %d", got, tc.want)
			}
			if got := tc.a.AtLeast(tc.b); got != (tc.want >= 0) {
				t.Fatalf("AtLeast = %v", got)
			}
		})
	}
}

func TestCheckVersion(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{in: "2.1.272 (Claude Code)", ok: true},
		{in: "2.1.257 (Claude Code)", ok: true},
		{in: "3.0.0", ok: true},
		{in: "2.1.256 (Claude Code)", want: "found 2.1.256, need 2.1.257 or newer; run `claude update`"},
		{in: "2.1.257-rc.1", want: "found 2.1.257-rc.1"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			v, err := ParseVersion(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			err = CheckVersion(v)
			if tc.ok {
				if err != nil {
					t.Fatalf("CheckVersion(%s) = %v", v, err)
				}
				return
			}
			if !errors.Is(err, ErrTooOld) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckVersion(%s) = %v, want ErrTooOld containing %q", v, err, tc.want)
			}
		})
	}
}

func FuzzParseVersion(f *testing.F) {
	for _, s := range []string{"2.1.272 (Claude Code)", "2.1.257", "1.0.0-rc.1 (x)", "", "2..1", "9999999999.1.1", "2.1.1-\x00", "\n\n2.1.1\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseVersion(s)
		if err != nil {
			if !errors.Is(err, ErrVersionFormat) {
				t.Fatalf("error %v does not wrap ErrVersionFormat", err)
			}
			return
		}
		if v.Major < 0 || v.Minor < 0 || v.Patch < 0 {
			t.Fatalf("negative component in %+v", v)
		}
		again, err := ParseVersion(v.String())
		if err != nil || again != v {
			t.Fatalf("round trip of %q: %+v -> %q -> %+v, %v", s, v, v.String(), again, err)
		}
		if v.Compare(again) != 0 || !v.AtLeast(again) {
			t.Fatalf("version %+v does not equal itself", v)
		}
	})
}
