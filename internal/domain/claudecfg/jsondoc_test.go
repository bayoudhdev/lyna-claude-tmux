package claudecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseJSONRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "scalar string", in: `"x"`, want: "\"x\"\n"},
		{name: "number literal kept", in: `1.50e+3`, want: "1.50e+3\n"},
		{name: "null", in: ` null `, want: "null\n"},
		{name: "empty object", in: `{}`, want: "{}\n"},
		{name: "empty array", in: `[ ]`, want: "[]\n"},
		{name: "key order kept", in: `{"z":1,"a":{"y":true,"b":[]}}`, want: "{\n  \"z\": 1,\n  \"a\": {\n    \"y\": true,\n    \"b\": []\n  }\n}\n"},
		{name: "array of objects", in: `[{"a":"<&>"},2]`, want: "[\n  {\n    \"a\": \"<&>\"\n  },\n  2\n]\n"},
		{name: "duplicate keys kept in place", in: `{"a":1,"a":2}`, want: "{\n  \"a\": 1,\n  \"a\": 2\n}\n"},
		{name: "unicode escape decoded", in: `{"k":"é"}`, want: "{\n  \"k\": \"é\"\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := parseJSON([]byte(tc.in))
			if err != nil {
				t.Fatalf("parseJSON: %v", err)
			}
			got, err := marshalNode(n)
			if err != nil {
				t.Fatalf("marshalNode: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("round trip = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseJSONErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{name: "empty", in: ""},
		{name: "trailing value", in: `{} {}`},
		{name: "trailing garbage", in: `1 x`},
		{name: "unterminated object", in: `{"a":1`},
		{name: "missing value", in: `{"a":}`},
		{name: "too deep", in: strings.Repeat("[", maxJSONDepth+1) + strings.Repeat("]", maxJSONDepth+1), want: errTooDeep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseJSON([]byte(tc.in))
			if err == nil {
				t.Fatalf("parseJSON(%q) succeeded", tc.in)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("parseJSON error = %v, want %v", err, tc.want)
			}
		})
	}
	deepest := strings.Repeat("[", maxJSONDepth) + strings.Repeat("]", maxJSONDepth)
	if _, err := parseJSON([]byte(deepest)); err != nil {
		t.Fatalf("parseJSON at the depth limit: %v", err)
	}
}

func TestNodeGetSet(t *testing.T) {
	n, err := parseJSON([]byte(`{"a":1,"b":2,"a":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.get("a"); got == nil || got.scalar != json.Number("3") {
		t.Fatalf("get(a) = %+v, want the last member", got)
	}
	if n.get("missing") != nil {
		t.Fatal("get(missing) is not nil")
	}
	n.set("a", &node{kind: kindScalar, scalar: "x"})
	n.set("c", &node{kind: kindScalar, scalar: false})
	got, err := marshalNode(n)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"a\": \"x\",\n  \"c\": false\n}\n"
	if string(got) != want {
		t.Fatalf("after set = %q, want %q", got, want)
	}
}

func TestToNodeRejectsUnencodable(t *testing.T) {
	if _, err := toNode(func() {}); err == nil {
		t.Fatal("toNode accepted a func")
	}
	var buf bytes.Buffer
	if err := (&node{kind: kindObject, members: []member{{key: "f", value: &node{kind: kindScalar, scalar: make(chan int)}}}}).encode(&buf, ""); err == nil {
		t.Fatal("encode accepted a channel in an object")
	}
	if err := (&node{kind: kindArray, items: []*node{{kind: kindScalar, scalar: make(chan int)}}}).encode(&buf, ""); err == nil {
		t.Fatal("encode accepted a channel in an array")
	}
}

func FuzzParseJSON(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `{"a":[1,{"b":null}],"a":"x"}`, `"\ud800"`, `1e999`, `{"a":1}x`, "[[[[", `{"statusLine":{"type":"command"}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := parseJSON(data)
		if err != nil {
			if json.Valid(data) && !errors.Is(err, errTooDeep) {
				t.Fatalf("parseJSON rejected valid JSON %q: %v", data, err)
			}
			return
		}
		if !json.Valid(data) {
			t.Fatalf("parseJSON accepted invalid JSON %q", data)
		}
		out, err := marshalNode(n)
		if err != nil {
			t.Fatalf("marshalNode: %v", err)
		}
		if !json.Valid(out) {
			t.Fatalf("marshalNode produced invalid JSON %q", out)
		}
		if !reflect.DeepEqual(decodeAny(t, data), decodeAny(t, out)) {
			t.Fatalf("round trip changed the value: %q -> %q", data, out)
		}
		again, err := parseJSON(out)
		if err != nil {
			t.Fatalf("parseJSON of its own output: %v", err)
		}
		out2, err := marshalNode(again)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("encoding is not stable: %q vs %q (%v)", out, out2, err)
		}
	})
}

func decodeAny(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return v
}
