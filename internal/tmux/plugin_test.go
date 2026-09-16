package tmux

import (
	"reflect"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

func TestPluginSeq(t *testing.T) {
	const bin = "/home/dev/.local/bin/lyna-tmux"
	launch := Command{"bind-key", "-T", "prefix", "y", "run-shell", "-b", bin + " popup launch --pane #{pane_id} --client #{q:client_name}"}
	agents := Command{"bind-key", "-T", "prefix", "u", "run-shell", "-b", bin + " popup agents --pane #{pane_id} --client #{q:client_name}"}
	bell := Command{"set-hook", "-g", "alert-bell[91]", "run-shell -b '" + bin + " bell-forward #{q:hook_session}'"}
	unset := Command{"set-hook", "-gu", "alert-bell[91]"}
	cases := []struct {
		name string
		opts PluginOptions
		want Seq
	}{
		{name: "defaults", opts: PluginOptions{Bin: bin, LaunchKey: "y", ListKey: "u", ForwardBell: true}, want: Seq{launch, agents, bell}},
		{name: "bell off removes only our hook", opts: PluginOptions{Bin: bin, LaunchKey: "y", ListKey: "u"}, want: Seq{launch, agents, unset}},
		{name: "empty keys leave bindings alone", opts: PluginOptions{Bin: bin, ForwardBell: true}, want: Seq{bell}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PluginSeq(tc.opts); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PluginSeq:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestPluginConfGolden(t *testing.T) {
	cases := []struct {
		name string
		opts PluginOptions
	}{
		{name: "default", opts: PluginOptions{Bin: "/home/dev/.local/bin/lyna-tmux", LaunchKey: "y", ListKey: "u", ForwardBell: true}},
		{name: "hostile-bin-no-bell", opts: PluginOptions{Bin: "/opt/it's #[x] #{pane_id} ;/lyna-tmux", LaunchKey: "M-y", ListKey: `\`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			golden.Assert(t, "plugin/"+tc.name+".conf", []byte(PluginConf(tc.opts)))
		})
	}
}
