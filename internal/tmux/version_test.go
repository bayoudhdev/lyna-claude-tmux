package tmux

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in        string
		major     int
		minor     int
		suffix    string
		supported bool
		wantErr   bool
	}{
		{in: "tmux 3.7c\n", major: 3, minor: 7, suffix: "c", supported: true},
		{in: "tmux 3.3a", major: 3, minor: 3, suffix: "a", supported: true},
		{in: "tmux 3.3", major: 3, minor: 3, supported: true},
		{in: "tmux 3.2a", major: 3, minor: 2, suffix: "a"},
		{in: "tmux 3.2-rc4", major: 3, minor: 2, suffix: "rc4"},
		// A development branch carries the number of the release it is heading
		// for, so only the features of the release before it can be relied on.
		{in: "tmux next-3.8", major: 3, minor: 7, suffix: "next", supported: true},
		{in: "tmux next-3.4", major: 3, minor: 3, suffix: "next", supported: true},
		{in: "tmux next-3.3", major: 3, minor: 2, suffix: "next"},
		{in: "tmux next-4.0", major: 3, minor: 9, suffix: "next", supported: true},
		{in: "tmux next-3.x", wantErr: true},
		{in: "tmux 10.0", major: 10, minor: 0, supported: true},
		{in: "tmux master", major: 99, supported: true},
		{in: "tmux openbsd-7.6", major: 3, minor: 4, supported: true},
		{in: "tmux openbsd-7.7", major: 3, minor: 5, supported: true},
		{in: "tmux openbsd-7.3", major: 3, minor: 3, supported: true},
		{in: "tmux openbsd-6.9", major: 3, minor: 2},
		{in: "tmux 2.9", major: 2, minor: 9},
		{in: "", wantErr: true},
		{in: "tmux", wantErr: true},
		{in: "tmux abc", wantErr: true},
		{in: "tmux 3.x", wantErr: true},
		{in: "tmux x.3", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			v, err := ParseVersion(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if v.Major != tc.major || v.Minor != tc.minor || v.Suffix != tc.suffix {
				t.Fatalf("ParseVersion(%q) = %+v", tc.in, v)
			}
			if v.Supported() != tc.supported {
				t.Fatalf("Supported() = %v, want %v", v.Supported(), tc.supported)
			}
		})
	}
}

func TestVersionHas(t *testing.T) {
	v33 := Version{Major: 3, Minor: 3}
	v34 := Version{Major: 3, Minor: 4}
	v35 := Version{Major: 3, Minor: 5}
	v36 := Version{Major: 3, Minor: 6}
	v37 := Version{Major: 3, Minor: 7, Suffix: "c"}
	v40 := Version{Major: 4, Minor: 0}
	cases := []struct {
		name    string
		v       Version
		feature Feature
		want    bool
	}{
		{"3.3 no user ranges", v33, FeatureUserRanges, false},
		{"3.4 user ranges", v34, FeatureUserRanges, true},
		{"3.3 no menu styles", v33, FeatureMenuStyles, false},
		{"3.4 menu styles", v34, FeatureMenuStyles, true},
		{"3.4 confirm flags", v34, FeatureConfirmFlags, true},
		{"3.4 message line", v34, FeatureMessageLine, true},
		{"3.4 no extended keys format", v34, FeatureExtendedKeysFormat, false},
		{"3.5 extended keys format", v35, FeatureExtendedKeysFormat, true},
		{"3.5 allow set title", v35, FeatureAllowSetTitle, true},
		{"3.5 no scrollbars", v35, FeaturePaneScrollbars, false},
		{"3.6 scrollbars", v36, FeaturePaneScrollbars, true},
		{"3.6 popup any key", v36, FeaturePopupAnyKey, true},
		{"3.6 no focus follows mouse", v36, FeatureFocusFollowsMouse, false},
		{"3.7 focus follows mouse", v37, FeatureFocusFollowsMouse, true},
		{"4.0 everything", v40, FeatureFocusFollowsMouse, true},
		{"unknown feature", v40, Feature(999), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Has(tc.feature); got != tc.want {
				t.Fatalf("Has(%d) = %v, want %v", tc.feature, got, tc.want)
			}
		})
	}
	if _, _, err := FeatureMinimum(Feature(999)); err == nil {
		t.Fatal("FeatureMinimum(unknown) returned no error")
	}
	if maj, minor, err := FeatureMinimum(FeaturePaneScrollbars); err != nil || maj != 3 || minor != 6 {
		t.Fatalf("FeatureMinimum(scrollbars) = %d.%d %v", maj, minor, err)
	}
}

func TestVersionString(t *testing.T) {
	cases := []struct {
		v    Version
		want string
	}{
		{Version{Major: 3, Minor: 7, Suffix: "c"}, "3.7c"},
		{Version{Major: 3, Minor: 3}, "3.3"},
		{Version{Major: 99}, "master"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.v.String(); got != tc.want {
				t.Fatalf("String() = %q", got)
			}
		})
	}
}
