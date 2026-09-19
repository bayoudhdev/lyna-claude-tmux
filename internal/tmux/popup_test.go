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
		{
			name: "over a pane",
			spec: PopupSpec{Pane: "%12", Width: "95%", Height: "95%", Dir: "/src/api", Title: "review", Argv: []string{"/opt/lmux", "review", "--popup"}},
			want: Command{
				"display-popup", "-t", "%12", "-E", "-w", "95%", "-h", "95%",
				"-d", "/src/api", "-T", " review ", "--", "/opt/lmux", "review", "--popup",
			},
		},
		{
			name: "a failure kept on screen",
			spec: PopupSpec{Pane: "%3", Title: "spawn", Argv: []string{"/opt/lmux", "spawn", "--session", "api"}, KeepOnFailure: true},
			want: Command{"display-popup", "-t", "%3", "-E", "-E", "-T", " spawn ", "--", "/opt/lmux", "spawn", "--session", "api"},
		},
		{
			name: "client and pane",
			spec: PopupSpec{Client: "client-1", Pane: "%0", Argv: []string{"/usr/bin/tmux", "attach-session"}},
			want: Command{"display-popup", "-c", "client-1", "-t", "%0", "-E", "--", "/usr/bin/tmux", "attach-session"},
		},
		{name: "no client and no pane", spec: PopupSpec{Argv: argv}, wantErr: "no client and no pane"},
		{name: "pane is not a pane id", spec: PopupSpec{Pane: "review", Argv: argv}, wantErr: "not a pane id"},
		{name: "NUL in the pane", spec: PopupSpec{Client: "c", Pane: "%1\x00", Argv: argv}, wantErr: "not a pane id"},
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
