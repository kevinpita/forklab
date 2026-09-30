package snapshot

import (
	"context"
	"os"
	"syscall"
	"time"
)

// lock takes an exclusive flock on path, waiting for another forklab
// process to release it. waiting runs once if the lock is busy. Unlock
// removes the file, so a lock only counts while path still names the
// locked file: a waiter that wakes up on an unlinked inode starts over.
func lock(ctx context.Context, path string, waiting func()) (unlock func(), err error) {
	for first := true; ; first = false {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			held, err := f.Stat()
			if err != nil {
				_ = f.Close()
				return nil, err
			}
			if named, err := os.Stat(path); err == nil && os.SameFile(held, named) {
				return func() {
					_ = os.Remove(path)
					_ = f.Close()
				}, nil
			}
			_ = f.Close()
			continue
		}
		_ = f.Close()
		if err != syscall.EWOULDBLOCK {
			return nil, err
		}
		if first && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
