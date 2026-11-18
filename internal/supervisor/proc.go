package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// pidFile is what a node's pid file holds. Binary is the chain binary the
// node was started with, which a restart --binary may have changed from
// nodes.json. Exec is the program the pid runs when that is not Binary: the
// cosmovisor that wraps it. Container is the id of the container a node of
// the docker runtime runs in; it then has no PID. LogOffset is where the
// log subscriber resumes: the end of the log when the node started,
// advanced as lines are delivered.
type pidFile struct {
	PID       int       `json:"pid"`
	Binary    string    `json:"binary"`
	Exec      string    `json:"exec,omitempty"`
	Container string    `json:"container,omitempty"`
	StartedAt time.Time `json:"started_at"`
	LogOffset int64     `json:"log_offset"`
}

// String names the process for logs and errors.
func (p pidFile) String() string {
	if p.Container != "" {
		return fmt.Sprintf("container %.12s", p.Container)
	}
	return fmt.Sprintf("pid %d", p.PID)
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

// ErrLiveness means a node's runtime could not tell whether the node runs,
// for example because the docker engine does not answer. Such a lab may be
// running and must not be treated as stopped.
var ErrLiveness = errors.New("cannot tell whether the node runs")

// LiveNodes returns the indexes of labDir's nodes whose pid file names a live
// process running the recorded binary with the node's home, whether or not a
// supervisor watches it. A node without a pid file is asked about too: a
// runtime may know of a process that no file records. It fails with
// ErrLiveness when a runtime cannot tell.
func LiveNodes(labDir string) ([]int, error) {
	specs, err := LoadNodes(labDir)
	if err != nil {
		return nil, err
	}
	var live []int
	for _, s := range specs {
		pf, err := readPidFile(s.PidPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		running, err := runtimeOf(s).alive(s, pf)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w: %w", s.Name, pf, ErrLiveness, err)
		}
		if running {
			live = append(live, s.Index)
		}
	}
	return live, nil
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
