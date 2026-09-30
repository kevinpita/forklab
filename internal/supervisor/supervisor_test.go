package supervisor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/supervisor"
)

// harness gives one test its own FORKLAB_HOME, so active.lock is private
// to it.
type harness struct{ t *testing.T }

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("FORKLAB_HOME", filepath.Join(t.TempDir(), "forklab-home"))
	return &harness{t: t}
}

type lab struct {
	dir   string
	specs []supervisor.NodeSpec
}

// lab writes nodes.json for n fake nodes and registers a cleanup that kills
// whatever the lab's pid files still point at.
func (h *harness) lab(n int, nodeArgs ...string) *lab {
	h.t.Helper()
	dir := filepath.Join(h.t.TempDir(), "lab")
	l := &lab{dir: dir}
	for i := range n {
		home := filepath.Join(dir, "node"+strconv.Itoa(i))
		if err := os.MkdirAll(home, 0o755); err != nil {
			h.t.Fatal(err)
		}
		l.specs = append(l.specs, supervisor.NodeSpec{
			Index:   i,
			Name:    "node" + strconv.Itoa(i),
			Binary:  os.Args[0],
			Args:    append([]string{"fake-node", "--home", home}, nodeArgs...),
			Home:    home,
			LogPath: filepath.Join(home, "node.log"),
			PidPath: filepath.Join(home, "node.pid"),
		})
	}
	if err := supervisor.SaveNodes(dir, l.specs); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		killLab(l)
		if h.t.Failed() {
			log, _ := os.ReadFile(supervisor.Paths{Dir: dir}.Log())
			h.t.Logf("supervisor.log:\n%s", log)
		}
	})
	return l
}

// killLab SIGKILLs the supervisor and every node whose pid file names a
// process whose cmdline mentions the lab, whatever binary path it runs.
func killLab(l *lab) {
	kill := func(pid int, marker string) {
		if pid > 0 && pid != os.Getpid() && cmdlineHas(pid, marker) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if data, err := os.ReadFile(supervisor.Paths{Dir: l.dir}.Lock()); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		kill(pid, l.dir)
	}
	for _, s := range l.specs {
		if data, err := os.ReadFile(s.PidPath); err == nil {
			var pf struct {
				PID int `json:"pid"`
			}
			_ = json.Unmarshal(data, &pf)
			kill(pf.PID, s.Home)
		}
	}
}

func cmdlineHas(pid int, s string) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && strings.Contains(string(data), s)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func supervisorPid(t *testing.T, l *lab) int {
	t.Helper()
	data, err := os.ReadFile(supervisor.Paths{Dir: l.dir}.Lock())
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || !alive(pid) {
		t.Fatalf("supervisor.lock holds %q, want a live pid", data)
	}
	return pid
}

func up(t *testing.T, l *lab) *supervisor.Client {
	t.Helper()
	c, err := supervisor.EnsureRunning(context.Background(), l.dir, testSpawner)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitUpTo(t, 5*time.Second, what, cond)
}

func waitUpTo(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// mustStart starts sel and waits until each selected node has logged its
// start line, so a signal sent next reaches an installed handler.
func mustStart(t *testing.T, c *supervisor.Client, l *lab, sel string) []supervisor.NodeStatus {
	t.Helper()
	nodes, err := c.Start(sel)
	if err != nil {
		t.Fatalf("start %s: %v", sel, err)
	}
	for _, n := range nodes {
		if sel != supervisor.All && strconv.Itoa(n.Index) != sel {
			continue
		}
		if n.State != supervisor.StateRunning || !alive(n.PID) {
			t.Fatalf("after start, node %d = %+v, want running with a live pid\n%s", n.Index, n, logOf(l, n.Index))
		}
		ready := fmt.Sprintf("fake node %d starting in %s", n.PID, l.specs[n.Index].Home)
		waitFor(t, "node "+strconv.Itoa(n.Index)+" to log its start", func() bool { return strings.Contains(logOf(l, n.Index), ready) })
	}
	return nodes
}

func logOf(l *lab, i int) string {
	data, _ := os.ReadFile(l.specs[i].LogPath)
	return string(data)
}

func TestSupervisorOutlivesTheClientThatStartedIt(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	client := exec.Command(os.Args[0], "ensure", l.dir)
	client.Env = append(os.Environ(), childEnv+"=1")
	if out, err := client.CombinedOutput(); err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
	c, err := supervisor.Dial(l.dir)
	if err != nil {
		t.Fatalf("after the client exited: %v", err)
	}
	nodes, err := c.Status()
	if err != nil || len(nodes) != 1 || nodes[0].State != supervisor.StateStopped || nodes[0].Binary != os.Args[0] {
		t.Fatalf("status = %+v, %v", nodes, err)
	}
	if !alive(supervisorPid(t, l)) {
		t.Fatal("supervisor died with its client")
	}
}

func TestSecondSupervisorExitsQuietly(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	c := up(t, l)
	first := supervisorPid(t, l)

	second := exec.Command(os.Args[0], "supervisor-run", l.dir)
	second.Env = append(os.Environ(), childEnv+"=1")
	out, err := second.CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Fatalf("second supervisor: err %v, output %q; want exit 0 and silence", err, out)
	}
	if got := supervisorPid(t, l); got != first {
		t.Errorf("supervisor pid changed from %d to %d", first, got)
	}
	if _, err := c.Status(); err != nil {
		t.Errorf("first supervisor stopped answering: %v", err)
	}
}

