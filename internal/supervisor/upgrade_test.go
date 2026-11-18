package supervisor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/supervisor"
)

func TestMatchHaltReadsRealNodeLogLines(t *testing.T) {
	exrpd := "\x1b[90m12:23AM\x1b[0m \x1b[31mERR\x1b[0m \x1b[1mUPGRADE \"v11.2.0\" NEEDED at height: 218: {\"binaries\":{\"linux/amd64\":\"file://local\"}}\x1b[0m \x1b[36mmodule=\x1b[0mx/upgrade"
	simd := `2:41PM ERR UPGRADE "v-next" NEEDED at height: 363: module=x/upgrade`
	for _, c := range []struct {
		line string
		want supervisor.Halt
	}{
		{exrpd, supervisor.Halt{Name: "v11.2.0", Height: 218}},
		{simd, supervisor.Halt{Name: "v-next", Height: 363}},
		{"\x1b[90m2:41PM\x1b[0m \x1b[31mERR\x1b[0m \x1b[1m" + simd[11:] + "\x1b[0m", supervisor.Halt{Name: "v-next", Height: 363}},
	} {
		got, ok := supervisor.MatchHalt(c.line)
		if !ok || got != c.want {
			t.Errorf("MatchHalt(%q) = %+v, %t; want %+v", c.line, got, ok, c.want)
		}
	}
	for _, line := range []string{
		`12:23AM ERR error in proxyAppConn.FinalizeBlock err="UPGRADE \"v11.2.0\" NEEDED at height: 218: {}" module=state`,
		`12:23AM ERR CONSENSUS FAILURE!!! err="failed to apply block; error UPGRADE \"v11.2.0\" NEEDED at height: 218" module=consensus`,
		`12:23AM INF applying upgrade "v11.2.0" at height: 218 module=x/upgrade`,
		`12:23AM INF committed state height=217 module=state`,
	} {
		if h, ok := supervisor.MatchHalt(line); ok {
			t.Errorf("MatchHalt(%q) = %+v, want no match", line, h)
		}
	}
}

// upgradedBinary is a copy of the test binary that the fake node treats as
// the upgraded release.
func upgradedBinary(t *testing.T) string {
	t.Helper()
	v2 := filepath.Join(t.TempDir(), "fake-node-v2")
	if err := os.Symlink(os.Args[0], v2); err != nil {
		t.Fatal(err)
	}
	return v2
}

func versionLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "versions")
	t.Setenv(versionLogEnv, path)
	return path
}

func swapped(c *supervisor.Client, binary string, indexes ...int) func() bool {
	return func() bool {
		nodes, err := c.Status()
		if err != nil {
			return false
		}
		for _, i := range indexes {
			n := nodes[i]
			if n.Upgrade != supervisor.SwapDone || n.State != supervisor.StateRunning || n.Binary != binary {
				return false
			}
		}
		return true
	}
}

func TestUpgradeSwapsEveryHaltedNode(t *testing.T) {
	h := newHarness(t)
	versions := versionLog(t)
	l := h.lab(2, "--halt", "v2:50", "--halt-delay", "300ms")
	v2 := upgradedBinary(t)
	c := up(t, l)
	plan := &supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}
	if _, err := c.SetUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	if got, err := supervisor.LoadUpgrade(l.dir); err != nil || !reflect.DeepEqual(got, plan) {
		t.Fatalf("upgrade.json = %+v, %v; want %+v", got, err, plan)
	}
	pids := mustStart(t, c, l, supervisor.All)

	waitUpTo(t, 15*time.Second, "both nodes to be swapped", swapped(c, v2, 0, 1))
	nodes, _ := c.Status()
	for i, n := range nodes {
		if n.PID == pids[i].PID || n.Halt == nil || n.Halt.Name != "v2" || n.Halt.Height != 50 || n.Halt.Binary != os.Args[0] {
			t.Errorf("node %d = %+v, want a new pid and the halt of %s recorded", i, n, os.Args[0])
		}
	}
	specs, err := supervisor.LoadNodes(l.dir)
	if err != nil || specs[0].Binary != v2 || specs[1].Binary != v2 {
		t.Errorf("nodes.json = %+v, %v; want both on %s", specs, err, v2)
	}
	if data, _ := os.ReadFile(versions); !strings.Contains(string(data), "0 2.0.0\n") || !strings.Contains(string(data), "1 2.0.0\n") || strings.Contains(string(data), "overlap") {
		t.Errorf("recorded versions = %q, want both nodes on 2.0.0, recorded one at a time", data)
	}
	if !strings.Contains(logOf(l, 0), "fake node") || strings.Count(logOf(l, 0), `UPGRADE "v2" NEEDED`) != 1 {
		t.Errorf("node0 halted again after the swap:\n%s", logOf(l, 0))
	}

	// A new supervisor finds the swap done and leaves the nodes alone.
	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	c = up(t, l)
	after, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range after {
		if n.PID != nodes[i].PID || n.Upgrade != supervisor.SwapDone || !n.Adopted {
			t.Errorf("after adoption node %d = %+v, want the swapped pid %d adopted", i, n, nodes[i].PID)
		}
	}
	if st, err := c.Upgrade(); err != nil || st.Upgrade == nil || !reflect.DeepEqual(st.Upgrade, plan) || st.Completed != nil {
		t.Errorf("after restart = %+v, %v; want %+v pending", st, err, plan)
	}
}

