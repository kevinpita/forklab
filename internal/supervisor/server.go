package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrAlreadyRunning means another supervisor holds this lab's supervisor.lock.
var ErrAlreadyRunning = errors.New("a supervisor is already running for this lab")

type Options struct {
	LabDir string
	// Subscriber, when set, receives every node log line.
	Subscriber LineSubscriber
	// RecordVersion, when set, is told the profile version a node runs once
	// a restart --binary or an upgrade swap has put it on that binary.
	RecordVersion func(index int, version string) error
	// Log defaults to stderr, which the spawner points at supervisor.log.
	Log io.Writer
}

// killTimeout bounds the wait after SIGKILL; the kernel ends the process at
// once, so this only covers reaping.
const killTimeout = 5 * time.Second

type supervisor struct {
	paths         Paths
	log           *log.Logger
	nodes         []*node
	forward       LineSubscriber
	recordVersion func(index int, version string) error

	// upgrade is the pending upgrade and completed the last one every node
	// was swapped to, both mirrored in upgrade.json.
	upMu      sync.Mutex
	upgrade   *Upgrade
	completed *Upgrade
	// planOps serializes the ops that change the plan, so each reconciles
	// the nodes with the plan it set.
	planOps sync.Mutex

	saveMu sync.Mutex
	// recorded is the version last passed to recordVersion, by node index.
	recorded map[int]string
	// closing is set by down and exit so a node op racing them cannot spawn
	// a process the shutdown never sees.
	closing  atomic.Bool
	quit     chan struct{}
	quitOnce sync.Once
	// stop closes once Run is leaving; wg counts the node goroutines that
	// end on it, so Run returns only after they have.
	stop chan struct{}
	wg   sync.WaitGroup
}

// Run is the supervisor process: it takes supervisor.lock and active.lock,
// adopts live nodes, and serves the API until ctx ends or a down or exit
// request arrives. It returns ErrAlreadyRunning when this lab already has a
// supervisor and ErrLabActive when another lab is running.
func Run(ctx context.Context, opts Options) error {
	labDir, err := filepath.Abs(opts.LabDir)
	if err != nil {
		return err
	}
	logw := opts.Log
	if logw == nil {
		logw = os.Stderr
	}
	s := &supervisor{paths: Paths{labDir}, log: log.New(logw, "", log.LstdFlags|log.Lmicroseconds), quit: make(chan struct{}), stop: make(chan struct{}), forward: opts.Subscriber, recordVersion: opts.RecordVersion}

	lock, err := tryLock(s.paths.Lock())
	if errors.Is(err, errLocked) {
		return ErrAlreadyRunning
	}
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := writeLockContent(lock, strconv.Itoa(os.Getpid())); err != nil {
		return err
	}

	active, err := s.claimActive(labDir)
	if err != nil {
		return err
	}
	defer func() { _ = active.Close() }()
	if err := writeLockContent(active, labDir); err != nil {
		return err
	}

	specs, err := LoadNodes(labDir)
	if err != nil {
		return err
	}
	// The pending upgrade and the halts already seen are known before any
	// log line is replayed, so a halt that happened while no supervisor
	// watched, or whose line the last supervisor consumed before dying, is
	// swapped on adoption.
	st, err := loadUpgradeState(labDir)
	if err != nil {
		return err
	}
	s.upgrade, s.completed = st.Plan, st.Completed
	for _, spec := range specs {
		n := &node{log: s.log, sub: s, stop: s.stop, wg: &s.wg, spec: spec}
		if h, ok := st.Halts[spec.Index]; ok {
			n.halt, n.swap = &h, SwapHalted
		}
		s.nodes = append(s.nodes, n)
	}
	for _, n := range s.nodes {
		if err := n.adopt(); err != nil {
			s.log.Printf("node %d: adopt: %v", n.spec.Index, err)
		}
		s.reconcile(n, s.upgrade)
	}
	s.saveState()

	if err := s.paths.CheckSock(); err != nil {
		return err
	}
	_ = os.Remove(s.paths.Sock())
	ln, err := net.Listen("unix", s.paths.Sock())
	if err != nil {
		return err
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(s.paths.Sock())
	}()
	go s.accept(ln)
	s.log.Printf("supervisor ready: pid %d, %d nodes, %s", os.Getpid(), len(s.nodes), s.paths.Sock())

	select {
	case <-ctx.Done():
		s.log.Printf("supervisor exiting on signal; nodes keep running")
	case <-s.quit:
		s.log.Printf("supervisor exiting on request")
	}
	// Let every in-flight node op finish, so no process is left without its
	// pid file for the next supervisor to find, then end the tails and polls
	// so nothing touches the lab after Run returns.
	s.closing.Store(true)
	for _, n := range s.nodes {
		n.ops.Lock()
	}
	close(s.stop)
	s.wg.Wait()
	return nil
}