func TestNodeLifecycle(t *testing.T) {
	h := newHarness(t)
	l := h.lab(2)
	c := up(t, l)

	nodes, err := c.Status()
	if err != nil || len(nodes) != 2 {
		t.Fatalf("status = %+v, %v", nodes, err)
	}
	for _, n := range nodes {
		if n.State != supervisor.StateStopped || n.PID != 0 || n.Binary != os.Args[0] {
			t.Fatalf("fresh node = %+v, want stopped", n)
		}
	}

	nodes = mustStart(t, c, l, supervisor.All)
	pid0, pid1 := nodes[0].PID, nodes[1].PID
	waitFor(t, "node output in its log", func() bool { return strings.Contains(logOf(l, 1), "tick 2") })

	nodes, err = c.Stop("0", 2*time.Second)
	if err != nil {
		t.Fatalf("stop 0: %v", err)
	}
	if n := nodes[0]; n.State != supervisor.StateExited || n.ExitCode == nil || *n.ExitCode != 0 || n.Signal != "" || n.ExitedAt == nil {
		t.Errorf("after SIGTERM node 0 = %+v, want exited with code 0", n)
	}
	if alive(pid0) {
		t.Errorf("pid %d still alive after stop", pid0)
	}
	if _, err := os.Stat(l.specs[0].PidPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pid file of stopped node still exists: %v", err)
	}
	if !strings.Contains(logOf(l, 0), "bye") {
		t.Errorf("node 0 did not get SIGTERM:\n%s", logOf(l, 0))
	}
	if nodes[1].State != supervisor.StateRunning || nodes[1].PID != pid1 {
		t.Errorf("stop 0 touched node 1: %+v", nodes[1])
	}

	nodes, err = c.Kill("1")
	if err != nil {
		t.Fatalf("kill 1: %v", err)
	}
	if n := nodes[1]; n.State != supervisor.StateExited || n.Signal != "killed" || n.ExitCode != nil {
		t.Errorf("after SIGKILL node 1 = %+v, want exited by signal killed", n)
	}
	if alive(pid1) {
		t.Errorf("pid %d still alive after kill", pid1)
	}

	nodes = mustStart(t, c, l, "1")
	if nodes[0].State != supervisor.StateExited {
		t.Errorf("start 1 touched node 0: %+v", nodes[0])
	}

	if err := syscall.Kill(nodes[1].PID, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "crash to be recorded", func() bool {
		nodes, _ := c.Status()
		n := nodes[1]
		return n.State == supervisor.StateExited && n.ExitCode != nil && *n.ExitCode == 3
	})

	nodes, err = c.Restart("1", "", "", 2*time.Second)
	if err != nil || nodes[1].State != supervisor.StateRunning {
		t.Fatalf("restart after crash: %+v, %v", nodes[1], err)
	}
}

func TestStopTimeoutReportsAndKillEnds(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1, "--ignore-term")
	c := up(t, l)
	pid := mustStart(t, c, l, supervisor.All)[0].PID

	nodes, err := c.Stop("0", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("stop of a node ignoring SIGTERM: err %v, want a timeout report", err)
	}
	if nodes[0].State != supervisor.StateRunning || !alive(pid) {
		t.Fatalf("node = %+v, want still running", nodes[0])
	}
	waitFor(t, "node to log the ignored signal", func() bool { return strings.Contains(logOf(l, 0), "ignoring SIGTERM") })

	nodes, err = c.Kill("0")
	if err != nil || nodes[0].State != supervisor.StateExited || nodes[0].Signal != "killed" {
		t.Fatalf("kill: %+v, %v", nodes[0], err)
	}
	if alive(pid) {
		t.Errorf("pid %d survived SIGKILL", pid)
	}
}

