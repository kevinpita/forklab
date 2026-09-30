package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// Upgrade is the software upgrade the supervisor watches for. Binary is the
// absolute path the nodes switch to and Version its profile version,
// recorded in lab.yaml through Options.RecordVersion.
type Upgrade struct {
	Name     string `json:"name"`
	Height   int64  `json:"height"`
	Version  string `json:"version"`
	Binary   string `json:"binary"`
	AutoSwap bool   `json:"auto_swap"`
	// ProposalID is the gov proposal that schedules the plan, once known.
	ProposalID uint64 `json:"proposal_id,omitempty"`
}

// upgradeState is <lab>/upgrade.json: the pending plan and the halt each
// node logged, by index. The halts are kept because the log offset moves
// past a halt line long before the swap changes anything, so a supervisor
// started in between would otherwise never see it.
type upgradeState struct {
	Plan  *Upgrade     `json:"plan,omitempty"`
	Halts map[int]Halt `json:"halts,omitempty"`
}

func (p Paths) Upgrade() string { return filepath.Join(p.Dir, "upgrade.json") }

func loadUpgradeState(labDir string) (upgradeState, error) {
	path := Paths{labDir}.Upgrade()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return upgradeState{}, nil
	}
	if err != nil {
		return upgradeState{}, err
	}
	var st upgradeState
	if err := json.Unmarshal(data, &st); err != nil {
		return upgradeState{}, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

// LoadUpgrade reads the pending plan from upgrade.json; nil when none.
func LoadUpgrade(labDir string) (*Upgrade, error) {
	st, err := loadUpgradeState(labDir)
	return st.Plan, err
}

// saveUpgradeState writes upgrade.json, or removes it when there is
// nothing to keep.
func saveUpgradeState(labDir string, st upgradeState) error {
	path := Paths{labDir}.Upgrade()
	if st.Plan == nil && len(st.Halts) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// Halt is what a node logs when it reaches a plan height its binary has no
// handler for: `UPGRADE "<name>" NEEDED at height: <h>`. Binary is the
// binary that halted; the halt is over once the node runs another one.
type Halt struct {
	Name   string `json:"name"`
	Height int64  `json:"height"`
	Binary string `json:"binary,omitempty"`
}

// sameHalt reports whether two halts are the same event, whichever binary
// each recorded.
func sameHalt(a, b Halt) bool { return a.Name == b.Name && a.Height == b.Height }

var (
	ansi     = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	haltLine = regexp.MustCompile(`UPGRADE "([^"]+)" NEEDED at height: ([0-9]+)`)
)

// StripANSI removes terminal color codes from a log line.
func StripANSI(line string) string { return ansi.ReplaceAllString(line, "") }

// MatchHalt reads the halt out of a node log line, colored or not. The
// lines CometBFT logs afterwards quote the same message with escaped
// quotes, so they do not match.
func MatchHalt(line string) (Halt, bool) {
	m := haltLine.FindStringSubmatch(StripANSI(line))
	if m == nil {
		return Halt{}, false
	}
	h, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return Halt{}, false
	}
	return Halt{Name: m[1], Height: h}, true
}

// SwapPhase is where a node stands in the pending upgrade.
type SwapPhase string

const (
	SwapNone    SwapPhase = ""
	SwapHalted  SwapPhase = "halted"
	SwapRunning SwapPhase = "swapping"
	SwapDone    SwapPhase = "swapped"
	// SwapFailed: the restart on the new binary failed; see SwapError.
	SwapFailed SwapPhase = "swap_failed"
)

// haltExitWait is how long a halted node gets to exit on its own before it
// is stopped. A CometBFT node stays up after a consensus failure, so the
// wait is short.
const haltExitWait = 5 * time.Second

// markHalted records a halt of the binary the node runs and reports
// whether it is new. A replayed line for a halt already recorded changes
// nothing, and a swap in progress owns the node.
func (n *node) markHalted(h Halt) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.swap == SwapRunning || (n.halt != nil && sameHalt(*n.halt, h)) {
		return false
	}
	h.Binary = n.spec.Binary
	if n.proc != nil {
		h.Binary = n.proc.binary
	}
	n.halt, n.swap, n.swapErr = &h, SwapHalted, ""
	return true
}

// beginSwap moves a node halted on plan name to SwapRunning. The caller
// holds ops.
func (n *node) beginSwap(name string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.swap != SwapHalted || n.halt.Name != name {
		return false
	}
	n.swap = SwapRunning
	return true
}

// setSwap sets the phase; SwapNone also forgets the halt.
func (n *node) setSwap(phase SwapPhase, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.swap, n.swapErr = phase, ""
	if err != nil {
		n.swapErr = err.Error()
	}
	if phase == SwapNone {
		n.halt = nil
	}
}

func (n *node) swapState() (SwapPhase, *Halt) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.swap, n.halt
}

func (s *supervisor) pending() *Upgrade {
	s.upMu.Lock()
	defer s.upMu.Unlock()
	return s.upgrade
}

// saveState writes the plan and every node's halt to upgrade.json.
func (s *supervisor) saveState() {
	s.upMu.Lock()
	defer s.upMu.Unlock()
	st := upgradeState{Plan: s.upgrade}
	for _, n := range s.nodes {
		if _, h := n.swapState(); h != nil {
			if st.Halts == nil {
				st.Halts = map[int]Halt{}
			}
			st.Halts[n.spec.Index] = *h
		}
	}
	if err := saveUpgradeState(s.paths.Dir, st); err != nil {
		s.log.Printf("upgrade.json: %v", err)
	}
}

