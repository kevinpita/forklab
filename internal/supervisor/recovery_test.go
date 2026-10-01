package supervisor

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func recoverySupervisor(t *testing.T) (*supervisor, *Upgrade) {
	t.Helper()
	dir := t.TempDir()
	n := &node{spec: NodeSpec{Index: 0, Name: "node0", Binary: "/bin/sh"}, swap: SwapHalted, halt: &Halt{Name: "v2", Height: 50, Binary: "/bin/sh"}}
	s := &supervisor{paths: Paths{Dir: dir}, log: log.New(io.Discard, "", 0), nodes: []*node{n}}
	up := &Upgrade{Name: "v2", Height: 50, Binary: "/bin/sh", Version: "2", AutoSwap: true, Previous: []UpgradeTarget{{Index: 0, Binary: "/bin/sh", Version: "1"}}}
	s.upgrade = up
	return s, up
}

func TestQueuedSwapCannotUndoFrozenRecovery(t *testing.T) {
	s, up := recoverySupervisor(t)
	frozen := *up
	frozen.AutoSwap = false
	s.upgrade = &frozen
	s.swap(s.nodes[0], *up)
	if s.nodes[0].swap != SwapHalted {
		t.Fatalf("queued swap changed phase to %s", s.nodes[0].swap)
	}
}

func TestRecoveryFreezePersistenceFailureLeavesPlanUntouched(t *testing.T) {
	s, up := recoverySupervisor(t)
	if err := os.Mkdir(filepath.Join(s.paths.Dir, "upgrade.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := *up
	next.AutoSwap = false
	next.Recovery = &Recovery{ID: "retry", Mode: "retry", Targets: up.Previous}
	if err := s.replaceUpgrade(up, &next); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if !reflect.DeepEqual(s.pending(), up) || s.nodes[0].swap != SwapHalted {
		t.Fatal("changed plan or node after persistence failure")
	}
}

func TestRecoveryRejectsStalePlanAndOrdinaryCompletion(t *testing.T) {
	s, up := recoverySupervisor(t)
	next := *up
	next.AutoSwap = false
	next.Recovery = &Recovery{ID: "retry", Mode: "retry", Targets: up.Previous}
	stale := *up
	stale.Height++
	if err := s.replaceUpgrade(&stale, &next); err == nil {
		t.Fatal("accepted stale recovery")
	}
	s.upgrade = &next
	if err := s.complete(up.Name); err == nil {
		t.Fatal("ordinary status marked recovery completed")
	}
	if err := s.setUpgrade(nil); err == nil {
		t.Fatal("ordinary cancel erased recovery")
	}
	if err := s.finishRecovery(&next); err == nil {
		t.Fatal("finished without running validators")
	}
}
