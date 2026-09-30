package tui

import (
	"regexp"
	"strings"
)

// logBuffer holds the tail of one node's log. offset counts lines up from
// the bottom; follow pins it to 0 as lines arrive.
type logBuffer struct {
	node   string
	lines  []string
	follow bool
	wrap   bool
	offset int
}

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

func (l *logBuffer) reset(node string) {
	l.node, l.lines, l.offset, l.follow = node, nil, 0, true
}

// add appends a line with its ANSI codes stripped, since the TUI colors
// levels itself and a stray cursor code would break the layout. A paused
// view keeps showing the same lines.
func (l *logBuffer) add(line string, rows int) {
	l.lines = append(l.lines, strings.ReplaceAll(ansiSeq.ReplaceAllString(line, ""), "\t", "    "))
	// Trimming in bulk keeps the copy off the per-line path.
	if n := len(l.lines); n > 2*logCap {
		l.lines = append(l.lines[:0:0], l.lines[n-logCap:]...)
	}
	if !l.follow {
		l.offset = min(l.offset+1, max(len(l.lines)-rows, 0))
	}
}

// scrollBy moves the view; scrolling up pauses follow and reaching the
// bottom resumes it.
func (l *logBuffer) scrollBy(delta, rows int) {
	l.offset = min(max(l.offset-delta, 0), max(len(l.lines)-rows, 0))
	l.follow = l.offset == 0
}

// window returns the rows lines ending offset lines above the bottom.
func (l *logBuffer) window(rows int) []string {
	end := max(len(l.lines)-l.offset, 0)
	return l.lines[max(end-rows, 0):end]
}