// NodeLine forwards every line to the configured subscriber and reacts to
// halts. It never blocks: the swap runs in its own goroutine.
func (s *supervisor) NodeLine(index int, line string) {
	if s.forward != nil {
		s.forward.NodeLine(index, line)
	}
	h, ok := MatchHalt(line)
	if !ok {
		return
	}
	n := s.nodes[index]
	if !n.markHalted(h) {
		return
	}
	s.log.Printf("node %d: halted for upgrade %q at height %d", index, h.Name, h.Height)
	s.saveState()
	s.swapIfDue(n)
}

// swapIfDue starts the swap of a halted node when the pending upgrade
// names its plan and wants auto swap.
func (s *supervisor) swapIfDue(n *node) {
	up := s.pending()
	phase, h := n.swapState()
	if up == nil || phase != SwapHalted || !up.AutoSwap {
		return
	}
	if h.Name != up.Name {
		s.log.Printf("node %d: halted on %q, but the pending upgrade is %q; not swapping", n.spec.Index, h.Name, up.Name)
		return
	}
	go s.swap(n, *up)
}

// swap restarts a halted node on the upgrade binary. beginSwap admits only
// the first of several swaps started for the same halt.
func (s *supervisor) swap(n *node, up Upgrade) {
	if p := n.running(); p != nil {
		select {
		case <-p.gone:
		case <-time.After(haltExitWait):
		}
	}
	n.ops.Lock()
	defer n.ops.Unlock()
	if s.closing.Load() || !n.beginSwap(up.Name) {
		return
	}
	index := n.spec.Index
	s.log.Printf("node %d: swapping to %s", index, up.Binary)
	err := n.signal(syscall.SIGTERM, DefaultStopTimeout)
	if err != nil {
		s.log.Printf("node %d: %v; killing", index, err)
		err = n.signal(syscall.SIGKILL, killTimeout)
	}
	if err == nil {
		err = s.restart(n, up.Binary, up.Version, DefaultStopTimeout)
	}
	if err != nil {
		n.setSwap(SwapFailed, err)
		s.log.Printf("node %d: swap failed: %v", index, err)
	} else {
		n.setSwap(SwapDone, nil)
		s.log.Printf("node %d: swapped to %s", index, up.Binary)
	}
	s.saveState()
}

// setUpgrade replaces the pending upgrade, reconciles every node with it,
// and persists both.
func (s *supervisor) setUpgrade(up *Upgrade) error {
	if up != nil {
		if err := isExecutable(up.Binary); err != nil {
			return err
		}
	}
	s.upMu.Lock()
	s.upgrade = up
	s.upMu.Unlock()
	if up == nil {
		s.log.Printf("upgrade cleared")
	} else {
		s.log.Printf("upgrade %q at height %d pending: %s (auto swap %t)", up.Name, up.Height, up.Binary, up.AutoSwap)
	}
	for _, n := range s.nodes {
		s.reconcile(n, up)
	}
	s.saveState()
	return nil
}

// reconcile converges one node with the pending upgrade. A node on the
// upgrade binary is swapped, and its binary and version are persisted in
// case the previous supervisor died before doing so. A node that finished
// an earlier upgrade, or that was moved off the binary that halted, starts
// clean. A node still on the binary that halted, including one whose swap
// failed, is swapped now when the plan is its own. Clearing the upgrade
// forgets every halt.
func (s *supervisor) reconcile(n *node, up *Upgrade) {
	n.ops.Lock()
	defer n.ops.Unlock()
	if up == nil {
		n.setSwap(SwapNone, nil)
		return
	}
	phase, h := n.swapState()
	switch {
	case n.status().Binary == up.Binary:
		n.setSwap(SwapDone, nil)
		s.persistBinary(n, up.Binary, up.Version)
	case phase == SwapDone || (h != nil && h.Binary != "" && h.Binary != n.status().Binary):
		n.setSwap(SwapNone, nil)
	case h != nil:
		n.setSwap(SwapHalted, nil)
		s.swapIfDue(n)
	}
}

// settleSwap records what a restart onto binary means for the node's halt:
// the plan binary completes the swap, any other binary ends the halt, and
// the binary that halted keeps it. The caller holds ops.
func (s *supervisor) settleSwap(n *node, binary string) {
	_, h := n.swapState()
	switch up := s.pending(); {
	case up != nil && binary == up.Binary:
		n.setSwap(SwapDone, nil)
	case h != nil && h.Binary != binary:
		n.setSwap(SwapNone, nil)
	default:
		return
	}
	s.saveState()
}

// persistBinary makes binary the node's next start and records version,
// when either is not yet on disk. The caller holds ops.
func (s *supervisor) persistBinary(n *node, binary, version string) {
	if n.currentSpec().Binary != binary {
		if err := n.setBinary(binary); err != nil {
			s.log.Printf("node %d: %v", n.spec.Index, err)
			return
		}
		if err := s.saveNodes(); err != nil {
			s.log.Printf("node %d: nodes.json: %v", n.spec.Index, err)
		}
	}
	if err := s.record(n, version); err != nil {
		s.log.Printf("node %d: record version %s: %v", n.spec.Index, version, err)
	}
}

func isExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}
