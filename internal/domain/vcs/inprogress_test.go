package vcs

import (
	"errors"
	"testing"
)

const (
	stoppedOID = "31f87ea3bc97a3779ac025cce3a6d27a16d8e53f"
	ontoOID    = "505702f750dc16539828b41f36b0731227245f60"
	mergedOID  = "c6e2c84123ba06ff2d0dee6a7bbd3d6c24419d94"
)

// The files a stopped rebase writes, captured from a real repository.
func rebaseMergeFiles() map[string]string {
	return map[string]string{
		"rebase-merge/head-name":   "refs/heads/feat/side\n",
		"rebase-merge/onto":        ontoOID + "\n",
		"rebase-merge/msgnum":      "1\n",
		"rebase-merge/end":         "2\n",
		"rebase-merge/stopped-sha": stoppedOID + "\n",
	}
}

func TestParseInProgress(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		want    InProgress
		wantErr bool
	}{
		{name: "a repository in the middle of nothing", files: map[string]string{}},
		{
			name:  "a rebase stopped on a conflict",
			files: rebaseMergeFiles(),
			want: InProgress{
				Kind: OperationRebase, Branch: "feat/side", Onto: ontoOID,
				Heads: []string{stoppedOID}, Step: 1, Total: 2,
			},
		},
		{
			name: "a rebase of a detached head",
			files: map[string]string{
				"rebase-merge/head-name": "detached HEAD\n",
				"rebase-merge/onto":      ontoOID + "\n",
				"rebase-merge/msgnum":    "1\n",
				"rebase-merge/end":       "1\n",
			},
			want: InProgress{Kind: OperationRebase, Onto: ontoOID, Step: 1, Total: 1},
		},
		{
			name: "a rebase carried out patch by patch",
			files: map[string]string{
				"rebase-apply/head-name":       "refs/heads/side\n",
				"rebase-apply/onto":            ontoOID + "\n",
				"rebase-apply/next":            "1\n",
				"rebase-apply/last":            "3\n",
				"rebase-apply/original-commit": stoppedOID + "\n",
			},
			want: InProgress{
				Kind: OperationRebase, Branch: "side", Onto: ontoOID,
				Heads: []string{stoppedOID}, Step: 1, Total: 3,
			},
		},
		{
			name: "a mailbox being applied",
			files: map[string]string{
				"rebase-apply/next":     "2\n",
				"rebase-apply/last":     "5\n",
				"rebase-apply/applying": "",
			},
			want: InProgress{Kind: OperationApply, Step: 2, Total: 5},
		},
		{
			name:  "a cherry pick stopped on a conflict",
			files: map[string]string{"CHERRY_PICK_HEAD": mergedOID + "\n"},
			want:  InProgress{Kind: OperationCherryPick, Heads: []string{mergedOID}},
		},
		{
			name:  "a revert stopped on a conflict",
			files: map[string]string{"REVERT_HEAD": mergedOID + "\n"},
			want:  InProgress{Kind: OperationRevert, Heads: []string{mergedOID}},
		},
		{
			name:  "a merge stopped on a conflict",
			files: map[string]string{"MERGE_HEAD": mergedOID + "\n"},
			want:  InProgress{Kind: OperationMerge, Heads: []string{mergedOID}},
		},
		{
			name:  "a merge of more than two branches",
			files: map[string]string{"MERGE_HEAD": mergedOID + "\n" + stoppedOID + "\n"},
			want:  InProgress{Kind: OperationMerge, Heads: []string{mergedOID, stoppedOID}},
		},
		{
			name:  "a bisection from a branch",
			files: map[string]string{"BISECT_LOG": "git bisect start\n", "BISECT_START": "side\n"},
			want:  InProgress{Kind: OperationBisect, Branch: "side", Bisecting: true},
		},
		{
			name:  "a bisection from a detached head",
			files: map[string]string{"BISECT_LOG": "git bisect start\n", "BISECT_START": ontoOID + "\n"},
			want:  InProgress{Kind: OperationBisect, Heads: []string{ontoOID}, Bisecting: true},
		},
		{
			name:  "a bisection that has not been told where to start",
			files: map[string]string{"BISECT_LOG": "git bisect start\n"},
			want:  InProgress{Kind: OperationBisect, Bisecting: true},
		},
		{
			name: "a rebase is what it is, not the merge it stopped in",
			files: merge(rebaseMergeFiles(), map[string]string{
				"MERGE_HEAD": mergedOID + "\n",
				"BISECT_LOG": "git bisect start\n",
			}),
			want: InProgress{
				Kind: OperationRebase, Branch: "feat/side", Onto: ontoOID,
				Heads: []string{stoppedOID}, Step: 1, Total: 2, Bisecting: true,
			},
		},
		{
			name: "a cherry pick comes before the merge it uses",
			files: map[string]string{
				"CHERRY_PICK_HEAD": mergedOID + "\n",
				"MERGE_HEAD":       stoppedOID + "\n",
			},
			want: InProgress{Kind: OperationCherryPick, Heads: []string{mergedOID}},
		},
		{
			name:    "a head that names no branch",
			files:   map[string]string{"rebase-merge/head-name": "feat/side\n"},
			wantErr: true,
		},
		{
			name:    "a head whose branch is refused",
			files:   map[string]string{"rebase-merge/head-name": "refs/heads/--upload-pack\n"},
			wantErr: true,
		},
		{
			name: "a commit to replay onto that is no commit",
			files: map[string]string{
				"rebase-merge/head-name": "refs/heads/side\n",
				"rebase-merge/onto":      "not-a-hash\n",
			},
			wantErr: true,
		},
		{
			name: "a counter that is no count",
			files: map[string]string{
				"rebase-merge/head-name": "refs/heads/side\n",
				"rebase-merge/msgnum":    "one\n",
			},
			wantErr: true,
		},
		{
			name: "a counter padded with zeroes",
			files: map[string]string{
				"rebase-merge/head-name": "refs/heads/side\n",
				"rebase-merge/msgnum":    "01\n",
			},
			wantErr: true,
		},
		{
			name: "a commit stopped on that is no commit",
			files: map[string]string{
				"rebase-merge/head-name":   "refs/heads/side\n",
				"rebase-merge/stopped-sha": "nope\n",
			},
			wantErr: true,
		},
		{
			name: "a patch counter that is no count",
			files: map[string]string{
				"rebase-apply/next": "-1\n",
				"rebase-apply/last": "3\n",
			},
			wantErr: true,
		},
		{
			name:    "a merge head that is no commit",
			files:   map[string]string{"MERGE_HEAD": "nope\n"},
			wantErr: true,
		},
		{
			name:    "a merge head with an empty line",
			files:   map[string]string{"MERGE_HEAD": mergedOID + "\n\n" + stoppedOID + "\n"},
			wantErr: true,
		},
		{
			name:    "a cherry pick head that is no commit",
			files:   map[string]string{"CHERRY_PICK_HEAD": "nope\n"},
			wantErr: true,
		},
		{
			name:    "a revert head that is no commit",
			files:   map[string]string{"REVERT_HEAD": "nope\n"},
			wantErr: true,
		},
		{
			name: "a bisection started from a name that is refused",
			files: map[string]string{
				"BISECT_LOG":   "git bisect start\n",
				"BISECT_START": "--exec\n",
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseInProgress(tc.files)
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("ParseInProgress() = %+v, want a failure", got)
			case tc.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseInProgress() error = %v, want %v", err, ErrMalformed)
				}
				return
			case err != nil:
				t.Fatalf("ParseInProgress() error = %v", err)
			}
			if got.Kind != tc.want.Kind || got.Branch != tc.want.Branch || got.Onto != tc.want.Onto ||
				got.Step != tc.want.Step || got.Total != tc.want.Total || got.Bisecting != tc.want.Bisecting {
				t.Fatalf("ParseInProgress() = %+v, want %+v", got, tc.want)
			}
			if !equalStrings(got.Heads, tc.want.Heads) {
				t.Fatalf("ParseInProgress() heads = %v, want %v", got.Heads, tc.want.Heads)
			}
			if got.Running() != (tc.want.Kind != OperationNone) {
				t.Fatalf("Running() = %v for %v", got.Running(), got.Kind)
			}
		})
	}
}

