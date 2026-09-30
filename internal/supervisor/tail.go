package supervisor

import (
	"bytes"
	"os"
	"time"
)

// LineSubscriber receives every line a node appends to its log while it runs
// under this supervisor, including lines written while no supervisor was
// alive. Delivery is at-least-once: after a supervisor crash the lines since
// the last persisted offset are delivered again. NodeLine is called from one
// goroutine per node, so calls for different nodes run concurrently. The
// upgrade watcher plugs in here.
type LineSubscriber interface {
	NodeLine(index int, line string)
}

// Tail delivers each complete line of path from offset on, following the
// file until done is closed and then draining what was written by then. A
// closed done reads the file once.
func Tail(path string, offset int64, done <-chan struct{}, fn func(string)) error {
	return tail(path, offset, done, nil, fn, nil)
}

// TailOffset is the offset where the last n lines of path start,
// for a Tail that shows only the end of a long log. An unterminated last
// line counts. n <= 0 means the start.
func TailOffset(path string, n int) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end := fi.Size()
	buf := make([]byte, 64<<10)
	seen := 0
	// Tail delivers an unterminated last line too, so it counts as one.
	if end > 0 {
		if _, err := f.ReadAt(buf[:1], end-1); err != nil {
			return 0, err
		}
		if buf[0] != '\n' {
			seen = 1
		}
	}
	for pos := end; pos > 0; {
		size := min(int64(len(buf)), pos)
		pos -= size
		if _, err := f.ReadAt(buf[:size], pos); err != nil {
			return 0, err
		}
		for i := size - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			if seen == n {
				return pos + i + 1, nil
			}
			seen++
		}
	}
	return 0, nil
}

// tail is Tail with a progress callback that receives the offset just after
// the last delivered line, after each batch, so a restart can resume there.
// A closed stop ends the tail without draining: the file is still being
// written, and the next tail resumes at the last reported offset.
func tail(path string, offset int64, done, stop <-chan struct{}, fn func(string), progress func(int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var partial []byte
	buf := make([]byte, 64<<10)
	// readTo delivers the lines in [offset, limit) and keeps an unterminated
	// tail in partial.
	readTo := func(limit int64) {
		for offset < limit {
			n, err := f.ReadAt(buf[:min(int64(len(buf)), limit-offset)], offset)
			offset += int64(n)
			partial = append(partial, buf[:n]...)
			for {
				i := bytes.IndexByte(partial, '\n')
				if i < 0 {
					break
				}
				fn(string(partial[:i]))
				partial = partial[i+1:]
			}
			if n == 0 || err != nil {
				return
			}
		}
	}
	size := func() int64 {
		info, err := f.Stat()
		if err != nil {
			return offset
		}
		if info.Size() < offset {
			offset, partial = 0, nil
		}
		return info.Size()
	}
	report := func() {
		if progress != nil {
			progress(offset - int64(len(partial)))
		}
	}
	for {
		readTo(size())
		report()
		select {
		case <-done:
			readTo(size())
			if len(partial) > 0 {
				fn(string(partial))
				partial = nil
			}
			report()
			return nil
		case <-stop:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}
