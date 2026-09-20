package theme

import "testing"

func TestGetSeps(t *testing.T) {
	cases := []struct {
		name      string
		style     string
		wantErr   bool
		powerline bool
	}{
		{name: "powerline", style: StatusPowerline, powerline: true},
		{name: "plain", style: StatusPlain},
		{name: "auto is not a drawn style", style: StatusAuto, wantErr: true},
		{name: "unknown", style: "arrows", wantErr: true},
		{name: "empty", style: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := GetSeps(tc.style)
			if (err != nil) != tc.wantErr {
				t.Fatalf("GetSeps(%q) error %v, want error %v", tc.style, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if s.Name != tc.style || s.Powerline() != tc.powerline {
				t.Fatalf("GetSeps(%q) = %+v, powerline %v", tc.style, s, tc.powerline)
			}
			if tc.powerline && (s.Left == "" || s.Thin == "") {
				t.Errorf("powerline separators incomplete: %+v", s)
			}
			if !tc.powerline && (s.Left != "" || s.Thin != "") {
				t.Errorf("plain style draws separators: %+v", s)
			}
		})
	}
}

func TestResolveStatusStyle(t *testing.T) {
	cases := []struct {
		name    string
		setting string
		icons   string
		want    string
	}{
		{name: "auto with a patched font", setting: StatusAuto, icons: "nerd", want: StatusPowerline},
		{name: "auto with unicode icons", setting: StatusAuto, icons: "unicode", want: StatusPlain},
		{name: "auto with ascii icons", setting: StatusAuto, icons: "ascii", want: StatusPlain},
		{name: "empty is auto", setting: "", icons: "nerd", want: StatusPowerline},
		{name: "asked for powerline without a patched font", setting: StatusPowerline, icons: "ascii", want: StatusPowerline},
		{name: "asked for plain with a patched font", setting: StatusPlain, icons: "nerd", want: StatusPlain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			icons, err := GetIcons(tc.icons)
			if err != nil {
				t.Fatal(err)
			}
			if got := ResolveStatusStyle(tc.setting, icons); got != tc.want {
				t.Errorf("ResolveStatusStyle(%q, %s) = %q, want %q", tc.setting, tc.icons, got, tc.want)
			}
			if _, err := GetSeps(ResolveStatusStyle(tc.setting, icons)); err != nil {
				t.Errorf("resolved style is not drawable: %v", err)
			}
		})
	}
}