func TestCompleteUpgradeEndsThePlanOnceEveryNodeSwapped(t *testing.T) {
	h := newHarness(t)
	l := h.lab(2, "--halt", "v2:50", "--halt-delay", "300ms")
	v2 := upgradedBinary(t)
	c := up(t, l)
	plan := &supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}
	if _, err := c.SetUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	mustStart(t, c, l, supervisor.All)
	if st, err := c.CompleteUpgrade("v2"); err == nil || st.Upgrade == nil {
		t.Fatalf("complete before the swap = %+v, %v; want an error and the plan kept", st, err)
	}
	waitUpTo(t, 15*time.Second, "both nodes to be swapped", swapped(c, v2, 0, 1))
	if _, err := c.CompleteUpgrade("v1"); err == nil {
		t.Error("completing a plan that is not pending succeeded")
	}

	st, err := c.CompleteUpgrade("v2")
	if err != nil || st.Upgrade != nil || st.Completed == nil || !reflect.DeepEqual(st.Completed, plan) {
		t.Fatalf("complete = %+v, %v; want no pending plan and %+v completed", st, err, plan)
	}
	for i, n := range st.Nodes {
		if n.Upgrade != supervisor.SwapNone || n.Halt != nil || n.Binary != v2 || n.State != supervisor.StateRunning {
			t.Errorf("node %d = %+v, want it running %s with no upgrade phase", i, n, v2)
		}
	}
	if _, err := c.CompleteUpgrade("v2"); err != nil {
		t.Errorf("completing again: %v", err)
	}
	data, _ := os.ReadFile(supervisor.Paths{Dir: l.dir}.Upgrade())
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(data, &saved); err != nil || saved["plan"] != nil || saved["halts"] != nil || saved["completed"] == nil {
		t.Errorf("upgrade.json = %s, want only the completed plan", data)
	}

	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	c = up(t, l)
	if st, err := c.Upgrade(); err != nil || st.Upgrade != nil || st.Completed == nil || st.Completed.Name != "v2" || st.Nodes[0].Upgrade != supervisor.SwapNone {
		t.Errorf("after restart = %+v, %v; want v2 completed and nothing pending", st, err)
	}
}

func TestUpgradeRegisteredAfterTheHaltSwapsAndClearing(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1, "--halt", "v2:50")
	v2 := upgradedBinary(t)
	c := up(t, l)
	mustStart(t, c, l, supervisor.All)
	halted := func() bool {
		nodes, _ := c.Status()
		return nodes[0].Upgrade == supervisor.SwapHalted && nodes[0].State == supervisor.StateExited
	}
	waitFor(t, "node to halt", halted)

	plan := &supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2}
	if _, err := c.SetUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !halted() {
		t.Fatal("an upgrade without auto swap touched the halted node")
	}
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "other", Height: 50, Version: "3", Binary: v2, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !halted() {
		t.Fatal("an upgrade for another plan swapped the node")
	}

	plan.AutoSwap = true
	if _, err := c.SetUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	waitUpTo(t, 15*time.Second, "node to be swapped", swapped(c, v2, 0))

	nodes, err := c.SetUpgrade(nil)
	if err != nil || nodes[0].Upgrade != supervisor.SwapNone || nodes[0].Halt != nil {
		t.Errorf("after clearing node = %+v, %v; want no upgrade phase", nodes[0], err)
	}
	if pending, err := supervisor.LoadUpgrade(l.dir); err != nil || pending != nil {
		t.Errorf("upgrade.json after clearing = %+v, %v; want none", pending, err)
	}
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v3", Binary: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Error("an upgrade to a missing binary was accepted")
	}
}