func merge(into, from map[string]string) map[string]string {
	for k, v := range from {
		into[k] = v
	}
	return into
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOperationString(t *testing.T) {
	cases := []struct {
		op   Operation
		want string
	}{
		{OperationNone, "none"},
		{OperationMerge, "merge"},
		{OperationRebase, "rebase"},
		{OperationApply, "apply"},
		{OperationCherryPick, "cherry pick"},
		{OperationRevert, "revert"},
		{OperationBisect, "bisect"},
		{Operation(42), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.op.String(); got != tc.want {
				t.Fatalf("Operation(%d).String() = %q, want %q", tc.op, got, tc.want)
			}
		})
	}
}

// TestInProgressPathsAreBounded keeps the reader to a known, small number of
// files: it runs on every refresh of the view.
func TestInProgressPathsAreBounded(t *testing.T) {
	if len(InProgressPaths) == 0 || len(InProgressPaths) > 20 {
		t.Fatalf("InProgressPaths holds %d files", len(InProgressPaths))
	}
	seen := make(map[string]bool, len(InProgressPaths))
	for _, p := range InProgressPaths {
		if seen[p] {
			t.Fatalf("InProgressPaths names %s twice", p)
		}
		seen[p] = true
		if err := ValidatePath(p); err != nil {
			t.Fatalf("InProgressPaths names %s: %v", p, err)
		}
	}
	for _, needed := range []string{
		fileMergeHead, fileCherryPickHead, fileRevertHead, fileBisectLog, fileBisectStart,
		fileRebaseMerge + "head-name", fileRebaseApply + "next",
	} {
		if !seen[needed] {
			t.Fatalf("InProgressPaths leaves out %s, which ParseInProgress reads", needed)
		}
	}
}

func FuzzParseInProgress(f *testing.F) {
	f.Add("rebase-merge/head-name", "refs/heads/side\n", "rebase-merge/msgnum", "1\n")
	f.Add("MERGE_HEAD", mergedOID+"\n", "BISECT_LOG", "git bisect start\n")
	f.Add("rebase-apply/next", "1\n", "rebase-apply/applying", "")
	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		got, err := ParseInProgress(map[string]string{k1: v1, k2: v2})
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseInProgress() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		if got.Branch != "" {
			if err := ValidateRefName(got.Branch); err != nil {
				t.Fatalf("ParseInProgress() read the branch %q: %v", got.Branch, err)
			}
		}
		for _, h := range got.Heads {
			if !isHex(h) {
				t.Fatalf("ParseInProgress() read the commit %q, which is not one", h)
			}
		}
		if got.Onto != "" && !isHex(got.Onto) {
			t.Fatalf("ParseInProgress() read the commit %q, which is not one", got.Onto)
		}
		if got.Step < 0 || got.Total < 0 {
			t.Fatalf("ParseInProgress() = %+v, counting backwards", got)
		}
		if !got.Running() && (got.Branch != "" || got.Onto != "" || len(got.Heads) != 0) {
			t.Fatalf("ParseInProgress() = %+v, with nothing running", got)
		}
	})
}
