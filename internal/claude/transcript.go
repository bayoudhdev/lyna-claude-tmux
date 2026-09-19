package claude

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Claude Code writes the transcript of every session under its configuration
// directory, one directory per project. Everything here reads them and nothing
// writes them: the agent appends to a transcript for as long as its session
// runs.
const (
	projectsDir   = "projects"
	transcriptExt = ".jsonl"
	// MaxTranscriptRead bounds one read of a transcript. A transcript grows by
	// a few lines at a time, and one that is read for the first time is read
	// in reads of this size, so a session of any length costs a reader this
	// much memory at a time and no more.
	MaxTranscriptRead = 4 << 20
)

// ErrNotTranscript reports a path that is not a transcript under the projects
// directory. The path comes from a pane option, which anything able to run
// tmux commands can set, so nothing else is ever opened.
var ErrNotTranscript = errors.New("claude: not a transcript under the projects directory")

// TranscriptFile resolves a transcript path to the file it names.
//
// The path must be absolute and already clean, and name a .jsonl file that is,
// once every symbolic link on the way is followed, under the projects
// directory of claudeHome, itself resolved the same way: a configuration
// directory that is a link, as dotfile managers make it, is followed, and a
// link that leads out of it is refused. A file that does not exist is
// reported as such, since a subagent's is written some time after it starts.
func TranscriptFile(claudeHome, path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !isTranscriptName(path) {
		return "", fmt.Errorf("%w: %q", ErrNotTranscript, path)
	}
	root, err := filepath.EvalSymlinks(filepath.Join(claudeHome, projectsDir))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !isTranscriptName(resolved) {
		return "", fmt.Errorf("%w: %q", ErrNotTranscript, path)
	}
	return resolved, nil
}

// isTranscriptName reports a path whose last element is a .jsonl file name.
func isTranscriptName(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, transcriptExt) && base != transcriptExt
}

// TranscriptCursor is where the last read of a transcript ended. The zero
// value reads a transcript from its start.
type TranscriptCursor struct {
	offset int64
	// file is the file the offset is in, so a transcript replaced under the
	// same name is told from one that grew.
	file os.FileInfo
}

// TranscriptChunk is what one read of a transcript returns.
type TranscriptChunk struct {
	// Data are the bytes the transcript gained since the cursor, at most
	// MaxTranscriptRead of them.
	Data []byte
	// Next is the cursor the next read starts from.
	Next TranscriptCursor
	// Restart reports a transcript that shrank or was replaced since the
	// cursor was taken: Data are read from its start, and whatever was built
	// from the bytes read before is to be dropped.
	Restart bool
	// More reports bytes the transcript already holds past Data.
	More bool
}

// ReadTranscript returns what a transcript gained since a cursor. Only a path
// TranscriptFile accepts is opened, and only for reading.
func ReadTranscript(claudeHome, path string, from TranscriptCursor) (TranscriptChunk, error) {
	resolved, err := TranscriptFile(claudeHome, path)
	if err != nil {
		return TranscriptChunk{}, err
	}
	// Not following a link is what keeps the file the one just resolved, and
	// not blocking is what keeps a pipe planted under the name from holding
	// the reader until something writes to it.
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return TranscriptChunk{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return TranscriptChunk{}, err
	}
	if !info.Mode().IsRegular() {
		return TranscriptChunk{}, fmt.Errorf("%w: %q is not a regular file", ErrNotTranscript, path)
	}
	offset := from.offset
	restart := from.file != nil && (!os.SameFile(from.file, info) || info.Size() < offset)
	if restart {
		offset = 0
	}
	buf := make([]byte, min(info.Size()-offset, MaxTranscriptRead))
	n, err := f.ReadAt(buf, offset)
	// A transcript cut short between the stat and the read gives what is
	// there; the next read sees it shrank.
	if err != nil && !errors.Is(err, io.EOF) {
		return TranscriptChunk{}, err
	}
	next := TranscriptCursor{offset: offset + int64(n), file: info}
	return TranscriptChunk{Data: buf[:n], Next: next, Restart: restart, More: info.Size() > next.offset}, nil
}