func TestUpgradeFinishesUnderANewSupervisorAfterKill9(t *testing.T) {
	h := newHarness(t)
	versions := versionLog(t)
	l := h.lab(1, "--halt", "v2:50", "--halt-delay", "1s")
	v2 := upgradedBinary(t)
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	mustStart(t, c, l, supervisor.All)
	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	waitFor(t, "node to halt unsupervised", func() bool {
		live, err := supervisor.LiveNodes(l.dir)
		return err == nil && len(live) == 0 && strings.Contains(logOf(l, 0), "NEEDED at height")
	})

	c = up(t, l)
	waitUpTo(t, 15*time.Second, "new supervisor to swap the node", swapped(c, v2, 0))
	if data, _ := os.ReadFile(versions); string(data) != "0 2.0.0\n" {
		t.Errorf("recorded versions = %q, want the single swap", data)
	}
}

func TestUpgradeStopsAHaltedNodeThatStaysUp(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1, "--halt", "v2:50", "--halt-stay")
	v2 := upgradedBinary(t)
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	pid := mustStart(t, c, l, supervisor.All)[0].PID
	waitUpTo(t, 20*time.Second, "node to be swapped", swapped(c, v2, 0))
	if alive(pid) {
		t.Errorf("halted pid %d still runs after the swap", pid)
	}
}

// TestUpgradeSurvivesKill9BetweenHaltAndSwap kills the supervisor once the
// halt is recorded, before the swap touches the node. The tail has long
// persisted its offset past the halt line, so only upgrade.json can tell
// the next supervisor to swap.
func TestUpgradeSurvivesKill9BetweenHaltAndSwap(t *testing.T) {
	h := newHarness(t)
	l := h.lab(1, "--halt", "v2:50", "--halt-stay")
	v2 := upgradedBinary(t)
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	pid := mustStart(t, c, l, supervisor.All)[0].PID
	waitFor(t, "halt to be recorded", func() bool {
		nodes, err := c.Status()
		return err == nil && nodes[0].Upgrade == supervisor.SwapHalted
	})
	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	waitFor(t, "tail to move past the halt line", func() bool { return strings.Count(logOf(l, 0), "tick") > 10 })
	if !alive(pid) {
		t.Fatalf("node %d died before the swap", pid)
	}

	c = up(t, l)
	waitUpTo(t, 20*time.Second, "new supervisor to swap the halted node", swapped(c, v2, 0))
	if nodes, _ := c.Status(); nodes[0].PID == pid {
		t.Errorf("node kept pid %d, want a restart on %s", pid, v2)
	}
}

func TestUpgradeTwiceInOneSupervisor(t *testing.T) {
	h := newHarness(t)
	// Each release halts on the next plan later than the probe that trusts
	// it, as a chain that reaches the next plan height later would.
	l := h.lab(1, "--halt", "v2:50,v3:80", "--halt-delay", "1500ms")
	v2 := upgradedBinary(t)
	v3 := filepath.Join(t.TempDir(), "fake-node-v3")
	if err := os.Symlink(os.Args[0], v3); err != nil {
		t.Fatal(err)
	}
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	mustStart(t, c, l, supervisor.All)
	waitUpTo(t, 15*time.Second, "swap to v2", swapped(c, v2, 0))

	nodes, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v3", Height: 80, Version: "3.0.0", Binary: v3, AutoSwap: true})
	if err != nil || nodes[0].Upgrade == supervisor.SwapDone || (nodes[0].Halt != nil && nodes[0].Halt.Name != "v3") {
		t.Fatalf("after the second plan node = %+v, %v; want the v2 swap forgotten", nodes[0], err)
	}
	waitUpTo(t, 15*time.Second, "swap to v3", swapped(c, v3, 0))
	if nodes, _ := c.Status(); nodes[0].Halt == nil || nodes[0].Halt.Name != "v3" {
		t.Errorf("node = %+v, want the v3 halt recorded", nodes[0])
	}
}

