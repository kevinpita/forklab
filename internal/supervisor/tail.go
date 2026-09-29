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
	return tail(path, offset, done, fn, nil)
}

// tail is Tail with a progress callback that receives the offset just after
// the last delivered line, after each batch, so a restart can resume there.
func tail(path string, offset int64, done <-chan struct{}, fn func(string), progress func(int64)) error {
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
		case <-time.After(100 * time.Millisecond):
		}
	}
}
