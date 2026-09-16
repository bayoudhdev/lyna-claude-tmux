package tmux

import (
	"reflect"
	"strings"
	"testing"
)

func TestPopupSpecCommand(t *testing.T) {
	argv := []string{"/opt/it's #{x}/lyna-tmux", "agents", "--popup"}
	cases := []struct {
		name    string
		spec    PopupSpec
		want    Command
		wantErr string
	}{
		{
			name: "every field",
			spec: PopupSpec{Client: "/dev/ttys003", Width: "90%", Height: "40", Dir: "/src/#[x] #{y}", Title: "agents #1", Argv: argv},
			want: Command{
				"display-popup", "-c", "/dev/ttys003", "-E", "-w", "90%", "-h", "40",
				"-d", "/src/#{?,,##}[x] ##{y}", "-T", " agents ##1 ", "--", "/opt/it's #{x}/lyna-tmux", "agents", "--popup",
			},
		},
		{
			name: "defaults left to tmux",
			spec: PopupSpec{Client: "client-1", Argv: []string{"/usr/bin/tmux", "attach-session"}},
			want: Command{"display-popup", "-c", "client-1", "-E", "--", "/usr/bin/tmux", "attach-session"},
		},
		{name: "no client", spec: PopupSpec{Argv: argv}, wantErr: "no client"},
		{name: "single argument would reach the shell", spec: PopupSpec{Client: "c", Argv: []string{"/bin/true"}}, wantErr: "at least one argument"},
		{name: "empty program", spec: PopupSpec{Client: "c", Argv: []string{"", "x"}}, wantErr: "at least one argument"},
		{name: "relative directory", spec: PopupSpec{Client: "c", Dir: "src", Argv: argv}, wantErr: "not absolute"},
		{name: "NUL in an argument", spec: PopupSpec{Client: "c", Argv: []string{"/bin/sh", "a\x00b"}}, wantErr: "NUL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.spec.Command()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Command =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
