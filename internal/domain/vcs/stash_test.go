package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	stashOID  = "46ebe2cdfeadaf5bf1f666da95ba5ce87bb264a3"
	stashBase = "9882110bfb11035d932393a7e2d9068645245d66"
	stashTree = "eff8f3eb5cf856aa9f8b41b5ca48819af37d5414"
)

// stashRecord writes the five fields StashFormat gives one stash.
func stashRecord(ref, oid, parents, created, subject string) []string {
	return []string{ref, oid, parents, created, subject}
}

func TestParseStashes(t *testing.T) {
	both := stashBase + " " + stashTree
	cases := []struct {
		name    string
		fields  []string
		want    []Stash
		wantErr bool
	}{
		{name: "no stash at all"},
		{
			name:   "a stash pushed with a message",
			fields: stashRecord("stash@{0}", stashOID, both, "1789863043", "On main: a message of my own"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase,
				Created: time.Unix(1789863043, 0).UTC(), Branch: "main", Message: "a message of my own",
			}},
		},
		{
			name:   "a stash pushed with none",
			fields: stashRecord("stash@{0}", stashOID, both, "1", "WIP on main: 9882110 first commit subject"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase, Created: time.Unix(1, 0).UTC(),
				Branch: "main", Message: "9882110 first commit subject", WIP: true,
			}},
		},
		{
			name:   "a stash carrying untracked files",
			fields: stashRecord("stash@{0}", stashOID, both+" "+stashOID, "1", "On main: with an untracked file"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase, Created: time.Unix(1, 0).UTC(),
				Branch: "main", Message: "with an untracked file", Untracked: true,
			}},
		},
		{
			name:   "a stash made on no branch",
			fields: stashRecord("stash@{0}", stashOID, both, "1", "On (no branch): on a detached head"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase,
				Created: time.Unix(1, 0).UTC(), Message: "on a detached head",
			}},
		},
		{
			name:   "a branch whose name holds a slash",
			fields: stashRecord("stash@{0}", stashOID, both, "1", "On feat/deep-name: on a branch with a slash"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase, Created: time.Unix(1, 0).UTC(),
				Branch: "feat/deep-name", Message: "on a branch with a slash",
			}},
		},
		{
			name:   "a subject of another shape is kept whole",
			fields: stashRecord("stash@{0}", stashOID, both, "1", "written by hand"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase,
				Created: time.Unix(1, 0).UTC(), Message: "written by hand",
			}},
		},
		{
			name:   "a message with no branch before it",
			fields: stashRecord("stash@{0}", stashOID, both, "1", "On main"),
			want: []Stash{{
				Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase,
				Created: time.Unix(1, 0).UTC(), Message: "On main",
			}},
		},
		{
			name: "the list counts from the entry pushed last",
			fields: append(
				stashRecord("stash@{0}", stashOID, both, "2", "On main: newer"),
				stashRecord("stash@{1}", stashBase, both, "1", "On main: older")...,
			),
			want: []Stash{
				{Index: 0, Ref: "stash@{0}", OID: stashOID, Base: stashBase, Created: time.Unix(2, 0).UTC(), Branch: "main", Message: "newer"},
				{Index: 1, Ref: "stash@{1}", OID: stashBase, Base: stashBase, Created: time.Unix(1, 0).UTC(), Branch: "main", Message: "older"},
			},
		},
		{
			name:    "a record cut short",
			fields:  []string{"stash@{0}", stashOID, both, "1"},
			wantErr: true,
		},
		{
			name:    "a reference of another shape",
			fields:  stashRecord("refs/stash", stashOID, both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "an index that is not a number",
			fields:  stashRecord("stash@{last}", stashOID, both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "an index padded with zeroes",
			fields:  stashRecord("stash@{00}", stashOID, both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "an index with a sign",
			fields:  stashRecord("stash@{+1}", stashOID, both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "an index counting backwards",
			fields:  stashRecord("stash@{-1}", stashOID, both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "a list that does not start at zero",
			fields:  stashRecord("stash@{1}", stashOID, both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name: "a list that skips an entry",
			fields: append(
				stashRecord("stash@{0}", stashOID, both, "1", "On main: x"),
				stashRecord("stash@{2}", stashOID, both, "1", "On main: y")...,
			),
			wantErr: true,
		},
		{
			name:    "an object name that is not one",
			fields:  stashRecord("stash@{0}", "not-a-hash", both, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "a stash of one parent",
			fields:  stashRecord("stash@{0}", stashOID, stashBase, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "a stash of four parents",
			fields:  stashRecord("stash@{0}", stashOID, both+" "+stashOID+" "+stashBase, "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "a parent that is not an object name",
			fields:  stashRecord("stash@{0}", stashOID, stashBase+" nothex", "1", "On main: x"),
			wantErr: true,
		},
		{
			name:    "a date that is not a number",
			fields:  stashRecord("stash@{0}", stashOID, both, "yesterday", "On main: x"),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStashes(nul(tc.fields...))
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseStashes() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseStashes() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseStashes() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseStashes() read %d stashes, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("stash %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseStashesReadsGitItself reads a real stash list: one pushed with a
// message, one without, one carrying untracked files, one made on a detached
// HEAD and one on a branch whose name holds a slash.
func TestParseStashesReadsGitItself(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stash.z"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseStashes(data)
	if err != nil {
		t.Fatalf("ParseStashes() error = %v", err)
	}
	type state struct {
		ref, branch, message string
		wip, untracked       bool
	}
	want := []state{
		{ref: "stash@{0}", branch: "feat/deep-name", message: "on a branch with a slash"},
		{ref: "stash@{1}", message: "on a detached head"},
		{ref: "stash@{2}", branch: "side", message: "on another branch"},
		{ref: "stash@{3}", branch: "main", message: "with an untracked file", untracked: true},
		{ref: "stash@{4}", branch: "main", message: "9882110 first commit subject", wip: true},
		{ref: "stash@{5}", branch: "main", message: "a message of my own"},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseStashes() read %d stashes, want %d", len(got), len(want))
	}
	for i, s := range got {
		gotState := state{ref: s.Ref, branch: s.Branch, message: s.Message, wip: s.WIP, untracked: s.Untracked}
		if gotState != want[i] {
			t.Fatalf("stash %d = %+v, want %+v", i, gotState, want[i])
		}
		if s.Index != i || !isHex(s.OID) || !isHex(s.Base) || s.Created.IsZero() {
			t.Fatalf("stash %d = %+v, want a commit, a base and a date", i, s)
		}
	}
}

func TestParseStashesCapsTheList(t *testing.T) {
	var fields []string
	for i := range MaxStashes + 10 {
		fields = append(fields, stashRecord(
			"stash@{"+itoa(i)+"}", stashOID, stashBase+" "+stashTree, "1", "On main: x",
		)...)
	}
	got, err := ParseStashes(nul(fields...))
	if err != nil {
		t.Fatalf("ParseStashes() error = %v", err)
	}
	if len(got) != MaxStashes {
		t.Fatalf("ParseStashes() read %d stashes, want the cap %d", len(got), MaxStashes)
	}
}

func FuzzParseStashes(f *testing.F) {
	both := stashBase + " " + stashTree
	f.Add(nul(stashRecord("stash@{0}", stashOID, both, "1789863043", "On main: a message of my own")...))
	f.Add(nul(stashRecord("stash@{0}", stashOID, both+" "+stashOID, "1", "WIP on (no branch): 9882110 s")...))
	f.Add([]byte("stash@{0}\x00\x00\x00\x00\x00"))
	f.Add([]byte("stash@{00}\x000000\x000000 0000\x00\x000"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseStashes(data)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseStashes() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if len(got) > MaxStashes {
			t.Fatalf("ParseStashes() read %d stashes, past the cap %d", len(got), MaxStashes)
		}
		for i, s := range got {
			if s.Index != i {
				t.Fatalf("stash %+v sits in position %d", s, i)
			}
			if !isHex(s.OID) || !isHex(s.Base) {
				t.Fatalf("stash %+v does not name its commits", s)
			}
			if s.Ref != "stash@{"+itoa(s.Index)+"}" {
				t.Fatalf("stash %+v is not named by its index", s)
			}
		}
	})
}

// itoa keeps the test records readable where a stash index is written into a
// reference.
func itoa(i int) string { return strconv.Itoa(i) }
