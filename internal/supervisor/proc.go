package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// pidFile is what a node's pid file holds. Binary is the path the node was
// started with, which a restart --binary may have changed from nodes.json.
// LogOffset is where the log subscriber resumes: the end of the log when the
// node started, advanced as lines are delivered.
type pidFile struct {
	PID       int       `json:"pid"`
	Binary    string    `json:"binary"`
	StartedAt time.Time `json:"started_at"`
	LogOffset int64     `json:"log_offset"`
}

func writePidFile(path string, p pidFile) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readPidFile(path string) (pidFile, error) {
	var p pidFile
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// detach starts cmd in its own session with stdin from /dev/null and stdout
// and stderr appended to logPath, so it outlives its parent.
func detach(cmd *exec.Cmd, logPath string) error {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// alive reports whether pid exists. A zombie counts as alive until reaped.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// cmdlineMatches reports whether pid's argv names both binary and home, so a
// recycled pid running something else is never adopted.
func cmdlineMatches(pid int, binary, home string) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	argv := bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0})
	has := func(s string) bool { return slices.ContainsFunc(argv, func(a []byte) bool { return string(a) == s }) }
	return has(binary) && has(home)
}
