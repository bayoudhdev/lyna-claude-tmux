package tui

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// benchCommits is how much history the workstation is measured against: one
// page of it, which is what the graph draws through before it asks for more.
const benchCommits = 400

// benchGitState is a repository the size the workstation is judged at: a page
// of history with refs scattered through it, every kind of row in the refs
// pane, and a working tree holding changes.
func benchGitState() GitState {
	at := fixedNow.Add(-time.Duration(benchCommits) * time.Hour)
	commits := make([]vcs.Commit, 0, benchCommits)
	for i := range benchCommits {
		id := strconv.Itoa(i)
		c := vcs.Commit{
			OID:       fmt.Sprintf("%040x", i+1),
			Author:    "Dana Mercier",
			Authored:  at.Add(time.Duration(i) * time.Hour),
			Committed: at.Add(time.Duration(i) * time.Hour),
			Subject:   "rewrite the router of service " + id,
		}
		if i > 0 {
			c.Parents = []string{fmt.Sprintf("%040x", i)}
		}
		if i%3 == 0 {
			// A merge, which is what makes the lanes of the graph work.
			c.Parents = append(c.Parents, fmt.Sprintf("%040x", max(i-2, 1)))
		}
		if i%40 == 0 {
			c.Refs = []vcs.Ref{{Name: "feat/" + id, Full: "refs/heads/feat/" + id, Kind: vcs.RefBranch}}
		}
		commits = append(commits, c)
	}
	// Newest first, the way a history is read.
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	st := GitState{Commits: commits, More: true, Changes: sampleChanges(), At: fixedNow}
	for i := range 20 {
		id := strconv.Itoa(i)
		st.Refs.Branches = append(st.Refs.Branches, vcs.LocalBranch{
			Name: "feat/" + id, Ref: "refs/heads/feat/" + id, OID: commits[i].OID,
			Tip: commits[i].Committed, Subject: commits[i].Subject,
		})
		st.Refs.Remotes = append(st.Refs.Remotes, vcs.RemoteBranch{
			Name: "origin/feat/" + id, Ref: "refs/remotes/origin/feat/" + id,
			Remote: "origin", OID: commits[i].OID,
		})
		st.Refs.Tags = append(st.Refs.Tags, vcs.Tag{
			Name: "v1." + id + ".0", Ref: "refs/tags/v1." + id + ".0",
			OID: commits[i].OID, Commit: commits[i].OID,
		})
		st.Refs.Stashes = append(st.Refs.Stashes, vcs.Stash{
			Index: i, Ref: "stash@{" + id + "}", OID: commits[i].OID,
			Message: "what was put aside in service " + id,
		})
	}
	return st
}

// BenchmarkGitWorkRender measures one redraw of the whole workstation: the
// refs, the lanes of the history and the commit in front of you, which is what
// every key press costs.
func BenchmarkGitWorkRender(b *testing.B) {
	m := NewGitWork(GitWorkOptions{
		Styles: goldenStyles(b), Width: 200, Height: 50, Now: clock,
		Root: testHome + "/src/acme", Home: testHome,
	})
	m.Update(gitStateMsg{state: benchGitState()})
	b.ReportAllocs()
	for b.Loop() {
		if m.View().Content == "" {
			b.Fatal("the workstation drew nothing")
		}
	}
}

// BenchmarkGitWorkUpdate measures a reading arriving: the regions take the
// whole state again and the frame is drawn from it, which is the cost of one
// commit landing while the workstation is open.
func BenchmarkGitWorkUpdate(b *testing.B) {
	st := benchGitState()
	m := NewGitWork(GitWorkOptions{
		Styles: goldenStyles(b), Width: 200, Height: 50, Now: clock,
		Root: testHome + "/src/acme", Home: testHome,
	})
	b.ReportAllocs()
	for b.Loop() {
		m.Update(gitStateMsg{state: st})
		if m.View().Content == "" {
			b.Fatal("the workstation drew nothing")
		}
	}
}
