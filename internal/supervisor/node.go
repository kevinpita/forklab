package supervisor

import (
	"errors"
	"fmt"
	"log"
	"os"
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
	// Cosmovisor is set when the node runs under cosmovisor: PID is then
	// cosmovisor's and Binary the chain binary its current link holds.
	Cosmovisor bool `json:"cosmovisor,omitempty"`
	PID        int  `json:"pid,omitempty"`
	// Container names the container of a node on the docker runtime, which
	// has no PID.
	Container string `json:"container,omitempty"`
	// Adopted is set when the process was found via its pid file rather than
	// started by this supervisor; its exit code is then unknown.
	Adopted       bool       `json:"adopted,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	ExitCode      *int       `json:"exit_code,omitempty"`
	// Signal names the signal that ended the process, when one did.
	Signal   string     `json:"signal,omitempty"`
	ExitedAt *time.Time `json:"exited_at,omitempty"`
	// Upgrade is the node's phase in the pending upgrade and Halt the plan
	// it halted on; SwapError says why a swap failed.
	Upgrade   SwapPhase `json:"upgrade,omitempty"`
	Halt      *Halt     `json:"halt,omitempty"`
	SwapError string    `json:"swap_error,omitempty"`
}

// process is one live node process. pf is what the runtime knows it by, as
// read or written when the process was attached. adopted is set for a
// process found via its pid file, whose exit the runtime watches for instead
// of waiting on it. done closes when the exit is observed; tailDone closes
// once the log tail has drained after it, or stopped with the supervisor;
// gone closes once the exit is recorded, when the node no longer reads as
// running.
type process struct {
	pf       pidFile
	adopted  bool
	done     chan struct{}
	tailDone chan struct{}
	gone     chan struct{}
}

type exitInfo struct {
	code   *int
	signal string
	at     time.Time
}

// node owns one NodeSpec's process. ops serializes lifecycle operations
// (start, stop, kill, restart, swap); mu guards spec, proc, last, and the
// swap fields. spec.Binary is the path the next start uses; a running
// process may have been started with another one. The log tail and the
// watch of an adopted process run under wg and end when stop closes, so a
// supervisor that has left touches nothing.
type node struct {
	log  *log.Logger
	sub  LineSubscriber
	stop <-chan struct{}
	wg   *sync.WaitGroup
	rt   runtime

	ops     sync.Mutex
	mu      sync.Mutex
	spec    NodeSpec
	proc    *process
	last    *exitInfo
	swap    SwapPhase
	halt    *Halt
	swapErr string
}

func (n *node) status() NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := NodeStatus{Index: n.spec.Index, Name: n.spec.Name, State: StateStopped, Binary: n.binary(), Cosmovisor: n.spec.Cosmovisor != nil, Upgrade: n.swap, SwapError: n.swapErr}
	if c := n.spec.Container; c != nil {
		st.Container = c.Name
	}
	if n.halt != nil {
		halt := *n.halt
		st.Halt = &halt
	}
	if p := n.proc; p != nil {
		started := p.pf.StartedAt
		st.State = StateRunning
		st.PID = p.pf.PID
		st.Adopted = p.adopted
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

// binary is the chain binary the node runs, or will on its next start. A
// running process keeps the one it was started with, except under
// cosmovisor, which switches binaries below a process that stays: there
// spec.Binary follows the current link. The caller holds mu.
func (n *node) binary() string {
	if n.proc != nil && n.spec.Cosmovisor == nil {
		return n.proc.pf.Binary
	}
	return n.spec.Binary
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

// start launches the node. A running node is left as it is. The caller
// holds ops.
func (n *node) start() error {
	if n.running() != nil {
		return nil
	}
	spec := n.currentSpec()
	if spec.Cosmovisor != nil {
		if err := spec.cosmovisorDir().prepare(spec.Binary); err != nil {
			return fmt.Errorf("start node %d: %w", spec.Index, err)
		}
	}
	logEnd := fileSize(spec.LogPath)
	pf, wait, err := n.rt.start(spec)
	if err != nil {
		return fmt.Errorf("start node %d: %w", spec.Index, err)
	}
	pf.LogOffset = logEnd
	if err := writePidFile(spec.PidPath, pf); err != nil {
		_ = n.rt.signal(spec, pf, syscall.SIGKILL, false)
		gaveUp := make(chan struct{})
		timer := time.AfterFunc(killTimeout, func() { close(gaveUp) })
		wait(gaveUp)
		timer.Stop()
		return fmt.Errorf("start node %d: %w", spec.Index, err)
	}
	p := n.attach(pf, false)
	n.log.Printf("node %d: started %s with %s", spec.Index, pf, pf.Binary)
	n.wg.Go(func() {
		if e, ok := wait(n.stop); ok {
			n.exited(p, e)
		}
	})
	return nil
}

// adopt takes over the process recorded in the pid file. One the runtime no
// longer finds alive (for a host process: the pid is gone, or its cmdline
// does not name the recorded binary and this node's home, a recycled pid)
// means the node exited while no supervisor watched: its remaining log
// lines are delivered, then it is marked exited. One the runtime cannot
// tell about is kept as running, with its pid file, and watched: the watch
// ends it once the runtime confirms that it is gone.
func (n *node) adopt() error {
	spec := n.currentSpec()
	pf, err := readPidFile(spec.PidPath)
	if errors.Is(err, os.ErrNotExist) {
		n.rt.reap(spec)
		return nil
	}
	if err != nil {
		return err
	}
	p := n.attach(pf, true)
	switch running, err := n.rt.alive(spec, pf); {
	case err != nil:
		n.log.Printf("node %d: cannot tell whether %s runs, keeping it: %v", spec.Index, pf, err)
	case !running:
		n.log.Printf("node %d: %s from %s is gone", spec.Index, pf, spec.PidPath)
		n.exited(p, exitInfo{at: time.Now()})
		return nil
	default:
		n.log.Printf("node %d: adopted %s running %s", spec.Index, pf, pf.Binary)
	}
	n.wg.Go(func() {
		if n.rt.watch(spec, pf, n.stop) {
			n.exited(p, exitInfo{at: time.Now()})
		}
	})
	return nil
}

// attach records pf as the running process and starts delivering its log
// from pf.LogOffset, persisting the delivered offset back into the pid file
// so the next supervisor resumes there.
func (n *node) attach(pf pidFile, adopted bool) *process {
	p := &process{pf: pf, adopted: adopted, done: make(chan struct{}), tailDone: make(chan struct{}), gone: make(chan struct{})}
	n.mu.Lock()
	n.proc, n.last = p, nil
	spec := n.spec
	n.mu.Unlock()
	if n.sub == nil {
		close(p.tailDone)
		return p
	}
	n.wg.Go(func() {
		defer close(p.tailDone)
		deliver := func(line string) { n.sub.NodeLine(spec.Index, line) }
		persist := func(offset int64) {
			if offset != pf.LogOffset {
				pf.LogOffset = offset
				_ = writePidFile(spec.PidPath, pf)
			}
		}
		if err := tail(spec.LogPath, pf.LogOffset, p.done, n.stop, deliver, persist); err != nil {
			n.log.Printf("node %d: tail %s: %v", spec.Index, spec.LogPath, err)
		}
	})
	return p
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
		n.log.Printf("node %d: %s ended by signal %s", index, p.pf, e.signal)
	case e.code != nil:
		n.log.Printf("node %d: %s exited with code %d", index, p.pf, *e.code)
	default:
		n.log.Printf("node %d: %s exited while unwatched", index, p.pf)
	}
}

// signal sends sig to the running process and waits up to timeout for it to
// exit. A node that is not running is a no-op. The caller holds ops.
func (n *node) signal(sig syscall.Signal, timeout time.Duration) error {
	p := n.running()
	if p == nil {
		return nil
	}
	if p.adopted {
		if running, err := n.rt.alive(n.currentSpec(), p.pf); err == nil && !running {
			<-p.gone
			return nil
		}
	}
	if err := n.rt.signal(n.currentSpec(), p.pf, sig, p.adopted); err != nil {
		return fmt.Errorf("signal node %d: %w", n.spec.Index, err)
	}
	select {
	case <-p.gone:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("node %d (%s) still running %s after %s", n.spec.Index, p.pf, timeout, sig)
	}
}

// setBinary switches the path the next start uses. The caller holds ops.
func (n *node) setBinary(path string) error {
	if err := isExecutable(path); err != nil {
		return err
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
