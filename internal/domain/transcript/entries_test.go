package transcript

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestEntriesSession reads the entries of a whole session: prompts, answers,
// tool calls and their results, and nothing of the lines a reader has no use
// for.
func TestEntriesSession(t *testing.T) {
	data, err := os.ReadFile("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{At: at("2026-09-15T11:58:01.000Z"), Who: User, Text: "Add a health check route\nand test it."},
		{At: at("2026-09-15T11:58:04.000Z"), Who: Assistant, Text: "I will add the route to the router."},
		{At: at("2026-09-15T11:58:05.000Z"), Who: Tool, Tool: "Bash", Text: "ls src/routes"},
		{At: at("2026-09-15T11:58:06.000Z"), Who: Tool, Text: "health.ts index.ts users.ts"},
		{At: at("2026-09-15T11:58:09.000Z"), Who: Tool, Tool: "Edit", Text: "/work/api/src/routes/health.ts"},
		{At: at("2026-09-15T11:58:10.000Z"), Who: Tool, Text: "File has not been read yet. Read it first before writing to it.", Failed: true},
		{At: at("2026-09-15T11:58:20.000Z"), Who: Assistant, Text: "Done: the route answers 200 on /health."},
	}
	got := Entries(data)
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if !sameEntry(got[i], want[i]) {
			t.Fatalf("entry %d\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestEntries(t *testing.T) {
	const ts = `"timestamp":"2026-09-15T12:00:00.000Z"`
	user := func(content string) string {
		return `{"type":"user","message":{"role":"user","content":` + content + `},` + ts + `}`
	}
	reply := func(content string) string {
		return `{"type":"assistant","message":{"id":"m","content":` + content + `},` + ts + `}`
	}
	cases := []struct {
		name string
		in   string
		want []Entry
	}{
		{name: "a prompt", in: user(`"hello"`), want: []Entry{{Who: User, Text: "hello"}}},
		{name: "a prompt in blocks", in: user(`[{"type":"text","text":"look at this"},{"type":"image","source":{"type":"base64","data":"AAAA"}}]`), want: []Entry{{Who: User, Text: "look at this"}}},
		{name: "an empty prompt", in: user(`"   "`)},
		{name: "a prompt of no content", in: user(`null`)},
		{name: "a prompt Claude Code wrote itself", in: `{"type":"user","isMeta":true,"message":{"content":"Caveat"}}`},
		{name: "an answer in two blocks", in: reply(`[{"type":"text","text":"one"},{"type":"text","text":"two"}]`), want: []Entry{{Who: Assistant, Text: "one"}, {Who: Assistant, Text: "two"}}},
		{name: "an answer as a string", in: reply(`"plain"`), want: []Entry{{Who: Assistant, Text: "plain"}}},
		{name: "thinking is not shown", in: reply(`[{"type":"thinking","thinking":"hmm"},{"type":"redacted_thinking","data":"x"}]`)},
		{name: "a tool call by the path it acts on", in: reply(`[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/work/a.go","limit":20}}]`), want: []Entry{{Who: Tool, Tool: "Read", Text: "/work/a.go"}}},
		{name: "a tool call by its pattern", in: reply(`[{"type":"tool_use","name":"Grep","input":{"pattern":"func main","path":"/work"}}]`), want: []Entry{{Who: Tool, Tool: "Grep", Text: "func main"}}},
		{name: "a tool call by the directory it lists", in: reply(`[{"type":"tool_use","name":"LS","input":{"path":"/work","ignore":["node_modules"]}}]`), want: []Entry{{Who: Tool, Tool: "LS", Text: "/work"}}},
		{name: "a tool call with no known input", in: reply(`[{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"content":"a"}]}}]`), want: []Entry{{Who: Tool, Tool: "TodoWrite", Text: `{"todos":[{"content":"a"}]}`}}},
		{name: "a tool call with a blank command", in: reply(`[{"type":"tool_use","name":"Bash","input":{"command":"  ","description":"nothing"}}]`), want: []Entry{{Who: Tool, Tool: "Bash", Text: "nothing"}}},
		{name: "a tool call with no input", in: reply(`[{"type":"tool_use","name":"Stop"}]`), want: []Entry{{Who: Tool, Tool: "Stop"}}},
		{name: "a tool call that names no tool", in: reply(`[{"type":"tool_use","input":{"command":"ls"}}]`)},
		{name: "a tool call on the user's side", in: user(`[{"type":"tool_use","name":"Bash","input":{}}]`)},
		{name: "a result as a string", in: user(`[{"type":"tool_result","tool_use_id":"t1","content":"a\n\tb"}]`), want: []Entry{{Who: Tool, Text: "a b"}}},
		{name: "a result in blocks", in: user(`[{"type":"tool_result","content":[{"type":"text","text":"x"},{"type":"image"},{"type":"text","text":"y"}]}]`), want: []Entry{{Who: Tool, Text: "x y"}}},
		{name: "a result that failed", in: user(`[{"type":"tool_result","content":"boom","is_error":true}]`), want: []Entry{{Who: Tool, Text: "boom", Failed: true}}},
		{name: "a result of nothing", in: user(`[{"type":"tool_result"}]`), want: []Entry{{Who: Tool}}},
		{name: "a result on the assistant's side", in: reply(`[{"type":"tool_result","content":"x"}]`)},
		{name: "a block of the wrong shape leaves the others", in: reply(`[{"type":"text","text":7},{"type":"text","text":"kept"}]`), want: []Entry{{Who: Assistant, Text: "kept"}}},
		{name: "content of the wrong shape", in: reply(`{"type":"text"}`)},
		{name: "a line of another type", in: `{"type":"summary","summary":"x"}`},
		{name: "a line that is not JSON", in: `{"type":"user","message":`},
		{name: "a line that is not an object", in: `[1,2]`},
		{name: "an empty line", in: ``},
		{name: "escape sequences are removed", in: user(`"\u001b]52;c;aGk=\u0007hi \u001b[31mred\u001b[0m"`), want: []Entry{{Who: User, Text: "hi red"}}},
		{name: "tabs become spaces", in: reply(`"a\tb"`), want: []Entry{{Who: Assistant, Text: "a    b"}}},
		{name: "a tool name on one line", in: reply(`[{"type":"tool_use","name":"Bad\nName\u001b[2J","input":{"command":"ls"}}]`), want: []Entry{{Who: Tool, Tool: "Bad Name", Text: "ls"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entries([]byte(tc.in))
			for i := range got {
				// The time is covered by the session test; here it is the
				// same for every line.
				got[i].At = got[i].At.UTC()
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%d entries %+v, want %d", len(got), got, len(tc.want))
			}
			for i := range tc.want {
				tc.want[i].At = got[i].At
				if !reflect.DeepEqual(got[i], tc.want[i]) {
					t.Fatalf("entry %d\n got %+v\nwant %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestEntriesBounds covers what a reader is shown of a message that is far
// longer than anything it can draw.
func TestEntriesBounds(t *testing.T) {
	long := strings.Repeat("word ", MaxText)
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{name: "an answer the size of a file", in: `{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"}]}}`, max: MaxText},
		{name: "a result the size of a file", in: `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + long + `"}]}}`, max: MaxSummary},
		{name: "a call with a long command", in: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + long + `"}}]}}`, max: MaxSummary},
		{name: "a long tool name", in: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + strings.Repeat("n", 500) + `","input":{}}]}}`, max: MaxToolName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entries([]byte(tc.in))
			if len(got) != 1 {
				t.Fatalf("entries %d", len(got))
			}
			text := got[0].Text
			if got[0].Tool != "" && tc.max == MaxToolName {
				text = got[0].Tool
			}
			n := len(text)
			if tc.max != MaxText {
				n = utf8.RuneCountInString(text)
			}
			if n > tc.max || !strings.HasSuffix(text, ellipsis) {
				t.Fatalf("%d long, want at most %d ending in the ellipsis", n, tc.max)
			}
		})
	}
}

func TestCuts(t *testing.T) {
	cases := []struct {
		name string
		cut  func(string, int) string
		in   string
		n    int
		want string
	}{
		{name: "runes that fit", cut: cutRunes, in: "héllo", n: 5, want: "héllo"},
		{name: "runes cut", cut: cutRunes, in: "héllo world", n: 6, want: "hél..."},
		{name: "bytes that fit", cut: cutBytes, in: "abc", n: 3, want: "abc"},
		{name: "bytes cut", cut: cutBytes, in: "abcdef", n: 5, want: "ab..."},
		{name: "bytes cut before a rune it would split", cut: cutBytes, in: "aé€bcdef", n: 7, want: "aé..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cut(tc.in, tc.n)
			if got != tc.want || !utf8.ValidString(got) {
				t.Fatalf("cut(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// sameEntry compares two entries, their times as instants.
func sameEntry(a, b Entry) bool {
	at := a.At.Equal(b.At)
	a.At, b.At = time.Time{}, time.Time{}
	return at && a == b
}
