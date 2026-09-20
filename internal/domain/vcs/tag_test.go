package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	tagObject = "4bf4662b8b8f6375c445b1ce8eeea8a22fa906ca"
	tagCommit = "363a103d936d9e7713c8e1389e2008186f232128"
)

// tagRecord writes the six fields TagFormat gives one tag.
func tagRecord(fields ...string) string { return strings.Join(fields, "\x00") }

func TestParseTags(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		want    []Tag
		wantErr bool
	}{
		{name: "no tag at all", data: ""},
		{
			name: "a tag that is only a ref",
			data: tagRecord("refs/tags/light", "commit", tagCommit, "", "1789896642", "first commit subject") + "\n",
			want: []Tag{{
				Name: "light", Ref: "refs/tags/light", OID: tagCommit, Commit: tagCommit,
				Created: time.Unix(1789896642, 0).UTC(), Subject: "first commit subject",
			}},
		},
		{
			name: "a tag carrying a message",
			data: tagRecord("refs/tags/v1.0.0", "tag", tagObject, tagCommit, "1789896642", "the first release") + "\n",
			want: []Tag{{
				Name: "v1.0.0", Ref: "refs/tags/v1.0.0", OID: tagObject, Commit: tagCommit, Annotated: true,
				Created: time.Unix(1789896642, 0).UTC(), Subject: "the first release",
			}},
		},
		{
			name: "a name holding a slash",
			data: tagRecord("refs/tags/release/2026.09", "commit", tagCommit, "", "1", "s") + "\n",
			want: []Tag{{
				Name: "release/2026.09", Ref: "refs/tags/release/2026.09", OID: tagCommit,
				Commit: tagCommit, Created: time.Unix(1, 0).UTC(), Subject: "s",
			}},
		},
		{
			name: "a tag with no date and no message",
			data: tagRecord("refs/tags/bare", "commit", tagCommit, "", "", "") + "\n",
			want: []Tag{{Name: "bare", Ref: "refs/tags/bare", OID: tagCommit, Commit: tagCommit}},
		},
		{
			name:    "a record with a field missing",
			data:    tagRecord("refs/tags/x", "commit", tagCommit, "", "1") + "\n",
			wantErr: true,
		},
		{
			name:    "a ref outside the tag namespace",
			data:    tagRecord("refs/heads/main", "commit", tagCommit, "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a ref that is only its namespace",
			data:    tagRecord("refs/tags/", "commit", tagCommit, "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "an object name that is not one",
			data:    tagRecord("refs/tags/x", "commit", "nothex", "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a tag object naming no commit",
			data:    tagRecord("refs/tags/x", "tag", tagObject, "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a tag that is a commit and names one as well",
			data:    tagRecord("refs/tags/x", "commit", tagCommit, tagObject, "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a tag pointing at something else",
			data:    tagRecord("refs/tags/x", "blob", tagCommit, "", "1", "s") + "\n",
			wantErr: true,
		},
		{
			name:    "a date that is not a number",
			data:    tagRecord("refs/tags/x", "commit", tagCommit, "", "yesterday", "s") + "\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTags([]byte(tc.data))
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseTags() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseTags() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseTags() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseTags() read %d tags, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("tag %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseTagsReadsGitItself reads a real listing: a tag that is only a ref
// and two carrying a message, one of them on an older commit.
func TestParseTagsReadsGitItself(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "tags.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseTags(data)
	if err != nil {
		t.Fatalf("ParseTags() error = %v", err)
	}
	type state struct {
		name, subject string
		annotated     bool
	}
	want := []state{
		{name: "light", subject: "first commit subject"},
		{name: "v1.0.0", subject: "the first release", annotated: true},
		{name: "v1.0.1", subject: "a later release", annotated: true},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseTags() read %d tags, want %d", len(got), len(want))
	}
	for i, tag := range got {
		if (state{name: tag.Name, subject: tag.Subject, annotated: tag.Annotated}) != want[i] {
			t.Fatalf("tag %d = %+v, want %+v", i, tag, want[i])
		}
		if !IsObjectName(tag.OID) || !IsObjectName(tag.Commit) || tag.Created.IsZero() {
			t.Fatalf("tag %s = %+v, want a commit and a date", tag.Name, tag)
		}
		if tag.Annotated == (tag.OID == tag.Commit) {
			t.Fatalf("tag %s = %+v, want the tag object apart from the commit", tag.Name, tag)
		}
	}
}

func TestParseTagsCapsTheList(t *testing.T) {
	var b strings.Builder
	for range MaxTags + 10 {
		b.WriteString(tagRecord("refs/tags/x", "commit", tagCommit, "", "1", "s") + "\n")
	}
	got, err := ParseTags([]byte(b.String()))
	if err != nil {
		t.Fatalf("ParseTags() error = %v", err)
	}
	if len(got) != MaxTags {
		t.Fatalf("ParseTags() read %d tags, want the cap %d", len(got), MaxTags)
	}
}

func FuzzParseTags(f *testing.F) {
	f.Add(tagRecord("refs/tags/v1.0.0", "tag", tagObject, tagCommit, "1789896642", "the first release") + "\n")
	f.Add(tagRecord("refs/tags/light", "commit", tagCommit, "", "1", "") + "\n")
	f.Add("refs/tags/x\x00commit\x00\x00\x00\x00\n")
	f.Fuzz(func(t *testing.T, data string) {
		got, err := ParseTags([]byte(data))
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseTags() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if len(got) > MaxTags {
			t.Fatalf("ParseTags() read %d tags, past the cap %d", len(got), MaxTags)
		}
		for _, tag := range got {
			if tag.Name == "" || tagsPrefix+tag.Name != tag.Ref {
				t.Fatalf("tag %+v is not named by its ref", tag)
			}
			if !IsObjectName(tag.OID) || !IsObjectName(tag.Commit) {
				t.Fatalf("tag %+v does not name its objects", tag)
			}
			if !tag.Annotated && tag.OID != tag.Commit {
				t.Fatalf("tag %+v is only a ref and names another commit", tag)
			}
		}
	})
}
