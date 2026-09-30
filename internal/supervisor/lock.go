package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kevinpita/forklab/internal/paths"
)

var errLocked = errors.New("locked")

// ActiveLockPath is active.lock under the forklab home. The running
// supervisor holds it with flock and writes its lab dir in it.
func ActiveLockPath() (string, error) {
	dir, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "active.lock"), nil
}

// tryLock opens path and takes an exclusive flock without blocking. The lock
// lives as long as the returned file is open; the kernel drops it when the
// holder dies, so a stale lock cannot outlive its process.
func tryLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

// ErrLabActive means another lab's supervisor holds active.lock.
type ErrLabActive struct{ LabDir string }

func (e ErrLabActive) Error() string {
	return fmt.Sprintf("lab %s is already running (%s); stop it first", filepath.Base(e.LabDir), e.LabDir)
}

// lockActive takes active.lock for labDir without writing to it, so a
// client probing the lock never leaves its own lab dir behind for a
// supervisor to misread. When another lab holds it the error names that lab;
// when this lab holds it (a live supervisor) the error is errLocked.
func lockActive(labDir string) (*os.File, error) {
	path, err := ActiveLockPath()
	if err != nil {
		return nil, err
	}
	f, err := tryLock(path)
	if errors.Is(err, errLocked) {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		if active := strings.TrimSpace(string(data)); active != "" && active != labDir {
			return nil, ErrLabActive{LabDir: active}
		}
		return nil, errLocked
	}
	return f, err
}

// ActiveLab is the lab dir whose supervisor holds active.lock, or "" when no
// supervisor runs. The file keeps a dead holder's dir, so only a held lock
// counts.
func ActiveLab() (string, error) {
	path, err := ActiveLockPath()
	if err != nil {
		return "", err
	}
	f, err := tryLock(path)
	if err == nil {
		return "", f.Close()
	}
	if !errors.Is(err, errLocked) {
		return "", err
	}
	data, err := os.ReadFile(path)
	return strings.TrimSpace(string(data)), err
}

func writeLockContent(f *os.File, line string) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteString(line + "\n")
	return err
}
