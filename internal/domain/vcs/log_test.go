package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	commitA = "95f1ac295145520993c7df2d5824fa06dce9cd75"
	commitB = "3099289da1eb0854a12df374c11a7260b655acd5"
	commitC = "464636bab23cfea7dfb2c3698f7af11787d520c3"
)

// commitRecord writes the seven fields LogFormat gives one commit.
func commitRecord(oid, parents, author, authored, committed, refs, subject string) []string {
	return []string{oid, parents, author, authored, committed, refs, subject}
}

func TestParseLog(t *testing.T) {
	cases := []struct {
		name    string
		fields  []string
		want    []Commit
		wantErr bool
	}{
		{name: "an empty history", fields: nil},
		{
			name:   "one commit with no parent",
			fields: commitRecord(commitA, "", "Ada", "1789861740", "1789861741", "", "the first commit"),
			want: []Commit{{
				OID: commitA, Author: "Ada", Authored: time.Unix(1789861740, 0).UTC(),
				Committed: time.Unix(1789861741, 0).UTC(), Subject: "the first commit",
			}},
		},
		{
			name:   "a merge of two parents",
			fields: commitRecord(commitA, commitB+" "+commitC, "Ada", "1", "1", "", "a merge commit"),
			want: []Commit{{
				OID: commitA, Parents: []string{commitB, commitC}, Author: "Ada",
				Authored: time.Unix(1, 0).UTC(), Committed: time.Unix(1, 0).UTC(), Subject: "a merge commit",
			}},
		},
		{
			name:   "the branch HEAD is on",
			fields: commitRecord(commitA, "", "Ada", "1", "1", "HEAD -> refs/heads/main", "s"),
			want: []Commit{{
				OID: commitA, Author: "Ada", Authored: time.Unix(1, 0).UTC(), Committed: time.Unix(1, 0).UTC(),
				Refs:    []Ref{{Name: "main", Full: "refs/heads/main", Kind: RefBranch, Head: true}},
				Subject: "s",
			}},
		},
		{
			name:   "a tag, a remote branch and a branch at once",
			fields: commitRecord(commitA, "", "Ada", "1", "1", "tag: refs/tags/v1.1.0, refs/remotes/origin/main, refs/heads/feat/git", "s"),
			want: []Commit{{
				OID: commitA, Author: "Ada", Authored: time.Unix(1, 0).UTC(), Committed: time.Unix(1, 0).UTC(),
				Refs: []Ref{
					{Name: "v1.1.0", Full: "refs/tags/v1.1.0", Kind: RefTag},
					{Name: "origin/main", Full: "refs/remotes/origin/main", Kind: RefRemote},
					{Name: "feat/git", Full: "refs/heads/feat/git", Kind: RefBranch},
				},
				Subject: "s",
			}},
		},
		{
			name:   "a detached HEAD",
			fields: commitRecord(commitA, "", "Ada", "1", "1", "HEAD", "s"),
			want: []Commit{{
				OID: commitA, Author: "Ada", Authored: time.Unix(1, 0).UTC(), Committed: time.Unix(1, 0).UTC(),
				Refs: []Ref{{Name: "HEAD", Full: "HEAD", Kind: RefOther, Head: true}}, Subject: "s",
			}},
		},
		{
			name:   "a ref of another namespace",
			fields: commitRecord(commitA, "", "Ada", "1", "1", "refs/stash", "s"),
			want: []Commit{{
				OID: commitA, Author: "Ada", Authored: time.Unix(1, 0).UTC(), Committed: time.Unix(1, 0).UTC(),
				Refs: []Ref{{Name: "refs/stash", Full: "refs/stash", Kind: RefOther}}, Subject: "s",
			}},
		},
		{
			name:   "an empty subject and no dates",
			fields: commitRecord(commitA, "", "", "", "", "", ""),
			want:   []Commit{{OID: commitA}},
		},
		{
			name:    "a record cut short",
			fields:  []string{commitA, "", "Ada", "1", "1", ""},
			wantErr: true,
		},
		{
			name:    "an object name that is not one",
			fields:  commitRecord("nothex", "", "Ada", "1", "1", "", "s"),
			wantErr: true,
		},
		{
			name:    "a parent that is not an object name",
			fields:  commitRecord(commitA, "nothex", "Ada", "1", "1", "", "s"),
			wantErr: true,
		},
		{
			name:    "a date that is not a number",
			fields:  commitRecord(commitA, "", "Ada", "yesterday", "1", "", "s"),
			wantErr: true,
		},
		{
			name:    "a decoration with an empty ref",
			fields:  commitRecord(commitA, "", "Ada", "1", "1", "refs/heads/main, ", "s"),
			wantErr: true,
		},
		{
			name:    "a ref that names nothing",
			fields:  commitRecord(commitA, "", "Ada", "1", "1", "refs/heads/", "s"),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLog(nul(tc.fields...))
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseLog() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseLog() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseLog() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseLog() read %d commits, want %d", len(got), len(tc.want))
			}
			for i := range got {
				assertCommit(t, got[i], tc.want[i])
			}
		})
	}
}

