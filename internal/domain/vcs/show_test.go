package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// showRecord writes the nine fields ShowFormat puts before the message, the
// message, and the numstat records that follow it.
func showRecord(fields []string, message string, files ...string) []byte {
	all := strings.Join(fields, "\x00") + "\x00" + message + "\x00"
	for _, f := range files {
		all += f + "\x00"
	}
	return []byte(all)
}

// showFieldsOf is the head of a record for one commit.
func showFieldsOf(oid, parents string) []string {
	return []string{oid, parents, "Ada", "ada@example.invalid", "1789466400", "Grace", "grace@example.invalid", "1789470000", ""}
}

func TestParseShow(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		want    CommitDetail
		files   []NumStat
		wantErr bool
	}{
		{
			name: "a commit of one line",
			data: showRecord(showFieldsOf(commitA, ""), "the first commit\n", "2\t0\ta.txt"),
			want: CommitDetail{
				Commit: Commit{
					OID: commitA, Author: "Ada", Subject: "the first commit",
					Authored: time.Unix(1789466400, 0).UTC(), Committed: time.Unix(1789470000, 0).UTC(),
				},
				AuthorEmail: "ada@example.invalid", Committer: "Grace", CommitterEmail: "grace@example.invalid",
			},
			files: []NumStat{{Path: "a.txt", Added: 2}},
		},
		{
			name: "a commit with a body",
			data: showRecord(showFieldsOf(commitA, commitB), "a subject\n\nthe body\nover two lines\n"),
			want: CommitDetail{
				Commit: Commit{OID: commitA, Parents: []string{commitB}, Author: "Ada", Subject: "a subject"},
				Body:   "the body\nover two lines",
			},
		},
		{
			name:  "a merge of two parents",
			data:  showRecord(showFieldsOf(commitA, commitB+" "+commitC), "a merge commit\n", "1\t1\ta.txt"),
			want:  CommitDetail{Commit: Commit{OID: commitA, Parents: []string{commitB, commitC}, Subject: "a merge commit"}},
			files: []NumStat{{Path: "a.txt", Added: 1, Deleted: 1}},
		},
		{
			name:  "a binary file and a rename",
			data:  showRecord(showFieldsOf(commitA, ""), "s\n", "-\t-\t", "logo.png", "new logo.png"),
			want:  CommitDetail{Commit: Commit{OID: commitA, Subject: "s"}},
			files: []NumStat{{Path: "new logo.png", OrigPath: "logo.png", Binary: true}},
		},
		{
			name: "a commit that touched nothing",
			data: showRecord(showFieldsOf(commitA, ""), "an empty commit\n"),
			want: CommitDetail{Commit: Commit{OID: commitA, Subject: "an empty commit"}},
		},
		{
			name: "a message of nothing at all",
			data: showRecord(showFieldsOf(commitA, ""), ""),
			want: CommitDetail{Commit: Commit{OID: commitA}},
		},
		{
			name:    "a record cut short",
			data:    []byte(commitA + "\x00\x00Ada\x00"),
			wantErr: true,
		},
		{
			name:    "an object name that is not one",
			data:    showRecord(showFieldsOf("nothex", ""), "s\n"),
			wantErr: true,
		},
		{
			name:    "a parent that is not an object name",
			data:    showRecord(showFieldsOf(commitA, "nothex"), "s\n"),
			wantErr: true,
		},
		{
			name:    "a date that is not a number",
			data:    showRecord([]string{commitA, "", "Ada", "a@b", "yesterday", "Grace", "g@h", "1", ""}, "s\n"),
			wantErr: true,
		},
		{
			name:    "a numstat record that is not one",
			data:    showRecord(showFieldsOf(commitA, ""), "s\n", "one line"),
			wantErr: true,
		},
		{
			name:    "a path that leaves the project",
			data:    showRecord(showFieldsOf(commitA, ""), "s\n", "1\t0\t../outside.txt"),
			wantErr: true,
		},
		{
			name:    "a message longer than one is read with",
			data:    showRecord(showFieldsOf(commitA, ""), strings.Repeat("x", MaxMessage+1)),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseShow(tc.data)
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseShow() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseShow() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseShow() error = %v", err)
			}
			if got.Commit.OID != tc.want.Commit.OID || got.Commit.Subject != tc.want.Commit.Subject ||
				got.Body != tc.want.Body {
				t.Fatalf("ParseShow() = %+v, want %+v", got, tc.want)
			}
			if len(got.Commit.Parents) != len(tc.want.Commit.Parents) {
				t.Fatalf("parents = %v, want %v", got.Commit.Parents, tc.want.Commit.Parents)
			}
			if tc.want.AuthorEmail != "" && got.AuthorEmail != tc.want.AuthorEmail {
				t.Fatalf("author email = %q, want %q", got.AuthorEmail, tc.want.AuthorEmail)
			}
			if len(got.Files) != len(tc.files) {
				t.Fatalf("files = %+v, want %+v", got.Files, tc.files)
			}
			for i := range got.Files {
				if got.Files[i] != tc.files[i] {
					t.Fatalf("file %d = %+v, want %+v", i, got.Files[i], tc.files[i])
				}
			}
		})
	}
}