// TestFailedSwapRecoveredByHandStaysRecovered swaps to a binary that
// refuses to start, recovers each node by hand onto a good one, and checks
// that a new supervisor leaves the recovered nodes alone instead of
// re-arming the old halt and stopping them again.
func TestFailedSwapRecoveredByHandStaysRecovered(t *testing.T) {
	h := newHarness(t)
	l := h.lab(2, "--halt", "v2:50", "--halt-delay", "300ms")
	v2 := upgradedBinary(t)
	bad := filepath.Join(t.TempDir(), "bad-node")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho 'bad binary refuses to start' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: bad, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	mustStart(t, c, l, supervisor.All)
	waitUpTo(t, 20*time.Second, "both swaps to fail", func() bool {
		nodes, err := c.Status()
		return err == nil && nodes[0].Upgrade == supervisor.SwapFailed && nodes[1].Upgrade == supervisor.SwapFailed
	})
	nodes, _ := c.Status()
	if !strings.Contains(nodes[0].SwapError, "bad binary refuses to start") {
		t.Errorf("swap error = %q, want the bad binary's own output", nodes[0].SwapError)
	}

	for _, i := range []string{"0", "1"} {
		if _, err := c.Restart(i, v2, "2.0.0", 5*time.Second); err != nil {
			t.Fatalf("manual restart of node %s: %v", i, err)
		}
	}
	nodes, _ = c.Status()
	for _, n := range nodes {
		if n.State != supervisor.StateRunning || n.Binary != v2 || n.Upgrade != supervisor.SwapNone || n.Halt != nil {
			t.Fatalf("recovered node = %+v, want running on %s with the halt over", n, v2)
		}
	}
	pids := []int{nodes[0].PID, nodes[1].PID}
	state, err := os.ReadFile(supervisor.Paths{Dir: l.dir}.Upgrade())
	if err != nil || strings.Contains(string(state), `"halts"`) {
		t.Fatalf("upgrade.json after recovery = %s, %v; want the plan without halts", state, err)
	}
	// A supervisor that died between the restart and its save leaves the
	// halts of the old binary on disk; the next one must not act on them.
	stale := map[string]any{
		"plan":  map[string]any{"name": "v2", "height": 50, "version": "2.0.0", "binary": bad, "auto_swap": true},
		"halts": map[string]any{"0": supervisor.Halt{Name: "v2", Height: 50, Binary: os.Args[0]}, "1": supervisor.Halt{Name: "v2", Height: 50, Binary: os.Args[0]}},
	}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(supervisor.Paths{Dir: l.dir}.Upgrade(), data, 0o644); err != nil {
		t.Fatal(err)
	}

	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	c = up(t, l)
	time.Sleep(2 * time.Second)
	nodes, err = c.Status()
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range nodes {
		if n.PID != pids[i] || n.State != supervisor.StateRunning || n.Binary != v2 || n.Upgrade != supervisor.SwapNone {
			t.Errorf("after adoption node %d = %+v, want pid %d left alone on %s", i, n, pids[i], v2)
		}
	}
	if st, err := supervisor.LoadUpgrade(l.dir); err != nil || st == nil || st.Binary != bad {
		t.Errorf("plan = %+v, %v; want the bad plan still registered for the user to cancel", st, err)
	}
}

// cosmovisorLab is a lab whose nodes run under the fake cosmovisor.
func cosmovisorLab(h *harness, n int, nodeArgs ...string) *lab {
	h.t.Helper()
	l := h.lab(n, nodeArgs...)
	for i := range l.specs {
		l.specs[i].Cosmovisor = &supervisor.Cosmovisor{Path: os.Args[0], Name: "fake-node"}
	}
	if err := supervisor.SaveNodes(l.dir, l.specs); err != nil {
		h.t.Fatal(err)
	}
	return l
}

func currentSlot(l *lab, i int) string {
	link, _ := os.Readlink(filepath.Join(l.specs[i].Home, "cosmovisor", "current"))
	return link
}

// chainProcesses counts the processes running out of the lab's cosmovisor
// trees: the chain binaries, not cosmovisor itself.
func chainProcesses(l *lab) int {
	count := 0
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if data, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && strings.HasPrefix(string(data), l.dir+"/node") {
			count++
		}
	}
	return count
}