func TestNewSupervisorAdoptsNodesAfterKill9(t *testing.T) {
	h := newHarness(t)
	l := h.lab(2)
	c := up(t, l)
	started := mustStart(t, c, l, supervisor.All)
	old := supervisorPid(t, l)

	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	for _, n := range started {
		if !alive(n.PID) {
			t.Fatalf("node %d died with the supervisor", n.Index)
		}
	}

	c = up(t, l)
	if supervisorPid(t, l) == old {
		t.Fatal("no new supervisor was spawned")
	}
	nodes, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range nodes {
		want := started[i]
		if n.State != supervisor.StateRunning || !n.Adopted || n.PID != want.PID || n.Binary != os.Args[0] {
			t.Errorf("node %d = %+v, want adopted pid %d", i, n, want.PID)
		}
		if n.StartedAt == nil || !n.StartedAt.Equal(*want.StartedAt) {
			t.Errorf("node %d started_at = %v, want the original %v", i, n.StartedAt, want.StartedAt)
		}
	}
	nodes, err = c.Start(supervisor.All)
	if err != nil {
		t.Fatalf("start all with every node adopted: %v", err)
	}
	for i, n := range nodes {
		if n.PID != started[i].PID {
			t.Errorf("start all replaced adopted node %d: %+v", i, n)
		}
	}

	nodes, err = c.Stop(supervisor.All, 2*time.Second)
	if err != nil {
		t.Fatalf("stop adopted nodes: %v", err)
	}
	for i, n := range nodes {
		if n.State != supervisor.StateExited || n.ExitCode != nil || alive(started[i].PID) {
			t.Errorf("adopted node %d after stop = %+v, want exited with unknown code and a dead pid %d", i, n, started[i].PID)
		}
		if _, err := os.Stat(l.specs[i].PidPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("node %d pid file still exists", i)
		}
	}
}

func TestPidFileOfAnotherProcessIsNotAdopted(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	// The test process runs this binary but not with the node's home, so the
	// pid file looks plausible and must still be rejected.
	stale := fmt.Sprintf(`{"pid":%d,"binary":%q,"started_at":"2026-01-01T00:00:00Z"}`, os.Getpid(), os.Args[0])
	if err := os.WriteFile(l.specs[0].PidPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	c := up(t, l)
	nodes, err := c.Status()
	if err != nil || nodes[0].State != supervisor.StateExited || nodes[0].PID != 0 || nodes[0].ExitCode != nil {
		t.Fatalf("node = %+v, %v; want exited with unknown code, not adopted", nodes[0], err)
	}
	if _, err := os.Stat(l.specs[0].PidPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale pid file was kept: %v", err)
	}
	if _, err := c.Kill("0"); err != nil {
		t.Errorf("kill of a stopped node: %v", err)
	}
}

func TestRestartWithBinaryOverridePersists(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	c := up(t, l)
	pid := mustStart(t, c, l, supervisor.All)[0].PID
	v2 := filepath.Join(t.TempDir(), "fake-node-v2")
	if err := os.Symlink(os.Args[0], v2); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Restart("0", filepath.Join(t.TempDir(), "missing"), "", time.Second); err == nil {
		t.Fatal("restart with a missing binary succeeded")
	}
	if nodes, _ := c.Status(); nodes[0].PID != pid {
		t.Fatalf("a rejected restart touched the node: %+v", nodes[0])
	}

	nodes, err := c.Restart("0", v2, "", 2*time.Second)
	if err != nil || nodes[0].State != supervisor.StateRunning || nodes[0].Binary != v2 || nodes[0].PID == pid {
		t.Fatalf("restart with %s: %+v, %v", v2, nodes[0], err)
	}
	specs, err := supervisor.LoadNodes(l.dir)
	if err != nil || specs[0].Binary != v2 {
		t.Errorf("nodes.json binary = %q, %v; want %s", specs[0].Binary, err, v2)
	}

	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	c = up(t, l)
	nodes, err = c.Status()
	if err != nil || !nodes[0].Adopted || nodes[0].Binary != v2 {
		t.Errorf("after adoption node = %+v, %v; want binary %s", nodes[0], err, v2)
	}
}

// Only later restarts are timed: the first stops a -race fake node, which
// sleeps a second at exit.
func TestRestartOnABinaryThatExitsAtOnceKeepsTheOldOne(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	c := up(t, l)
	mustStart(t, c, l, supervisor.All)
	bad := filepath.Join(t.TempDir(), "bad-node")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		start := time.Now()
		nodes, err := c.Restart("0", bad, "", 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "keeping "+os.Args[0]) {
			t.Fatalf("restart %d on %s: %v, %+v; want it refused", i, bad, err, nodes)
		}
		if took := time.Since(start); i > 0 && took > 900*time.Millisecond {
			t.Fatalf("restart %d took %s; an instant exit should not wait out the probe", i, took)
		}
		specs, err := supervisor.LoadNodes(l.dir)
		if err != nil || specs[0].Binary != os.Args[0] {
			t.Fatalf("nodes.json binary = %q, %v; want %s", specs[0].Binary, err, os.Args[0])
		}
	}
}

