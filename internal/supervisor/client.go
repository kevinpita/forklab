package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotRunning means no supervisor answers on the lab's socket.
var ErrNotRunning = errors.New("supervisor not running")

// Client talks to one lab's supervisor over its unix socket.
type Client struct {
	paths Paths
}

// Dial returns a client when labDir's supervisor answers, else an error
// wrapping ErrNotRunning.
func Dial(labDir string) (*Client, error) {
	c := &Client{paths: Paths{labDir}}
	if _, err := c.Status(); err != nil {
		return nil, fmt.Errorf("lab %s: %w", filepath.Base(labDir), ErrNotRunning)
	}
	return c, nil
}

func (c *Client) call(req Request) ([]NodeStatus, error) {
	resp, err := c.send(req)
	return resp.Nodes, err
}

func (c *Client) send(req Request) (Response, error) {
	conn, err := net.DialTimeout("unix", c.paths.Sock(), time.Second)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = conn.Close() }()
	wait := req.Timeout
	if wait <= 0 {
		wait = DefaultStopTimeout
	}
	_ = conn.SetDeadline(time.Now().Add(wait + killTimeout + 5*time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (c *Client) Status() ([]NodeStatus, error) { return c.call(Request{Op: OpStatus}) }

// Upgrade returns the pending upgrade and the last completed one, each nil
// when none, with every node's status.
func (c *Client) Upgrade() (Response, error) { return c.send(Request{Op: OpStatus}) }

// CompleteUpgrade ends the pending upgrade named name once every node runs
// its binary; it is a no-op when that upgrade is already completed.
func (c *Client) CompleteUpgrade(name string) (Response, error) {
	return c.send(Request{Op: OpComplete, Upgrade: &Upgrade{Name: name}})
}

// SetUpgrade makes u the pending upgrade, or clears it when u is nil. Nodes
// that already halted on u's plan are swapped at once when u wants it.
func (c *Client) SetUpgrade(u *Upgrade) ([]NodeStatus, error) {
	return c.call(Request{Op: OpUpgrade, Upgrade: u})
}

func (c *Client) Start(sel string) ([]NodeStatus, error) {
	return c.call(Request{Op: OpStart, Nodes: sel})
}

// Stop sends SIGTERM and waits up to timeout (zero: DefaultStopTimeout).
func (c *Client) Stop(sel string, timeout time.Duration) ([]NodeStatus, error) {
	return c.call(Request{Op: OpStop, Nodes: sel, Timeout: timeout})
}

func (c *Client) Kill(sel string) ([]NodeStatus, error) {
	return c.call(Request{Op: OpKill, Nodes: sel})
}

// Configure replaces the start arguments of stopped nodes. Home must remain
// in the arguments so process adoption can still identify them.
func (c *Client) Configure(sel string, args []string) ([]NodeStatus, error) {
	return c.call(Request{Op: OpConfigure, Nodes: sel, Args: args})
}

// Restart stops then starts sel; a non-empty binary becomes the node's
// binary from now on, and a non-empty version is recorded as the version
// the node runs.
func (c *Client) Restart(sel, binary, version string, timeout time.Duration) ([]NodeStatus, error) {
	return c.call(Request{Op: OpRestart, Nodes: sel, Binary: binary, Version: version, Timeout: timeout})
}

// Down stops every node, then the supervisor, and returns once the
// supervisor process is gone and its locks are free.
func (c *Client) Down(timeout time.Duration) ([]NodeStatus, error) {
	pid := c.supervisorPid()
	nodes, err := c.call(Request{Op: OpDown, Timeout: timeout})
	if err != nil {
		return nodes, err
	}
	return nodes, waitDead(pid)
}

// Exit stops the supervisor only; nodes keep running for the next one to
// adopt.
func (c *Client) Exit() error {
	pid := c.supervisorPid()
	if _, err := c.call(Request{Op: OpExit}); err != nil {
		return err
	}
	return waitDead(pid)
}

func (c *Client) supervisorPid() int {
	data, _ := os.ReadFile(c.paths.Lock())
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// waitDead waits for the supervisor process to exit, which is when the
// kernel releases its locks; the socket closes earlier than that.
func waitDead(pid int) error {
	deadline := time.Now().Add(killTimeout)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("supervisor pid %d still running after exit", pid)
}

// Spawner builds the command that runs a supervisor for labDir. EnsureRunning
// detaches it and points its output at supervisor.log.
type Spawner func(labDir string) (*exec.Cmd, error)

// ForklabSpawner runs this executable's hidden supervisor command.
func ForklabSpawner(labDir string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "supervisor", "run", "--lab", labDir), nil
}

// readyTimeout bounds how long EnsureRunning waits for a spawned supervisor.
const readyTimeout = 5 * time.Second

// EnsureRunning returns a client for labDir's supervisor, spawning one
// detached when none answers. Another lab holding active.lock is an error
// naming that lab.
func EnsureRunning(ctx context.Context, labDir string, spawn Spawner) (*Client, error) {
	labDir, err := filepath.Abs(labDir)
	if err != nil {
		return nil, err
	}
	if c, err := Dial(labDir); err == nil {
		return c, nil
	}
	if f, err := lockActive(labDir); err == nil {
		_ = f.Close()
	} else if !errors.Is(err, errLocked) {
		return nil, err
	}
	cmd, err := spawn(labDir)
	if err != nil {
		return nil, err
	}
	paths := Paths{labDir}
	cmd.Dir = labDir
	if err := detach(cmd, paths.Log()); err != nil {
		return nil, fmt.Errorf("start supervisor: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	timeout := time.After(readyTimeout)
	for {
		if c, err := Dial(labDir); err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-exited:
			// A clean exit is the quiet second instance: another supervisor
			// holds the lock and is coming up, so keep waiting for it.
			if err != nil {
				return nil, fmt.Errorf("supervisor exited: %w\n%s", err, lastLines(paths.Log(), 5))
			}
			exited = nil
		case <-timeout:
			return nil, fmt.Errorf("supervisor did not answer within %s; see %s\n%s", readyTimeout, paths.Log(), lastLines(paths.Log(), 5))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func lastLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (c *Client) RecoverUpgrade(expected, next *Upgrade) (Response, error) {
	return c.send(Request{Op: OpRecover, Expected: expected, Upgrade: next})
}

func (c *Client) FinishRecovery(expected *Upgrade) (Response, error) {
	return c.send(Request{Op: OpRecovered, Expected: expected})
}

func (c *Client) CompleteExpectedUpgrade(expected *Upgrade) (Response, error) {
	return c.send(Request{Op: OpComplete, Upgrade: expected, Expected: expected})
}
