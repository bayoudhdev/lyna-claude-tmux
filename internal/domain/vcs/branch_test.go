package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const branchOID = "3099289da1eb0854a12df374c11a7260b655acd5"

// branchRecord writes one record the way BranchFormat does.
func branchRecord(fields ...string) string { return strings.Join(fields, "\x00") }

func TestParseBranches(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		want    []LocalBranch
		wantErr bool
	}{
		{name: "no branch at all", data: ""},
		{
			name: "a branch that tracks nothing",
			data: branchRecord("refs/heads/local-only", branchOID, " ", "", "", "1789860331", "a branch with no upstream") + "\n",
			want: []LocalBranch{{
				Name: "local-only", Ref: "refs/heads/local-only", OID: branchOID,
				Tip: time.Unix(1789860331, 0).UTC(), Subject: "a branch with no upstream",
			}},
		},
		{
			name: "the branch HEAD is on",
			data: branchRecord("refs/heads/main", branchOID, "*", "refs/remotes/origin/main", "", "1789860345", "first commit") + "\n",
			want: []LocalBranch{{
				Name: "main", Ref: "refs/heads/main", OID: branchOID, Head: true,
				Upstream: "refs/remotes/origin/main", Tip: time.Unix(1789860345, 0).UTC(), Subject: "first commit",
			}},
		},
		{
			name: "ahead, behind and both ways",
			data: branchRecord("refs/heads/a", branchOID, " ", "refs/remotes/origin/a", "ahead 2", "1", "s") + "\n" +
				branchRecord("refs/heads/b", branchOID, " ", "refs/remotes/origin/b", "behind 3", "1", "s") + "\n" +
				branchRecord("refs/heads/c", branchOID, " ", "refs/remotes/origin/c", "ahead 1, behind 4", "1", "s") + "\n",
			want: []LocalBranch{
				{Name: "a", Ref: "refs/heads/a", OID: branchOID, Upstream: "refs/remotes/origin/a", Ahead: 2, Tip: time.Unix(1, 0).UTC(), Subject: "s"},
				{Name: "b", Ref: "refs/heads/b", OID: branchOID, Upstream: "refs/remotes/origin/b", Behind: 3, Tip: time.Unix(1, 0).UTC(), Subject: "s"},
				{
					Name: "c", Ref: "refs/heads/c", OID: branchOID, Upstream: "refs/remotes/origin/c",
					Ahead: 1, Behind: 4, Tip: time.Unix(1, 0).UTC(), Subject: "s",
				},
			},
		},
		{
			name: "an upstream that was removed",
			data: branchRecord("refs/heads/gone", branchOID, " ", "refs/remotes/origin/gone", "gone", "1", "s") + "\n",
			want: []LocalBranch{{
				Name: "gone", Ref: "refs/heads/gone", OID: branchOID,
				Upstream: "refs/remotes/origin/gone", Gone: true, Tip: time.Unix(1, 0).UTC(), Subject: "s",
			}},
		},
		{
			name: "a branch whose name holds slashes",
			data: branchRecord("refs/heads/feat/git-workstation", branchOID, " ", "", "", "1", "s") + "\n",
			want: []LocalBranch{{
				Name: "feat/git-workstation", Ref: "refs/heads/feat/git-workstation",
				OID: branchOID, Tip: time.Unix(1, 0).UTC(), Subject: "s",
			}},
		},
		{
			name: "a subject with no commit date, which a broken ref leaves",
			data: branchRecord("refs/heads/x", branchOID, " ", "", "", "", "") + "\n",
			want: []LocalBranch{{Name: "x", Ref: "refs/heads/x", OID: branchOID}},
		},
		{
			name:    "a record with a field missing",
			data:    branchRecord("refs/heads/x", branchOID, " ", "", "", "1") + "\n",
			wantErr: true,
		},
		{
			name:    "a ref outside the branch namespace",
			data:    branchRecord("refs/tags/v1.1.0", branchOID, " ", "", "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a ref that is only its namespace",
			data:    branchRecord("refs/heads/", branchOID, " ", "", "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "an object name that is not one",
			data:    branchRecord("refs/heads/x", "not-a-hash", " ", "", "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a date that is not a number",
			data:    branchRecord("refs/heads/x", branchOID, " ", "", "", "yesterday", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a track this does not know",
			data:    branchRecord("refs/heads/x", branchOID, " ", "u", "sideways 2", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a track without an upstream",
			data:    branchRecord("refs/heads/x", branchOID, " ", "", "ahead 1", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a track with no count",
			data:    branchRecord("refs/heads/x", branchOID, " ", "u", "ahead", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a track counting backwards",
			data:    branchRecord("refs/heads/x", branchOID, " ", "u", "ahead -1", "1", "s") + "\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBranches([]byte(tc.data))
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseBranches() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseBranches() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseBranches() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseBranches() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("branch %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseBranchesReadsGitItself reads a real `git for-each-ref` over a
// project whose branches are ahead, behind, both, without an upstream and
// with one that was deleted on the remote.
func TestParseBranchesReadsGitItself(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "branches.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseBranches(data)
	if err != nil {
		t.Fatalf("ParseBranches() error = %v", err)
	}
	type state struct {
		name           string
		head, gone     bool
		ahead, behind  int
		upstream, subj string
	}
	want := []state{
		{name: "ahead", head: true, ahead: 1, behind: 1, upstream: "refs/remotes/origin/ahead", subj: "now both ways"},
		{name: "diverged", ahead: 1, upstream: "refs/remotes/origin/main", subj: "diverged here"},
		{name: "gone", gone: true, upstream: "refs/remotes/origin/gone", subj: "one more, not pushed"},
		{name: "local-only", subj: "a branch with no upstream"},
		{name: "main", ahead: 1, upstream: "refs/remotes/origin/main", subj: "local only commit"},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseBranches() read %d branches, want %d", len(got), len(want))
	}
	for i, b := range got {
		gotState := state{
			name: b.Name, head: b.Head, gone: b.Gone, ahead: b.Ahead,
			behind: b.Behind, upstream: b.Upstream, subj: b.Subject,
		}
		if gotState != want[i] {
			t.Fatalf("branch %d = %+v, want %+v", i, gotState, want[i])
		}
		if !isHex(b.OID) || b.Tip.IsZero() {
			t.Fatalf("branch %s = %+v, want a commit and a date", b.Name, b)
		}
	}
}

func TestParseBranchesCapsTheList(t *testing.T) {
	var b strings.Builder
	for range MaxBranches + 10 {
		b.WriteString(branchRecord("refs/heads/x", branchOID, " ", "", "", "1", "s") + "\n")
	}
	got, err := ParseBranches([]byte(b.String()))
	if err != nil {
		t.Fatalf("ParseBranches() error = %v", err)
	}
	if len(got) != MaxBranches {
		t.Fatalf("ParseBranches() read %d branches, want the cap %d", len(got), MaxBranches)
	}
}

func FuzzParseBranches(f *testing.F) {
	f.Add(branchRecord("refs/heads/main", branchOID, "*", "refs/remotes/origin/main", "ahead 1, behind 2", "1789860345", "a subject") + "\n")
	f.Add(branchRecord("refs/heads/gone", branchOID, " ", "u", "gone", "1", "") + "\n")
	f.Add(branchRecord("refs/heads/x", branchOID, " ", "", "", "", "") + "\n")
	f.Add("refs/heads/x\x00\x00\x00\x00\x00\x00\n\n")
	f.Add("refs/heads/0\x000\x00\x00\x00ahead 1\x00\x00")
	f.Fuzz(func(t *testing.T, data string) {
		got, err := ParseBranches([]byte(data))
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseBranches() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if len(got) > MaxBranches {
			t.Fatalf("ParseBranches() read %d branches, past the cap %d", len(got), MaxBranches)
		}
		for _, b := range got {
			if b.Name == "" || headsPrefix+b.Name != b.Ref {
				t.Fatalf("branch %+v is not named by its ref", b)
			}
			if !isHex(b.OID) {
				t.Fatalf("branch object name %q is not one", b.OID)
			}
			if b.Ahead < 0 || b.Behind < 0 {
				t.Fatalf("branch %+v counts backwards", b)
			}
			if b.Upstream == "" && (b.Gone || b.Ahead != 0 || b.Behind != 0) {
				t.Fatalf("branch %+v is measured against an upstream it has not got", b)
			}
		}
	})
}

// TestValidateRefName holds the rule to what `git check-ref-format --branch`
// answers, checked against git itself, plus the two names this refuses on
// purpose: one git would read as an option, one it would resolve elsewhere.
func TestValidateRefName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "main", want: true},
		{name: "feat/git-workstation", want: true},
		{name: "a.b", want: true},
		{name: "release/2026.09", want: true},
		{name: "été", want: true},
		{name: ""},
		{name: "-x"},
		{name: "--upload-pack=x"},
		{name: "@"},
		{name: "HEAD"},
		{name: "/x"},
		{name: "x/"},
		{name: "a//b"},
		{name: "x."},
		{name: "a..b"},
		{name: "a@{0}"},
		{name: "a b"},
		{name: "a~1"},
		{name: "a^"},
		{name: "a:b"},
		{name: "a?"},
		{name: "a*"},
		{name: "a["},
		{name: `a\b`},
		{name: "a\tb"},
		{name: "a\nb"},
		{name: ".hidden/x"},
		{name: "feat/.x"},
		{name: "x.lock"},
		{name: "feat/x.lock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRefName(tc.name)
			if tc.want && err != nil {
				t.Fatalf("ValidateRefName(%q) = %v, want the name accepted", tc.name, err)
			}
			if !tc.want {
				if err == nil {
					t.Fatalf("ValidateRefName(%q) accepted it", tc.name)
				}
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ValidateRefName(%q) error = %v, want %v", tc.name, err, ErrMalformed)
				}
			}
		})
	}
}

func FuzzValidateRefName(f *testing.F) {
	f.Add("main")
	f.Add("feat/x.lock")
	f.Add("-x")
	f.Fuzz(func(t *testing.T, name string) {
		if err := ValidateRefName(name); err != nil {
			return
		}
		// An accepted name is safe to hand to a command: never an option,
		// never a path of its own, never a revision of another shape.
		if name == "" || name[0] == '-' || name[0] == '/' || name[len(name)-1] == '/' {
			t.Fatalf("ValidateRefName(%q) accepted a name git reads as something else", name)
		}
		for _, bad := range []string{"..", "@{", "//", " ", "~", "^", ":", "?", "*", "[", `\`} {
			if strings.Contains(name, bad) {
				t.Fatalf("ValidateRefName(%q) accepted a name holding %q", name, bad)
			}
		}
		for _, r := range name {
			if r < ' ' || r == 0x7f {
				t.Fatalf("ValidateRefName(%q) accepted a control character", name)
			}
		}
	})
}