func assertCommit(t *testing.T, got, want Commit) {
	t.Helper()
	if got.OID != want.OID || got.Author != want.Author || got.Subject != want.Subject ||
		!got.Authored.Equal(want.Authored) || !got.Committed.Equal(want.Committed) {
		t.Fatalf("commit = %+v, want %+v", got, want)
	}
	if len(got.Parents) != len(want.Parents) {
		t.Fatalf("commit parents = %v, want %v", got.Parents, want.Parents)
	}
	for i := range got.Parents {
		if got.Parents[i] != want.Parents[i] {
			t.Fatalf("commit parents = %v, want %v", got.Parents, want.Parents)
		}
	}
	if len(got.Refs) != len(want.Refs) {
		t.Fatalf("commit refs = %+v, want %+v", got.Refs, want.Refs)
	}
	for i := range got.Refs {
		if got.Refs[i] != want.Refs[i] {
			t.Fatalf("ref %d = %+v, want %+v", i, got.Refs[i], want.Refs[i])
		}
	}
}

// TestParseLogReadsGitItself reads a real history: a merge, a tag, a remote
// branch, a branch with no upstream and the first commit of the project.
func TestParseLogReadsGitItself(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "log.z"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLog(data)
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	if len(got) < 5 {
		t.Fatalf("ParseLog() read %d commits, want the whole history", len(got))
	}
	head := got[0]
	if !head.Merge() || len(head.Parents) != 2 {
		t.Fatalf("the first commit read is %+v, want the merge at the top", head)
	}
	if len(head.Refs) != 1 || head.Refs[0].Name != "ahead" || head.Refs[0].Kind != RefBranch || !head.Refs[0].Head {
		t.Fatalf("the merge carries %+v, want the branch HEAD is on", head.Refs)
	}
	if head.Short() != head.OID[:7] {
		t.Fatalf("Short() = %q", head.Short())
	}
	var tags, remotes int
	for _, c := range got {
		for _, r := range c.Refs {
			switch r.Kind {
			case RefTag:
				tags++
			case RefRemote:
				remotes++
			case RefBranch, RefOther:
			}
		}
		if c.Authored.IsZero() || c.Author == "" {
			t.Fatalf("commit %s has no author: %+v", c.Short(), c)
		}
	}
	if tags == 0 || remotes == 0 {
		t.Fatalf("the history read holds %d tags and %d remote branches, want some of each", tags, remotes)
	}
	// The whole history is read, so it closes on itself: every parent named is
	// a commit read, and exactly one commit has no parent at all.
	seen := make(map[string]bool, len(got))
	for _, c := range got {
		seen[c.OID] = true
	}
	var roots int
	for _, c := range got {
		if len(c.Parents) == 0 {
			roots++
		}
		for _, p := range c.Parents {
			if !seen[p] {
				t.Fatalf("commit %s names the parent %s, which was not read", c.Short(), p)
			}
		}
	}
	if roots != 1 {
		t.Fatalf("the history read holds %d commits with no parent, want the first commit alone", roots)
	}
}

func TestParseLogCapsTheHistory(t *testing.T) {
	var fields []string
	for range MaxCommits + 10 {
		fields = append(fields, commitRecord(commitA, "", "Ada", "1", "1", "", "s")...)
	}
	got, err := ParseLog(nul(fields...))
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	if len(got) != MaxCommits {
		t.Fatalf("ParseLog() read %d commits, want the cap %d", len(got), MaxCommits)
	}
}

func FuzzParseLog(f *testing.F) {
	f.Add(nul(commitRecord(commitA, commitB+" "+commitC, "Ada", "1789861740", "1789861741", "HEAD -> refs/heads/main, tag: refs/tags/v1.1.0", "a subject")...))
	f.Add(nul(commitRecord(commitA, "", "", "", "", "HEAD", "")...))
	f.Add([]byte("\x00\x00\x00\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseLog(data)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseLog() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if len(got) > MaxCommits {
			t.Fatalf("ParseLog() read %d commits, past the cap %d", len(got), MaxCommits)
		}
		for _, c := range got {
			if !isHex(c.OID) {
				t.Fatalf("commit object name %q is not one", c.OID)
			}
			if c.Merge() != (len(c.Parents) > 1) {
				t.Fatalf("commit %+v disagrees with itself about being a merge", c)
			}
			for _, p := range c.Parents {
				if !isHex(p) {
					t.Fatalf("parent object name %q is not one", p)
				}
			}
			for _, r := range c.Refs {
				if r.Name == "" || r.Full == "" {
					t.Fatalf("ref %+v names nothing", r)
				}
				if r.Kind == RefBranch && headsPrefix+r.Name != r.Full {
					t.Fatalf("branch ref %+v is not named by its ref", r)
				}
			}
		}
	})
}
