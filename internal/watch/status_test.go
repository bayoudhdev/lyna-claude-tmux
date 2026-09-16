package watch

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// nul joins records the way git -z terminates them.
func nul(records ...string) []byte {
	return []byte(strings.Join(records, "\x00") + "\x00")
}

const (
	hashA = "422c2b7ab3b3c668038da977e4e93a5fc623169c"
	zero  = "0000000000000000000000000000000000000000"
)

func TestParseStatus(t *testing.T) {
	cases := []struct {
		name    string
		in      []byte
		want    Status
		wantErr bool
	}{
		{
			name: "real output with rename, spaces and a tab",
			in: nul(
				"# branch.oid bcac0ad9076fa982d7eacc828f7ca40b9dcccb74",
				"# branch.head main",
				"1 .M N... 100644 100644 100644 "+hashA+" "+hashA+" keep.txt",
				"2 R. N... 100644 100644 100644 "+hashA+" "+hashA+" R100 new name.txt", "old.txt",
				"1 A. N... 000000 100644 100644 "+zero+" "+hashA+" tab\tx.txt",
				"? untracked.txt",
			),
			want: Status{
				Branch: Branch{OID: "bcac0ad9076fa982d7eacc828f7ca40b9dcccb74", Head: "main"},
				Entries: []Entry{
					{Kind: KindChanged, Index: '.', Worktree: 'M', Path: "keep.txt"},
					{Kind: KindRenamed, Index: 'R', Worktree: '.', Path: "new name.txt", OrigPath: "old.txt"},
					{Kind: KindChanged, Index: 'A', Worktree: '.', Path: "tab\tx.txt"},
					{Kind: KindUntracked, Index: '?', Worktree: '?', Path: "untracked.txt"},
				},
			},
		},
		{
			name: "initial commit and upstream with ahead behind",
			in: nul(
				"# branch.oid (initial)",
				"# branch.head feature/x",
				"# branch.upstream origin/feature/x",
				"# branch.ab +3 -12",
				"# stash 2",
			),
			want: Status{Branch: Branch{Initial: true, Head: "feature/x", Upstream: "origin/feature/x", AheadBehind: true, Ahead: 3, Behind: 12}},
		},
		{
			name: "detached with copy, conflict and ignored",
			in: nul(
				"# branch.oid "+hashA,
				"# branch.head (detached)",
				"2 C. N... 100644 100644 100644 "+hashA+" "+hashA+" C75 copy.go", "orig.go",
				"u UU N... 100644 100644 100644 100644 "+hashA+" "+hashA+" "+hashA+" merge me.txt",
				"! build/out.bin",
			),
			want: Status{
				Branch: Branch{OID: hashA, Detached: true},
				Entries: []Entry{
					{Kind: KindCopied, Index: 'C', Worktree: '.', Path: "copy.go", OrigPath: "orig.go"},
					{Kind: KindUnmerged, Index: 'U', Worktree: 'U', Path: "merge me.txt"},
					{Kind: KindIgnored, Index: '!', Worktree: '!', Path: "build/out.bin"},
				},
			},
		},
		{
			name: "newline and escape bytes stay in the path",
			in:   nul("? a\nb\x1b[31m.txt"),
			want: Status{Entries: []Entry{{Kind: KindUntracked, Index: '?', Worktree: '?', Path: "a\nb\x1b[31m.txt"}}},
		},
		{name: "empty output", in: nil, want: Status{}},
		{name: "missing final terminator", in: []byte("? a.txt"), want: Status{Entries: []Entry{{Kind: KindUntracked, Index: '?', Worktree: '?', Path: "a.txt"}}}},
		{name: "empty record", in: []byte("\x00\x00"), wantErr: true},
		{name: "unknown record type", in: nul("3 XY path"), wantErr: true},
		{name: "short ordinary record", in: nul("1 .M N... 100644"), wantErr: true},
		{name: "bad status code", in: nul("1 xM N... 100644 100644 100644 " + hashA + " " + hashA + " a"), wantErr: true},
		{name: "long status code", in: nul("1 .MM N... 100644 100644 100644 " + hashA + " " + hashA + " a"), wantErr: true},
		{name: "rename without original", in: []byte("2 R. N... 100644 100644 100644 " + hashA + " " + hashA + " R100 new"), wantErr: true},
		{name: "rename with bad score", in: nul("2 R. N... 100644 100644 100644 "+hashA+" "+hashA+" X100 new", "old"), wantErr: true},
		{name: "rename with empty score", in: nul("2 R. N... 100644 100644 100644 "+hashA+" "+hashA+"  new", "old"), wantErr: true},
		{name: "absolute path", in: nul("? /etc/passwd"), wantErr: true},
		{name: "parent path", in: nul("? a/../../x"), wantErr: true},
		{name: "rename from parent path", in: nul("2 R. N... 100644 100644 100644 "+hashA+" "+hashA+" R100 new", "../old"), wantErr: true},
		{name: "empty untracked path", in: nul("? "), wantErr: true},
		{name: "untracked without space", in: nul("?a"), wantErr: true},
		{name: "bad oid", in: nul("# branch.oid xyz"), wantErr: true},
		{name: "empty head", in: nul("# branch.head "), wantErr: true},
		{name: "bad ahead behind", in: nul("# branch.ab 3 12"), wantErr: true},
		{name: "negative ahead", in: nul("# branch.ab +-1 -2"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStatus(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseStatus error = %v, want ErrMalformed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStatus: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseStatus =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestParseNumstat(t *testing.T) {
	cases := []struct {
		name    string
		in      []byte
		want    []NumStat
		wantErr bool
	}{
		{
			name: "real staged output with rename and tab path",
			in:   []byte("0\t0\t\x00old.txt\x00new name.txt\x001\t0\ttab\tx.txt\x00"),
			want: []NumStat{
				{Path: "new name.txt", OrigPath: "old.txt"},
				{Path: "tab\tx.txt", Added: 1},
			},
		},
		{
			name: "binary and counts",
			in:   nul("-\t-\timg.png", "120\t7\tsrc/main.go"),
			want: []NumStat{{Path: "img.png", Binary: true}, {Path: "src/main.go", Added: 120, Deleted: 7}},
		},
		{name: "empty", in: nil, want: nil},
		{name: "missing tab", in: nul("1 2 a"), wantErr: true},
		{name: "bad count", in: nul("x\t2\ta"), wantErr: true},
		{name: "half binary", in: nul("-\t2\ta"), wantErr: true},
		{name: "negative count", in: nul("-1\t2\ta"), wantErr: true},
		{name: "rename cut short", in: []byte("0\t0\t\x00old\x00"), wantErr: true},
		{name: "absolute path", in: nul("1\t1\t/abs"), wantErr: true},
		{name: "rename from outside", in: nul("0\t0\t", "../x", "y"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNumstat(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseNumstat error = %v, want ErrMalformed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNumstat: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseNumstat = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func validEntryPath(t *testing.T, p string) {
	t.Helper()
	if p == "" || p[0] == '/' || strings.Contains(p, "\x00") {
		t.Fatalf("unsafe path accepted: %q", p)
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == ".." {
			t.Fatalf("parent component accepted: %q", p)
		}
	}
}

func FuzzParseStatus(f *testing.F) {
	f.Add(nul("# branch.oid (initial)", "# branch.head main", "# branch.ab +1 -2", "? a b"))
	f.Add(nul("2 R. N... 100644 100644 100644 "+hashA+" "+hashA+" R100 new", "old"))
	f.Add(nul("u UU N... 100644 100644 100644 100644 " + hashA + " " + hashA + " " + hashA + " m"))
	f.Add([]byte("2 R. N... 1 1 1 a b R1 x"))
	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := ParseStatus(data)
		if err != nil {
			return
		}
		if st.Branch.Ahead < 0 || st.Branch.Behind < 0 {
			t.Fatalf("negative ahead/behind: %+v", st.Branch)
		}
		for _, e := range st.Entries {
			validEntryPath(t, e.Path)
			if e.Kind == KindRenamed || e.Kind == KindCopied {
				validEntryPath(t, e.OrigPath)
			}
			if !statusCode(e.Index) && e.Index != '?' && e.Index != '!' {
				t.Fatalf("invalid index code %q", e.Index)
			}
		}
	})
}

func FuzzParseNumstat(f *testing.F) {
	f.Add([]byte("0\t0\t\x00old.txt\x00new name.txt\x001\t0\ttab\tx.txt\x00"))
	f.Add(nul("-\t-\timg.png"))
	f.Fuzz(func(t *testing.T, data []byte) {
		list, err := ParseNumstat(data)
		if err != nil {
			return
		}
		for _, ns := range list {
			validEntryPath(t, ns.Path)
			if ns.OrigPath != "" {
				validEntryPath(t, ns.OrigPath)
			}
			if ns.Added < 0 || ns.Deleted < 0 || (ns.Binary && (ns.Added != 0 || ns.Deleted != 0)) {
				t.Fatalf("invalid counts: %+v", ns)
			}
		}
	})
}
