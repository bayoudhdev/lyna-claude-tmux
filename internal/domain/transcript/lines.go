// Package transcript reads the session transcripts Claude Code writes: one JSON
// object per line, appended while the session runs, one file per session and
// one per subagent.
//
// Everything here is pure. The files are read elsewhere, a piece at a time as
// they grow, and fed in here in whatever pieces the reads return: nothing in
// this package depends on where a piece ends.
package transcript

import "bytes"

// MaxLine bounds one line of a transcript. A line carries one message, and
// the longest are tool results and file writes, which Claude Code bounds far
// below this. A line past it is dropped whole rather than held in memory
// until its newline arrives.
const MaxLine = 16 << 20

// keepCap is the buffer a line that has not ended yet may keep between two
// pieces once it has ended. A line that needed more is rare, and the memory
// it took is given back rather than held for the next one.
const keepCap = 1 << 20

// Lines cuts the bytes of a growing file into lines. The zero value is ready
// to use.
type Lines struct {
	// partial is the line the last piece started and did not end.
	partial []byte
	// skipping is set while the rest of a line longer than the limit arrives.
	skipping bool
	// limit replaces MaxLine when set, which is how tests reach the bound.
	limit int
}

// Feed takes the next bytes of the file and calls line with every line they
// complete, without its newline. A line that has not ended yet is kept until
// its newline arrives, so a line cut in two by a read is seen once, whole. A
// line longer than MaxLine is dropped, whatever pieces it arrives in.
//
// The slice line receives is only valid for the duration of the call.
func (l *Lines) Feed(p []byte, line func([]byte)) {
	limit := l.limit
	if limit <= 0 {
		limit = MaxLine
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			l.keep(p, limit)
			return
		}
		end := p[:i]
		p = p[i+1:]
		switch {
		case l.skipping:
			// The newline of the line that was too long.
			l.skipping = false
		case len(l.partial)+len(end) > limit:
			l.release()
		case len(l.partial) > 0:
			l.partial = append(l.partial, end...)
			line(l.partial)
			l.release()
		default:
			line(end)
		}
	}
}

// release empties the kept line, and gives its buffer back when a long line
// grew it.
func (l *Lines) release() {
	l.partial = l.partial[:0]
	if cap(l.partial) > keepCap {
		l.partial = nil
	}
}

// keep holds the start of a line that has not ended, or drops it once it is
// longer than the limit.
func (l *Lines) keep(p []byte, limit int) {
	if l.skipping {
		return
	}
	if len(l.partial)+len(p) > limit {
		l.partial, l.skipping = nil, true
		return
	}
	l.partial = append(l.partial, p...)
}