// maxSockPath is the longest unix socket path Linux accepts (sun_path holds
// 108 bytes including the terminating NUL).
const maxSockPath = 107

// CheckSock fails when the socket path is too long to bind.
func (p Paths) CheckSock() error {
	if len(p.Sock()) > maxSockPath {
		return fmt.Errorf("socket path %s is %d bytes; unix sockets allow at most %d, move the lab to a shorter path", p.Sock(), len(p.Sock()), maxSockPath)
	}
	return nil
}

// claimActive takes active.lock, retrying briefly because a client's
// pre-flight check may hold it for an instant.
func (s *supervisor) claimActive(labDir string) (*os.File, error) {
	deadline := time.Now().Add(time.Second)
	for {
		f, err := lockActive(labDir)
		if !errors.Is(err, errLocked) || time.Now().After(deadline) {
			return f, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *supervisor) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *supervisor) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: "bad request: " + err.Error(), Nodes: []NodeStatus{}})
		return
	}
	resp, quit := s.handle(req)
	_ = json.NewEncoder(conn).Encode(resp)
	if quit {
		s.quitOnce.Do(func() { close(s.quit) })
	}
}

// handle runs one request. quit reports that the supervisor exits once the
// response is written.
func (s *supervisor) handle(req Request) (resp Response, quit bool) {
	var err error
	switch req.Op {
	case OpStatus:
	case OpStart, OpStop, OpKill, OpRestart, OpConfigure:
		err = s.each(req)
	case OpDown:
		s.closing.Store(true)
		if err = s.each(Request{Op: OpStop, Nodes: All, Timeout: req.Timeout}); err != nil {
			s.log.Printf("down: %v; killing", err)
			err = s.each(Request{Op: OpKill, Nodes: All})
		}
		quit = true
	case OpExit:
		s.closing.Store(true)
		quit = true
	case OpUpgrade:
		err = s.setUpgrade(req.Upgrade)
	case OpComplete:
		if req.Upgrade == nil {
			err = errors.New("complete: no upgrade named")
		} else {
			err = s.complete(req.Upgrade.Name)
		}
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	resp.Nodes = s.statuses()
	s.upMu.Lock()
	resp.Upgrade, resp.Completed = s.upgrade, s.completed
	s.upMu.Unlock()
	if err != nil {
		resp.Error = err.Error()
	}
	return resp, quit
}

func (s *supervisor) statuses() []NodeStatus {
	out := make([]NodeStatus, len(s.nodes))
	for i, n := range s.nodes {
		out[i] = n.status()
	}
	return out
}

// each applies a node op to every selected node concurrently, so stopping
// all nodes waits one timeout, not one per node.
func (s *supervisor) each(req Request) error {
	idx, err := selectNodes(req.Nodes, len(s.nodes))
	if err != nil {
		return err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	errs := make([]error, len(idx))
	var wg sync.WaitGroup
	for k, i := range idx {
		wg.Go(func() {
			n := s.nodes[i]
			n.ops.Lock()
			defer n.ops.Unlock()
			if s.closing.Load() && req.Op != OpStop && req.Op != OpKill {
				errs[k] = errors.New("supervisor is shutting down")
				return
			}
			errs[k] = s.apply(n, req, timeout)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (s *supervisor) apply(n *node, req Request, timeout time.Duration) error {
	switch req.Op {
	case OpStart:
		return n.start()
	case OpStop:
		return n.signal(syscall.SIGTERM, timeout)
	case OpKill:
		return n.signal(syscall.SIGKILL, killTimeout)
	case OpRestart:
		return s.restart(n, req.Binary, req.Version, timeout)
	case OpConfigure:
		if n.running() != nil {
			return fmt.Errorf("node %d must be stopped before configuring arguments", n.spec.Index)
		}
		if !slices.Contains(req.Args, n.currentSpec().Home) {
			return fmt.Errorf("node %d arguments must contain its home", n.spec.Index)
		}
		n.mu.Lock()
		previous := n.spec.Args
		n.spec.Args = slices.Clone(req.Args)
		n.mu.Unlock()
		if err := s.saveNodes(); err != nil {
			n.mu.Lock()
			n.spec.Args = previous
			n.mu.Unlock()
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown op %q", req.Op)
}

// binaryProbe is how long a node must stay up on a new binary before the
// binary is persisted; a wrong binary usually dies at once.
const binaryProbe = time.Second

// restart persists a new binary only once the node has run on it for
// binaryProbe. If the node fails to start or dies within that time, the
// previous binary is restored for the next start and the error reports it.
// A version is recorded once the node runs on its binary. Once the node
// runs, a failure to persist is logged, not returned: the process state
// is right, and reconcile repairs the files on the next start.
func (s *supervisor) restart(n *node, binary, version string, timeout time.Duration) error {
	previous := n.currentSpec().Binary
	if binary != "" {
		if err := n.setBinary(binary); err != nil {
			return fmt.Errorf("restart node %d: %w", n.spec.Index, err)
		}
	}
	restore := func(err error) error {
		n.mu.Lock()
		n.spec.Binary = previous
		n.mu.Unlock()
		return err
	}
	if err := n.signal(syscall.SIGTERM, timeout); err != nil {
		return restore(err)
	}
	logEnd := fileSize(n.currentSpec().LogPath)
	if err := n.start(); err != nil {
		return restore(err)
	}
	if binary != "" && binary != previous {
		// A child that died at once may already be recorded as exited,
		// leaving no running process to wait on.
		p := n.running()
		if p != nil {
			select {
			case <-p.gone:
				p = nil
			case <-time.After(binaryProbe):
			}
		}
		if p == nil {
			st := n.status()
			return restore(fmt.Errorf("node %d exited within %s on %s (exit code %v, signal %q); keeping %s%s", n.spec.Index, binaryProbe, binary, deref(st.ExitCode), st.Signal, previous, logSince(n.currentSpec().LogPath, logEnd)))
		}
		if err := s.saveNodes(); err != nil {
			s.log.Printf("node %d: nodes.json: %v", n.spec.Index, err)
		}
	}
	if err := s.record(n, version); err != nil {
		s.log.Printf("node %d: record version %s: %v", n.spec.Index, version, err)
	}
	// Swapped means recorded, so the halt is settled last.
	s.settleSwap(n, n.currentSpec().Binary)
	return nil
}

// failLogLines bounds what a failed start's error quotes from the node log.
const failLogLines = 10

// logSince returns what the node wrote to its log from offset on, as the
// last failLogLines lines indented under a header, or "" when nothing.
func logSince(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.NewSectionReader(f, offset, 64<<10))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > failLogLines {
		lines = lines[len(lines)-failLogLines:]
	}
	return "\nit logged:\n  " + strings.Join(lines, "\n  ")
}

// record tells RecordVersion which profile version node n now runs, once
// per node and version: a swap that finishes while a new supervisor's
// startup reconcile waits on the node would otherwise be recorded by both.
// Calls are serialized: the callback rewrites lab.yaml, and nodes swap
// concurrently.
func (s *supervisor) record(n *node, version string) error {
	if version == "" || s.recordVersion == nil {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.recorded[n.spec.Index] == version {
		return nil
	}
	if err := s.recordVersion(n.spec.Index, version); err != nil {
		return err
	}
	if s.recorded == nil {
		s.recorded = map[int]string{}
	}
	s.recorded[n.spec.Index] = version
	return nil
}

func deref(code *int) any {
	if code == nil {
		return "unknown"
	}
	return *code
}

// saveNodes persists binary changes so the next supervisor restarts nodes
// with the same binaries.
func (s *supervisor) saveNodes() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	specs := make([]NodeSpec, len(s.nodes))
	for i, n := range s.nodes {
		specs[i] = n.currentSpec()
	}
	return SaveNodes(s.paths.Dir, specs)
}