func TestSecondLabIsRefusedWhileOneIsActive(t *testing.T) {
	h := newHarness(t)
	a := h.lab(1)
	b := h.lab(1)
	c := up(t, a)

	_, err := supervisor.EnsureRunning(context.Background(), b.dir, testSpawner)
	var active supervisor.ErrLabActive
	if !errors.As(err, &active) || active.LabDir != a.dir {
		t.Fatalf("starting lab b while a runs: %v, want ErrLabActive naming %s", err, a.dir)
	}
	if _, err := os.Stat(supervisor.Paths{Dir: b.dir}.Lock()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a supervisor was spawned for lab b anyway")
	}

	if _, err := c.Down(time.Second); err != nil {
		t.Fatalf("down a: %v", err)
	}
	if _, err := supervisor.EnsureRunning(context.Background(), b.dir, testSpawner); err != nil {
		t.Fatalf("lab b after lab a went down: %v", err)
	}
}

func TestDownStopsEverythingEvenNodesIgnoringSigterm(t *testing.T) {
	h := newHarness(t)
	l := h.lab(2, "--ignore-term")
	c := up(t, l)
	started := mustStart(t, c, l, supervisor.All)
	spid := supervisorPid(t, l)

	nodes, err := c.Down(300 * time.Millisecond)
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	for i, n := range nodes {
		if n.State != supervisor.StateExited || n.Signal != "killed" || alive(started[i].PID) {
			t.Errorf("node %d after down = %+v, want killed", i, n)
		}
	}
	waitFor(t, "supervisor to exit", func() bool { return !alive(spid) })
	if _, err := os.Stat(supervisor.Paths{Dir: l.dir}.Sock()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket left behind: %v", err)
	}
	if _, err := supervisor.Dial(l.dir); !errors.Is(err, supervisor.ErrNotRunning) {
		t.Errorf("Dial after down: %v, want ErrNotRunning", err)
	}
}

type lineLog struct {
	mu    sync.Mutex
	lines []string
}

func (s *lineLog) NodeLine(index int, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, fmt.Sprintf("%d: %s", index, line))
}

func (s *lineLog) has(line string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if l == line {
			return true
		}
	}
	return false
}