// TestParseShowReadsGitItself reads two commits of a real repository: a merge
// with a rename and a binary file, and the first commit with its body, its
// tag and a path holding a space.
func TestParseShowReadsGitItself(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		parents int
		subject string
		body    string
		files   []NumStat
		refs    []Ref
	}{
		{
			name: "a merge", file: "show-merge.z", parents: 2, subject: "a merge commit",
			files: []NumStat{
				{Path: "a.txt", Added: 2, Deleted: 1},
				{Path: "new logo.png", OrigPath: "logo.png", Binary: true},
			},
			refs: []Ref{{Name: "main", Full: "refs/heads/main", Kind: RefBranch, Head: true}},
		},
		{
			name: "the first commit", file: "show-root.z", subject: "the first commit",
			body: "A body over two lines,\nwith a trailing blank line.",
			files: []NumStat{
				{Path: "a.txt", Added: 2},
				{Path: `dir with space/tab\there.txt`, Added: 1},
				{Path: "logo.png", Binary: true},
			},
			refs: []Ref{{Name: "v1.0.0", Full: "refs/tags/v1.0.0", Kind: RefTag}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseShow(data)
			if err != nil {
				t.Fatalf("ParseShow() error = %v", err)
			}
			if got.Commit.Subject != tc.subject || got.Body != tc.body {
				t.Fatalf("ParseShow() read %q / %q, want %q / %q", got.Commit.Subject, got.Body, tc.subject, tc.body)
			}
			if len(got.Commit.Parents) != tc.parents {
				t.Fatalf("the commit has %d parents, want %d", len(got.Commit.Parents), tc.parents)
			}
			if got.Commit.Author != "Ada Lovelace" || got.AuthorEmail != "ada@example.invalid" ||
				got.Committer != "Grace Hopper" || got.CommitterEmail != "grace@example.invalid" {
				t.Fatalf("the commit was read as %+v, want the author and the committer apart", got)
			}
			if got.Commit.Authored.Equal(got.Commit.Committed) {
				t.Fatalf("the commit was authored and committed at %v, want the two dates apart", got.Commit.Authored)
			}
			if len(got.Files) != len(tc.files) {
				t.Fatalf("files = %+v, want %+v", got.Files, tc.files)
			}
			for i := range got.Files {
				if got.Files[i] != tc.files[i] {
					t.Fatalf("file %d = %+v, want %+v", i, got.Files[i], tc.files[i])
				}
			}
			if len(got.Commit.Refs) != len(tc.refs) {
				t.Fatalf("refs = %+v, want %+v", got.Commit.Refs, tc.refs)
			}
			for i := range got.Commit.Refs {
				if got.Commit.Refs[i] != tc.refs[i] {
					t.Fatalf("ref %d = %+v, want %+v", i, got.Commit.Refs[i], tc.refs[i])
				}
			}
		})
	}
}

func TestCommitDetailCounts(t *testing.T) {
	cases := []struct {
		name           string
		files          []NumStat
		added, deleted int
	}{
		{name: "a commit that touched nothing"},
		{
			name:  "text and binary together",
			files: []NumStat{{Path: "a", Added: 3, Deleted: 1}, {Path: "b", Binary: true}, {Path: "c", Added: 4}},
			added: 7, deleted: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := CommitDetail{Files: tc.files}
			if d.Added() != tc.added || d.Deleted() != tc.deleted {
				t.Fatalf("counts = +%d -%d, want +%d -%d", d.Added(), d.Deleted(), tc.added, tc.deleted)
			}
		})
	}
}

func TestParseShowCapsTheFiles(t *testing.T) {
	files := make([]string, 0, MaxShowFiles+10)
	for i := range MaxShowFiles + 10 {
		files = append(files, "1\t0\tf"+itoa(i)+".txt")
	}
	got, err := ParseShow(showRecord(showFieldsOf(commitA, ""), "s\n", files...))
	if err != nil {
		t.Fatalf("ParseShow() error = %v", err)
	}
	if len(got.Files) != MaxShowFiles {
		t.Fatalf("ParseShow() read %d files, want the cap %d", len(got.Files), MaxShowFiles)
	}
}

func FuzzParseShow(f *testing.F) {
	f.Add(showRecord(showFieldsOf(commitA, commitB+" "+commitC), "a subject\n\na body\n", "1\t0\ta.txt"))
	f.Add(showRecord(showFieldsOf(commitA, ""), "s\n", "-\t-\t", "logo.png", "new logo.png"))
	f.Add([]byte("\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseShow(data)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseShow() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if !IsObjectName(got.Commit.OID) {
			t.Fatalf("commit object name %q is not one", got.Commit.OID)
		}
		if strings.Contains(got.Commit.Subject, "\n") {
			t.Fatalf("the subject %q is more than one line", got.Commit.Subject)
		}
		if len(got.Commit.Subject)+len(got.Body) > MaxMessage {
			t.Fatalf("a message of %d bytes was read, past the cap", len(got.Commit.Subject)+len(got.Body))
		}
		if len(got.Files) > MaxShowFiles {
			t.Fatalf("%d files were read, past the cap %d", len(got.Files), MaxShowFiles)
		}
		for _, file := range got.Files {
			if err := ValidatePath(file.Path); err != nil {
				t.Fatalf("file %+v was read with a path that is not one: %v", file, err)
			}
		}
	})
}
