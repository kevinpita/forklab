package supervisor

import (
	"bytes"
	"cmp"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// runtime is how a node's process runs. What identifies a started process
// is whatever start puts in the pidFile, which the node persists so a later
// supervisor can ask about the same process.
type runtime interface {
	// start launches spec's node so it outlives the supervisor, with its
	// output appended to spec.LogPath. wait blocks until the process exits,
	// or returns false once stop closes: the node then runs on unwatched.
	start(spec NodeSpec) (pf pidFile, wait func(stop <-chan struct{}) (exitInfo, bool), err error)
	// alive reports whether the process pf records still runs spec's node.
	// An error means the runtime could not find out: the node may be
	// running, and must not be treated as stopped.
	alive(spec NodeSpec, pf pidFile) (bool, error)
	// watch blocks until a process this supervisor did not start exits. It
	// returns false when stop closes first.
	watch(spec NodeSpec, pf pidFile, stop <-chan struct{}) bool
	// signal sends sig to the process pf records. One that is already gone
	// is not an error. adopted says that this supervisor did not start the
	// process: what pf records may then belong to another process by now,
	// and is signalled only while it still runs spec's node. A process the
	// supervisor started is its own child and is signalled as it is.
	signal(spec NodeSpec, pf pidFile, sig syscall.Signal, adopted bool) error
	// reap ends what a node left running that no pid file records.
	reap(spec NodeSpec)
}

// runtimeOf returns the runtime that runs spec's node.
func runtimeOf(spec NodeSpec) runtime {
	if spec.Container != nil {
		return dockerRuntime{}
	}
	return hostRuntime{}
}

// hostRuntime runs a node as a detached host process identified by its pid.
// Under cosmovisor that process is cosmovisor and the chain binary is its
// child in the same process group.
type hostRuntime struct{}

func (hostRuntime) start(spec NodeSpec) (pidFile, func(<-chan struct{}) (exitInfo, bool), error) {
	pf := pidFile{Binary: spec.Binary}
	cmd := exec.Command(spec.Binary, spec.Args...)
	if cv := spec.Cosmovisor; cv != nil {
		killOrphans(spec)
		pf.Exec = cv.Path
		cmd = exec.Command(cv.Path, append([]string{"run"}, spec.Args...)...)
		cmd.Env = cv.environ(spec.Home, os.Environ())
	}
	cmd.Dir = spec.Home
	if err := detach(cmd, spec.LogPath); err != nil {
		return pidFile{}, nil, err
	}
	exit := make(chan exitInfo, 1)
	go func() {
		err := cmd.Wait()
		e := exitInfo{at: time.Now()}
		var exitErr *exec.ExitError
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			e.signal = ws.Signal().String()
		} else if err == nil || errors.As(err, &exitErr) {
			code := cmd.ProcessState.ExitCode()
			e.code = &code
		}
		if spec.Cosmovisor != nil {
			killOrphans(spec)
		}
		exit <- e
	}()
	wait := func(stop <-chan struct{}) (exitInfo, bool) {
		select {
		case e := <-exit:
			return e, true
		case <-stop:
			return exitInfo{}, false
		}
	}
	pf.PID, pf.StartedAt = cmd.Process.Pid, time.Now()
	return pf, wait, nil
}

// alive checks the cmdline too, so a recycled pid, or a zombie whose cmdline
// is empty, counts as exited. A cosmovisor node is also alive while the
// chain binary of a cosmovisor that died still runs: it holds the node's
// database and ports, so the lab is not down. The kernel always answers, so
// it never fails.
func (h hostRuntime) alive(spec NodeSpec, pf pidFile) (bool, error) {
	return h.supervised(spec, pf) || (spec.Cosmovisor != nil && len(chainProcesses(spec)) > 0), nil
}

// supervised reports whether the process the pid file records runs.
func (hostRuntime) supervised(spec NodeSpec, pf pidFile) bool {
	return alive(pf.PID) && cmdlineMatches(pf.PID, cmp.Or(pf.Exec, pf.Binary), spec.Home)
}

// watch polls, since this supervisor cannot wait(2) on a process it did not
// start. Once cosmovisor is gone, a chain binary it left behind is ended:
// nothing would stop or restart it otherwise.
func (h hostRuntime) watch(spec NodeSpec, pf pidFile, stop <-chan struct{}) bool {
	for h.supervised(spec, pf) {
		select {
		case <-stop:
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	if spec.Cosmovisor != nil {
		killOrphans(spec)
	}
	return true
}

// signal reaches cosmovisor's whole process group with SIGKILL, which
// cosmovisor cannot forward to the chain binary. The node is a session
// leader, so its pid is the group id. SIGTERM goes to cosmovisor alone: it
// forwards the signal and waits for the chain binary. An adopted pid is
// checked first, since the kernel may have given it to another process. A
// child is not: its pid stays its own until it is waited for, and its
// command line need not name the binary any more, for example when that
// is a wrapper script that execs the real one.
func (h hostRuntime) signal(spec NodeSpec, pf pidFile, sig syscall.Signal, adopted bool) error {
	if adopted && !h.supervised(spec, pf) {
		return nil
	}
	pid := pf.PID
	if pf.Exec != "" && sig == syscall.SIGKILL {
		pid = -pid
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// reap kills the chain binary of a cosmovisor that is gone: a supervisor
// that died before it wrote the pid file, or a child that could not be
// killed when cosmovisor exited, leaves one that nothing else would stop.
func (hostRuntime) reap(spec NodeSpec) {
	if spec.Cosmovisor != nil {
		killOrphans(spec)
	}
}

// chainProcesses lists the processes whose executable is in the node's
// cosmovisor tree: the chain binary cosmovisor runs. Cosmovisor resolves
// symlinks before it starts the binary, so the tree is looked for under
// its resolved path too.
func chainProcesses(spec NodeSpec) []int {
	root := spec.cosmovisorDir().root()
	prefixes := [][]byte{[]byte(root + "/")}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		prefixes = append(prefixes, []byte(resolved+"/"))
	}
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if slices.ContainsFunc(prefixes, func(p []byte) bool { return bytes.HasPrefix(cmdline, p) }) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// killOrphans ends the chain processes a cosmovisor that died abnormally
// left behind, which still hold the node's ports and database. It returns
// once they are gone. One it is not allowed to kill is logged and not
// waited for: the start that follows fails on the taken ports and says so.
func killOrphans(spec NodeSpec) {
	denied := map[int]bool{}
	for deadline := time.Now().Add(killTimeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		pending := false
		for _, pid := range chainProcesses(spec) {
			if denied[pid] {
				continue
			}
			if err := orphanKill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				denied[pid] = true
				log.Printf("node %d: cannot kill pid %d, which a cosmovisor left running out of %s: %v", spec.Index, pid, spec.cosmovisorDir().root(), err)
				continue
			}
			pending = true
		}
		if !pending {
			return
		}
	}
}

// orphanKill is kill(2); a test replaces it to refuse.
var orphanKill = syscall.Kill

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