func TestSubscriberSeesNodeLinesAndSignalExitLeavesNodesRunning(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	sub := &lineLog{}
	// The in-process supervisor spawns nodes with this process's environment.
	t.Setenv(childEnv, "1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- supervisor.Run(ctx, supervisor.Options{LabDir: l.dir, Subscriber: sub, Log: io.Discard})
	}()
	var c *supervisor.Client
	waitFor(t, "in-process supervisor", func() bool {
		var err error
		c, err = supervisor.Dial(l.dir)
		return err == nil
	})
	pid := mustStart(t, c, l, supervisor.All)[0].PID
	waitFor(t, "subscriber to receive node lines", func() bool { return sub.has("0: tick 3") })
	if !sub.has(fmt.Sprintf("0: fake node %d starting in %s", pid, l.specs[0].Home)) {
		t.Errorf("subscriber missed the first line; got %v", sub.lines)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	before := logOf(l, 0)
	waitFor(t, "the node to keep ticking after the supervisor left", func() bool { return len(logOf(l, 0)) > len(before) })
	if !alive(pid) {
		t.Error("supervisor exit on signal killed the node")
	}
	if _, err := os.Stat(supervisor.Paths{Dir: l.dir}.Sock()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket left behind: %v", err)
	}
}

func TestLoadNodesRejectsBadFiles(t *testing.T) {
	good := supervisor.NodeSpec{Index: 0, Name: "node0", Binary: "/bin/x", Args: []string{"start", "--home", "/h"}, Home: "/h", LogPath: "/h/log", PidPath: "/h/pid"}
	tests := []struct {
		name  string
		specs []supervisor.NodeSpec
		want  string
	}{
		{"empty", nil, "no nodes"},
		{"index gap", []supervisor.NodeSpec{good, func() supervisor.NodeSpec { s := good; s.Index = 2; return s }()}, "node 1 has index 2"},
		{"missing pid path", []supervisor.NodeSpec{func() supervisor.NodeSpec { s := good; s.PidPath = ""; return s }()}, "needs binary, home, log_path, and pid_path"},
		{"home not in args", []supervisor.NodeSpec{func() supervisor.NodeSpec { s := good; s.Args = []string{"start"}; return s }()}, "do not mention its home"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := supervisor.SaveNodes(dir, tt.specs); err != nil {
				t.Fatal(err)
			}
			_, err := supervisor.LoadNodes(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	dir := t.TempDir()
	if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{good}); err != nil {
		t.Fatal(err)
	}
	specs, err := supervisor.LoadNodes(dir)
	if err != nil || len(specs) != 1 || specs[0].Name != "node0" {
		t.Errorf("round trip = %+v, %v", specs, err)
	}
}

// sublogLines returns the "<index>: <line>" entries a child supervisor's
// subscriber wrote.
func sublogLines(path string) []string {
	data, _ := os.ReadFile(path)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// ticksOf returns the tick numbers node index delivered, in order.
func ticksOf(lines []string, index int) []int {
	var ticks []int
	for _, l := range lines {
		var i, n int
		if _, err := fmt.Sscanf(l, "%d: tick %d", &i, &n); err == nil && i == index {
			ticks = append(ticks, n)
		}
	}
	return ticks
}

func TestSubscriberMissesNoLineAcrossSupervisorRestart(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	sublog := filepath.Join(t.TempDir(), "sublog")
	t.Setenv(sublogEnv, sublog)
	c := up(t, l)
	pid := mustStart(t, c, l, supervisor.All)[0].PID
	waitFor(t, "the subscriber to see ticks", func() bool { return len(ticksOf(sublogLines(sublog), 0)) >= 3 })

	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	time.Sleep(400 * time.Millisecond)
	c = up(t, l)
	if nodes, _ := c.Status(); nodes[0].PID != pid || !nodes[0].Adopted {
		t.Fatalf("node was not adopted: %+v", nodes[0])
	}
	var ticks []int
	waitFor(t, "ticks after adoption", func() bool {
		ticks = ticksOf(sublogLines(sublog), 0)
		return len(ticks) > 0 && ticks[len(ticks)-1] >= 20
	})
	next := 0
	for _, n := range ticks {
		if n > next {
			t.Fatalf("subscriber missed ticks %d..%d across the supervisor restart: %v", next, n-1, ticks)
		}
		if n == next {
			next++
		}
	}
	if _, err := c.Stop(supervisor.All, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if lines := sublogLines(sublog); lines[len(lines)-1] != "0: bye" {
		t.Errorf("last delivered line = %q, want the node's farewell", lines[len(lines)-1])
	}
}

func TestNodeDyingWhileUnsupervisedIsReportedExited(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1)
	sublog := filepath.Join(t.TempDir(), "sublog")
	t.Setenv(sublogEnv, sublog)
	c := up(t, l)
	pid := mustStart(t, c, l, supervisor.All)[0].PID
	waitFor(t, "the subscriber to see ticks", func() bool { return len(ticksOf(sublogLines(sublog), 0)) >= 2 })

	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node to crash", func() bool { return !alive(pid) })

	c = up(t, l)
	nodes, err := c.Status()
	if err != nil || nodes[0].State != supervisor.StateExited || nodes[0].ExitCode != nil || nodes[0].ExitedAt == nil {
		t.Fatalf("node = %+v, %v; want exited with unknown code", nodes[0], err)
	}
	if _, err := os.Stat(l.specs[0].PidPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pid file of the dead node was kept: %v", err)
	}
	lines := sublogLines(sublog)
	if lines[len(lines)-1] != "0: crashing" {
		t.Errorf("last delivered line = %q, want the crash message; lines %v", lines[len(lines)-1], lines)
	}
	if nodes, err := c.Start(supervisor.All); err != nil || nodes[0].State != supervisor.StateRunning {
		t.Errorf("start after the unsupervised exit: %+v, %v", nodes, err)
	}
}
