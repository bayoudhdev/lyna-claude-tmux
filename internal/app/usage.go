package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/transcript"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// TranscriptUsage keeps what every agent of the agents rail has spent, read
// from the transcripts Claude Code writes.
//
// A transcript is read once from its start and afterwards only for what it
// gained, so a rail that reads the agents on every hook costs a few lines per
// reading however long the sessions run. It is owned by the rail's refresh
// loop, which is its one caller, and is not safe for concurrent use.
type TranscriptUsage struct {
	claudeHome string
	files      map[string]*usageFile
}

// usageFile is one transcript being followed: its tally so far and where the
// last read of it ended.
type usageFile struct {
	meter  transcript.Meter
	cursor claude.TranscriptCursor
}

// NewTranscriptUsage follows the transcripts under a Claude configuration
// directory, which is the only place a transcript is read from.
func NewTranscriptUsage(claudeHome string) *TranscriptUsage {
	return &TranscriptUsage{claudeHome: claudeHome, files: map[string]*usageFile{}}
}

// Fill sets the usage of every row that names a transcript, from what the
// transcript holds now, and forgets the transcripts no row names any more.
//
// A transcript not written yet, which a subagent that has just started has, is
// a usage of nothing. One that cannot be read keeps the usage last read from
// it; the failures are returned together, and the rows are filled either way.
func (u *TranscriptUsage) Fill(ctx context.Context, rows []team.Row) error {
	named := make(map[string]bool, len(rows))
	var errs []error
	for _, r := range rows {
		if r.Transcript == "" || named[r.Transcript] {
			continue
		}
		named[r.Transcript] = true
		f, ok := u.files[r.Transcript]
		if !ok {
			f = &usageFile{}
			u.files[r.Transcript] = f
		}
		if err := u.follow(ctx, r.Transcript, f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("usage of %s: %w", sanitize.Line(r.Name), err))
		}
	}
	for path := range u.files {
		if !named[path] {
			delete(u.files, path)
		}
	}
	for i, r := range rows {
		if f, ok := u.files[r.Transcript]; ok {
			rows[i].Usage = f.meter.Tally()
		}
	}
	return errors.Join(errs...)
}

// follow reads what a transcript gained since the last read, in reads of a
// bounded size, until it has read all of it or the context is done. A
// transcript that shrank or was replaced is tallied again from its start.
func (u *TranscriptUsage) follow(ctx context.Context, path string, f *usageFile) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, err := claude.ReadTranscript(u.claudeHome, path, f.cursor)
		if err != nil {
			return err
		}
		if chunk.Restart {
			f.meter = transcript.Meter{}
		}
		_, _ = f.meter.Write(chunk.Data)
		f.cursor = chunk.Next
		if !chunk.More {
			return nil
		}
	}
}