// The plan name has an upper-case letter: cosmovisor looks for its slot
// under the lower-cased name.
func TestCosmovisorSwapsThePlacedBinaryAndKillLeavesNoChainProcess(t *testing.T) {
	h := newHarness(t)
	versions := versionLog(t)
	l := cosmovisorLab(h, 2, "--halt", "V2:50", "--halt-delay", "300ms")
	v2 := upgradedBinary(t)
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "V2", Height: 50, Version: "2.0.0", Binary: v2, AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	started, err := c.Start(supervisor.All)
	if err != nil {
		t.Fatal(err)
	}
	waitUpTo(t, 15*time.Second, "cosmovisor to swap both nodes", swapped(c, v2, 0, 1))
	nodes, _ := c.Status()
	for i, n := range nodes {
		if n.PID != started[i].PID || !n.Cosmovisor || currentSlot(l, i) != "upgrades/v2" {
			t.Errorf("node %d = %+v, current -> %q; want cosmovisor pid %d kept and the link on the plan", i, n, currentSlot(l, i), started[i].PID)
		}
	}
	specs, err := supervisor.LoadNodes(l.dir)
	if err != nil || specs[0].Binary != v2 || specs[1].Binary != v2 {
		t.Errorf("nodes.json = %+v, %v; want both on %s", specs, err, v2)
	}
	if data, _ := os.ReadFile(versions); !strings.Contains(string(data), "0 2.0.0\n") || !strings.Contains(string(data), "1 2.0.0\n") {
		t.Errorf("recorded versions = %q, want both nodes on 2.0.0", data)
	}

	// A new supervisor reads the swap off the link.
	old := supervisorPid(t, l)
	_ = syscall.Kill(old, syscall.SIGKILL)
	waitFor(t, "old supervisor to die", func() bool { return !alive(old) })
	c = up(t, l)
	after, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range after {
		if n.PID != started[i].PID || n.Upgrade != supervisor.SwapDone || !n.Adopted || n.Binary != v2 {
			t.Errorf("after adoption node %d = %+v, want pid %d adopted and swapped to %s", i, n, started[i].PID, v2)
		}
	}

	if chainProcesses(l) != 2 {
		t.Fatalf("%d chain processes before the kill, want 2", chainProcesses(l))
	}
	if _, err := c.Kill(supervisor.All); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the chain binaries to die with cosmovisor", func() bool { return chainProcesses(l) == 0 })
}

func TestCosmovisorWithoutThePlanBinaryHaltsUntilItIsDelivered(t *testing.T) {
	h := newHarness(t)
	l := cosmovisorLab(h, 2, "--halt", "v2:50", "--halt-delay", "300ms")
	v2 := upgradedBinary(t)
	c := up(t, l)
	if _, err := c.SetUpgrade(&supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: v2}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(supervisor.All); err != nil {
		t.Fatal(err)
	}
	waitUpTo(t, 15*time.Second, "both nodes to halt and cosmovisor to exit", func() bool {
		nodes, err := c.Status()
		return err == nil && !slices.ContainsFunc(nodes, func(n supervisor.NodeStatus) bool {
			return n.State != supervisor.StateExited || n.Upgrade != supervisor.SwapHalted
		})
	})
	if currentSlot(l, 0) != "genesis" || !strings.Contains(logOf(l, 0), "binary not present") {
		t.Fatalf("node0 current -> %q, want it left on genesis by a cosmovisor that found no binary\n%s", currentSlot(l, 0), logOf(l, 0))
	}

	// restart --binary relinks; a plain start switches to a binary the
	// operator placed.
	if _, err := c.Restart("0", v2, "2.0.0", 0); err != nil {
		t.Fatal(err)
	}
	placed := filepath.Join(l.specs[1].Home, "cosmovisor", "upgrades", "v2", "bin", "fake-node")
	if err := os.MkdirAll(filepath.Dir(placed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v2, placed); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start("1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both nodes to run the plan binary", swapped(c, v2, 0, 1))
	for i := range l.specs {
		if currentSlot(l, i) != "upgrades/v2" || strings.Count(logOf(l, i), `UPGRADE "v2" NEEDED`) != 1 {
			t.Errorf("node %d current -> %q, want the plan's slot and a single halt\n%s", i, currentSlot(l, i), logOf(l, i))
		}
	}
}
