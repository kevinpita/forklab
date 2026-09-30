package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/supervisor"
)

const stateFile = "pause.json"

// State retains original arguments before changing any process, so a failed
// or interrupted pause is recoverable with Resume, even after daemon adoption.
type State struct {
	Height int64      `json:"height"`
	Phase  string     `json:"phase"`
	Args   [][]string `json:"args"`
}

func Load(dir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func save(dir string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pause-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, stateFile))
}

func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "control.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another pause/resume operation is active: %w", err)
	}
	return func() { _ = f.Close() }, nil
}

// Controller uses the supervisor for every process change. On error it leaves
// the persisted barrier in place; it never silently resumes a debugging lab.
type Controller struct {
	Dir        string
	Supervisor *supervisor.Client
	Clients    []*chain.Client
	Strategy   HaltStrategy
}

func (c Controller) Pause(ctx context.Context, height int64) (*State, error) {
	unlock, err := lock(c.Dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if st, err := Load(c.Dir); err != nil {
		return nil, err
	} else if st != nil {
		return st, fmt.Errorf("lab already has a pause at %d (%s); use lab resume first", st.Height, st.Phase)
	}
	specs, err := supervisor.LoadNodes(c.Dir)
	if err != nil {
		return nil, err
	}
	if len(specs) != len(c.Clients) {
		return nil, fmt.Errorf("pause clients and nodes differ")
	}
	status, err := c.Supervisor.Upgrade()
	if err != nil {
		return nil, err
	}
	if status.Upgrade != nil {
		return nil, fmt.Errorf("finish or clear the pending upgrade before pausing")
	}
	strategy := c.Strategy
	if strategy == nil {
		strategy = SDKFinalizeHalt{}
	}
	args := make([][]string, len(specs))
	st := State{Height: height, Phase: "arming", Args: make([][]string, len(specs))}
	offsets := make([]int64, len(specs))
	for i, s := range specs {
		for _, arg := range s.Args {
			if strings.HasPrefix(arg, "--halt-height") || strings.HasPrefix(arg, "--halt-time") {
				return nil, fmt.Errorf("node %d already has halt arguments", i)
			}
		}
		halt, err := strategy.Arguments(ctx, s.Binary, height)
		if err != nil {
			return nil, err
		}
		st.Args[i] = slices.Clone(s.Args)
		args[i] = append(slices.Clone(s.Args), halt...)
		if h, err := c.Clients[i].AppInfo(ctx); err == nil && h.Height > height {
			return nil, fmt.Errorf("node %d already committed height %d, beyond requested %d", i, h.Height, height)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := save(c.Dir, st); err != nil {
		return nil, err
	}
	if _, err := c.Supervisor.Stop(supervisor.All, 0); err != nil {
		return &st, err
	}
	for i, s := range specs {
		if info, err := os.Stat(s.LogPath); err == nil {
			offsets[i] = info.Size()
		}
		if _, err := c.Supervisor.Configure(strconv.Itoa(i), args[i]); err != nil {
			return &st, err
		}
	}
	if err := ctx.Err(); err != nil {
		return &st, err
	}
	if _, err := c.Supervisor.Start(supervisor.All); err != nil {
		return &st, err
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		ready := true
		for i, s := range specs {
			status, err := c.Clients[i].AppInfo(ctx)
			if err != nil {
				ready = false
				continue
			}
			if status.Height > height {
				return &st, fmt.Errorf("exact pause failed: node %d committed %d instead of %d; barrier retained, use lab resume", i, status.Height, height)
			}
			if status.Height != height || !hasMarker(s.LogPath, offsets[i], strategy.Marker(height)) {
				ready = false
			}
		}
		if ready {
			st.Phase = "paused"
			return &st, save(c.Dir, st)
		}
		select {
		case <-ctx.Done():
			return &st, fmt.Errorf("pause at %d: %w; barrier retained, use lab resume", height, ctx.Err())
		case <-tick.C:
		}
	}
}

func hasMarker(path string, offset int64, marker string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Size()-offset > 128*1024 {
		offset = info.Size() - 128*1024
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, 128*1024))
	return err == nil && strings.Contains(string(data), marker)
}

func (c Controller) Resume(ctx context.Context) error {
	unlock, err := lock(c.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := Load(c.Dir)
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("lab is not paused")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	st.Phase = "resuming"
	if err := save(c.Dir, *st); err != nil {
		return err
	}
	heights := make([]int64, len(c.Clients))
	for i, rpc := range c.Clients {
		if status, err := rpc.AppInfo(ctx); err == nil {
			heights[i] = status.Height
		}
	}
	if _, err := c.Supervisor.Stop(supervisor.All, 0); err != nil {
		return err
	}
	specs, err := supervisor.LoadNodes(c.Dir)
	if err != nil {
		return err
	}
	if len(st.Args) != len(specs) {
		return fmt.Errorf("pause state does not match nodes")
	}
	for i, args := range st.Args {
		if _, err := c.Supervisor.Configure(strconv.Itoa(i), args); err != nil {
			return err
		}
	}
	if _, err := c.Supervisor.Start(supervisor.All); err != nil {
		return err
	}
	for i, rpc := range c.Clients {
		if heights[i] == 0 {
			h, err := rpc.WaitAppHeight(ctx, 1)
			if err != nil {
				return err
			}
			heights[i] = h
		}
		if _, err := rpc.WaitAppHeight(ctx, heights[i]+1); err != nil {
			return fmt.Errorf("resume: %w; recovery state retained", err)
		}
	}
	return os.Remove(filepath.Join(c.Dir, stateFile))
}

// Clear restores normal arguments for a stopped lab before resetting data.
func Clear(dir string) error {
	st, err := Load(dir)
	if err != nil || st == nil {
		return err
	}
	specs, err := supervisor.LoadNodes(dir)
	if err != nil {
		return err
	}
	if len(st.Args) != len(specs) {
		return fmt.Errorf("pause state does not match nodes")
	}
	for i := range specs {
		specs[i].Args = st.Args[i]
	}
	if err := supervisor.SaveNodes(dir, specs); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, stateFile))
}
