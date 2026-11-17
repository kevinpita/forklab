package supervisor

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
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
	// output appended to spec.LogPath. wait blocks until the process exits.
	start(spec NodeSpec) (pf pidFile, wait func() exitInfo, err error)
	// alive reports whether the process pf records still runs spec's node.
	alive(spec NodeSpec, pf pidFile) bool
	// watch blocks until a process this supervisor did not start exits. It
	// returns false when stop closes first.
	watch(spec NodeSpec, pf pidFile, stop <-chan struct{}) bool
	// signal sends sig to the process. One that is already gone is not an
	// error.
	signal(pf pidFile, sig syscall.Signal) error
}

// runtimeOf returns the runtime that runs spec's node.
func runtimeOf(NodeSpec) runtime { return hostRuntime{} }

// hostRuntime runs a node as a detached host process identified by its pid.
type hostRuntime struct{}

func (hostRuntime) start(spec NodeSpec) (pidFile, func() exitInfo, error) {
	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Dir = spec.Home
	if err := detach(cmd, spec.LogPath); err != nil {
		return pidFile{}, nil, err
	}
	wait := func() exitInfo {
		err := cmd.Wait()
		e := exitInfo{at: time.Now()}
		var exitErr *exec.ExitError
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			e.signal = ws.Signal().String()
		} else if err == nil || errors.As(err, &exitErr) {
			code := cmd.ProcessState.ExitCode()
			e.code = &code
		}
		return e
	}
	return pidFile{PID: cmd.Process.Pid, Binary: spec.Binary, StartedAt: time.Now()}, wait, nil
}

// alive checks the cmdline too, so a recycled pid, or a zombie whose cmdline
// is empty, counts as exited.
func (hostRuntime) alive(spec NodeSpec, pf pidFile) bool {
	return alive(pf.PID) && cmdlineMatches(pf.PID, pf.Binary, spec.Home)
}

// watch polls, since this supervisor cannot wait(2) on a process it did not
// start.
func (h hostRuntime) watch(spec NodeSpec, pf pidFile, stop <-chan struct{}) bool {
	for h.alive(spec, pf) {
		select {
		case <-stop:
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return true
}

func (hostRuntime) signal(pf pidFile, sig syscall.Signal) error {
	if err := syscall.Kill(pf.PID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
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
