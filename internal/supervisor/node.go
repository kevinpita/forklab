package supervisor

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// State is a node's lifecycle phase: stopped (never started under this
// supervisor), running, or exited (the last process ended).
type State string

const (
	StateStopped State = "stopped"
	StateRunning State = "running"
	StateExited  State = "exited"
)

type NodeStatus struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	State  State  `json:"state"`
	Binary string `json:"binary"`
	PID    int    `json:"pid,omitempty"`
	// Adopted is set when the process was found via its pid file rather than
	// started by this supervisor; its exit code is then unknown.
	Adopted       bool       `json:"adopted,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	ExitCode      *int       `json:"exit_code,omitempty"`
	// Signal names the signal that ended the process, when one did.
	Signal   string     `json:"signal,omitempty"`
	ExitedAt *time.Time `json:"exited_at,omitempty"`
}

// process is one live node process. cmd is nil for adopted processes, whose
// exit is observed by polling instead of wait(2). done closes when the exit
// is observed; tailDone closes once the log tail has drained after it; gone
// closes once the exit is recorded, when the node no longer reads as running.
type process struct {
	pid       int
	binary    string
	startedAt time.Time
	cmd       *exec.Cmd
	done      chan struct{}
	tailDone  chan struct{}
	gone      chan struct{}
}

type exitInfo struct {
	code   *int
	signal string
	at     time.Time
}

// node owns one NodeSpec's process. ops serializes lifecycle operations
// (start, stop, kill, restart); mu guards spec, proc, adopted, and last.
// spec.Binary is the path the next start uses; a running process may have
// been started with another one.
type node struct {
	log *log.Logger
	sub LineSubscriber

	ops     sync.Mutex
	mu      sync.Mutex
	spec    NodeSpec
	proc    *process
	adopted bool
	last    *exitInfo
}

func (n *node) status() NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := NodeStatus{Index: n.spec.Index, Name: n.spec.Name, State: StateStopped, Binary: n.spec.Binary}
	if p := n.proc; p != nil {
		started := p.startedAt
		st.State = StateRunning
		st.PID = p.pid
		st.Binary = p.binary
		st.Adopted = n.adopted
		st.StartedAt = &started
		st.UptimeSeconds = int64(time.Since(started).Seconds())
	} else if e := n.last; e != nil {
		at := e.at
		st.State = StateExited
		st.ExitCode = e.code
		st.Signal = e.signal
		st.ExitedAt = &at
	}
	return st
}

func (n *node) running() *process {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.proc
}

func (n *node) currentSpec() NodeSpec {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.spec
}

// start spawns the node detached. A running node is left as it is. The
// caller holds ops.
func (n *node) start() error {
	if n.running() != nil {
		return nil
	}
	spec := n.currentSpec()
	pf := pidFile{Binary: spec.Binary, LogOffset: fileSize(spec.LogPath)}
	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Dir = spec.Home
	if err := detach(cmd, spec.LogPath); err != nil {
		return fmt.Errorf("start node %d: %w", spec.Index, err)
	}
	pf.PID, pf.StartedAt = cmd.Process.Pid, time.Now()
	if err := writePidFile(spec.PidPath, pf); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("start node %d: %w", spec.Index, err)
	}
	p := n.attach(pf, cmd, false)
	n.log.Printf("node %d: started pid %d with %s", spec.Index, p.pid, p.binary)
	go n.waitChild(p)
	return nil
}

// adopt takes over the process recorded in the pid file. A pid that is gone,
// or whose cmdline does not name the recorded binary and this node's home
// (a recycled pid), means the node exited while no supervisor watched: its
// remaining log lines are delivered, then it is marked exited.
func (n *node) adopt() error {
	spec := n.currentSpec()
	pf, err := readPidFile(spec.PidPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !alive(pf.PID) || !cmdlineMatches(pf.PID, pf.Binary, spec.Home) {
		n.log.Printf("node %d: pid %d from %s is gone", spec.Index, pf.PID, spec.PidPath)
		p := n.attach(pf, nil, true)
		n.exited(p, exitInfo{at: time.Now()})
		return nil
	}
	p := n.attach(pf, nil, true)
	n.log.Printf("node %d: adopted pid %d running %s", spec.Index, p.pid, p.binary)
	go n.pollAdopted(p)
	return nil
}

// attach records pf as the running process and starts delivering its log
// from pf.LogOffset, persisting the delivered offset back into the pid file
// so the next supervisor resumes there.
func (n *node) attach(pf pidFile, cmd *exec.Cmd, adopted bool) *process {
	p := &process{pid: pf.PID, binary: pf.Binary, startedAt: pf.StartedAt, cmd: cmd, done: make(chan struct{}), tailDone: make(chan struct{}), gone: make(chan struct{})}
	n.mu.Lock()
	n.proc, n.adopted, n.last = p, adopted, nil
	spec := n.spec
	n.mu.Unlock()
	if n.sub == nil {
		close(p.tailDone)
		return p
	}
	go func() {
		defer close(p.tailDone)
		deliver := func(line string) { n.sub.NodeLine(spec.Index, line) }
		persist := func(offset int64) {
			if offset != pf.LogOffset {
				pf.LogOffset = offset
				_ = writePidFile(spec.PidPath, pf)
			}
		}
		if err := tail(spec.LogPath, pf.LogOffset, p.done, deliver, persist); err != nil {
			n.log.Printf("node %d: tail %s: %v", spec.Index, spec.LogPath, err)
		}
	}()
	return p
}

func (n *node) waitChild(p *process) {
	err := p.cmd.Wait()
	e := exitInfo{at: time.Now()}
	var exitErr *exec.ExitError
	if ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		e.signal = ws.Signal().String()
	} else if err == nil || errors.As(err, &exitErr) {
		code := p.cmd.ProcessState.ExitCode()
		e.code = &code
	}
	n.exited(p, e)
}

// pollAdopted watches a process this supervisor cannot wait(2) on. The
// cmdline check makes a recycled pid, or a zombie whose cmdline is empty,
// count as exited.
func (n *node) pollAdopted(p *process) {
	for n.adoptedAlive(p) {
		time.Sleep(200 * time.Millisecond)
	}
	n.exited(p, exitInfo{at: time.Now()})
}

func (n *node) adoptedAlive(p *process) bool {
	return alive(p.pid) && cmdlineMatches(p.pid, p.binary, n.spec.Home)
}

// exited lets the tail drain what the process wrote, then records the exit
// and removes the pid file. Until then the node still reads as running, so
// signal waits for gone before its caller may start a new process.
func (n *node) exited(p *process, e exitInfo) {
	close(p.done)
	<-p.tailDone
	n.mu.Lock()
	if n.proc == p {
		n.proc, n.last = nil, &e
		_ = os.Remove(n.spec.PidPath)
	}
	index := n.spec.Index
	n.mu.Unlock()
	close(p.gone)
	switch {
	case e.signal != "":
		n.log.Printf("node %d: pid %d ended by signal %s", index, p.pid, e.signal)
	case e.code != nil:
		n.log.Printf("node %d: pid %d exited with code %d", index, p.pid, *e.code)
	default:
		n.log.Printf("node %d: pid %d exited while unwatched", index, p.pid)
	}
}

// signal sends sig to the running process and waits up to timeout for it to
// exit. A node that is not running is a no-op. The caller holds ops.
func (n *node) signal(sig syscall.Signal, timeout time.Duration) error {
	p := n.running()
	if p == nil {
		return nil
	}
	if p.cmd == nil && !n.adoptedAlive(p) {
		<-p.gone
		return nil
	}
	if err := syscall.Kill(p.pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal node %d: %w", n.spec.Index, err)
	}
	select {
	case <-p.gone:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("node %d (pid %d) still running %s after %s", n.spec.Index, p.pid, timeout, sig)
	}
}

// setBinary switches the path the next start uses. The caller holds ops.
func (n *node) setBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	n.mu.Lock()
	n.spec.Binary = path
	n.mu.Unlock()
	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
