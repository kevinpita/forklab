//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
)

const (
	exrpdOld = "11.1.1"
	exrpdNew = "11.2.0"
	// planIn is how many blocks ahead the plan lands: past the 30s voting
	// period at about one block per second, with room for slow blocks.
	planIn = "45"
)

// TestE2EUpgradeExrpd upgrades fresh two-validator exrpd labs from 11.1.1
// to 11.2.0 three ways: the supervisor swaps halted nodes on its own, the
// user restarts them by version after --no-auto-swap, and a supervisor
// spawned after the first was killed finishes the swap.
func TestE2EUpgradeExrpd(t *testing.T) {
	oldBin, newBin := os.Getenv("FORKLAB_EXRPD_OLD"), os.Getenv("FORKLAB_EXRPD_NEW")
	if oldBin == "" || newBin == "" {
		t.Skip("FORKLAB_EXRPD_OLD or FORKLAB_EXRPD_NEW not set")
	}
	config := t.TempDir()
	t.Setenv("FORKLAB_CONFIG_DIR", config)
	t.Setenv("FORKLAB_HOME", t.TempDir())
	t.Setenv("FORKLAB_TEST_CHILD", "1")
	e, err := profile.Store{Dir: t.TempDir()}.Get("xrplevm")
	if err != nil {
		t.Fatal(err)
	}
	e.Doc.Binaries = map[string]profile.BinaryDocument{exrpdOld: {Path: oldBin}, exrpdNew: {Path: newBin}}
	if _, err := (profile.Store{Dir: filepath.Join(config, "profiles")}).Save(e.Doc); err != nil {
		t.Fatal(err)
	}
	badBin := filepath.Join(t.TempDir(), "bad-exrpd")
	if err := os.WriteFile(badBin, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 11.3.0; exit 0; fi\necho 'bad binary refuses to start' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.Doc.Binaries["11.3.0"] = profile.BinaryDocument{Path: badBin}
	if _, err := (profile.Store{Dir: filepath.Join(config, "profiles")}).Save(e.Doc); err != nil {
		t.Fatal(err)
	}
	t.Run("auto-swap", func(t *testing.T) { autoSwap(t, "auto", newBin) })
	t.Run("manual-swap", func(t *testing.T) { manualSwap(t, "manual", newBin) })
	t.Run("supervisor-killed", func(t *testing.T) { supervisorKilled(t, "killed", newBin) })
	t.Run("bad-binary-recovered", func(t *testing.T) { badBinaryRecovered(t, "bad", badBin, newBin) })
	t.Run("interrupted", func(t *testing.T) { interrupted(t, "int", newBin) })
}

// badBinaryRecovered schedules a plan whose binary cannot start, recovers
// each node by hand onto the real release, and checks that a supervisor
// started afterwards leaves the recovered nodes alone.
func badBinaryRecovered(t *testing.T, name, badBin, newBin string) {
	dir, c := upLab(t, name)
	code, stdout, _ := run("upgrade", "schedule", "11.3.0", "--name", "v"+exrpdNew, "--in", planIn, "--lab", name, "--json")
	if code != 1 || !strings.Contains(stdout, "bad binary refuses to start") {
		t.Fatalf("schedule onto a bad binary: code %d, %s; want the swap failure with the binary's output", code, stdout)
	}
	// schedule returns on the first failed swap; the other node may still
	// be halting.
	var st upgradeStatusView
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) {
		st = forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
		if !slices.ContainsFunc(st.Nodes, func(n upgradeNode) bool { return n.Phase != supervisor.SwapFailed }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes = %+v, want every one swap_failed", st.Nodes)
		}
	}
	if code, stdout, _ := run("upgrade", "status", "--lab", name); code != 0 || !strings.Contains(stdout, "(see --json)") {
		t.Errorf("human status: code %d\n%s", code, stdout)
	}
	for _, n := range c.Nodes {
		forklab(t, "node", "restart", strconv.Itoa(n.Index), "--binary", exrpdNew, "--lab", dir)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	for _, n := range c.Nodes {
		client, err := chain.New(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort()), 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.WaitHeight(ctx, st.Pending.Height+3); err != nil {
			t.Fatalf("%s after manual recovery: %v", n.Name, err)
		}
	}
	nodes := forklabData[[]supervisor.NodeStatus](t, "node", "list", "--lab", dir)
	pids := []int{nodes[0].PID, nodes[1].PID}
	lockData, _ := os.ReadFile(supervisor.Paths{Dir: dir}.Lock())
	old, _ := strconv.Atoi(strings.TrimSpace(string(lockData)))
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if _, err := ensureSupervisor(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	st = forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
	time.Sleep(10 * time.Second)
	nodes = forklabData[[]supervisor.NodeStatus](t, "node", "list", "--lab", dir)
	for i, n := range nodes {
		if n.PID != pids[i] || n.State != supervisor.StateRunning || n.Binary != newBin || n.Upgrade != supervisor.SwapNone {
			t.Errorf("after the new supervisor %s = %+v, want pid %d left alone on %s", n.Name, n, pids[i], newBin)
		}
	}
	t.Logf("recovered nodes kept pids %v under the new supervisor; bad plan still registered: %v", pids, st.Pending != nil)
}

// interrupted sends SIGINT to a scheduling forklab once a yes vote is
// tallied, so the proposal passes while forklab is gone: the plan must stay
// registered and the halt must still be swapped.
func interrupted(t *testing.T, name, newBin string) {
	dir, c := upLab(t, name)
	cmd := exec.Command(os.Args[0], "upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", name)
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		var env struct {
			Data proposalView `json:"data"`
		}
		if code, stdout, _ := run("gov", "show", "1", "--lab", name, "--json"); code == 0 && json.Unmarshal([]byte(stdout), &env) == nil && env.Data.Tally.Yes != "" && env.Data.Tally.Yes != "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no proposal in voting; forklab said:\n%s", out.String())
		}
		time.Sleep(time.Second)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	t.Logf("interrupted schedule exited with %v and said:\n%s", err, out.String())
	if err == nil || !strings.Contains(out.String(), "stays registered") {
		t.Fatalf("want a failure that says the plan stays registered")
	}
	st := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
	if st.Pending == nil || st.Pending.ProposalID != 1 {
		t.Fatalf("after the interrupt status = %+v, want the plan registered with proposal 1", st)
	}
	planHeight := st.Pending.Height
	if code, stdout, _ := run("upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", name, "--json"); code != 1 || !strings.Contains(stdout, "still in VOTING_PERIOD") {
		t.Errorf("second schedule while voting: code %d, %s; want it refused", code, stdout)
	}
	deadline = time.Now().Add(3 * time.Minute)
	for {
		st = forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
		if swappedOrCompleted(t, st) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes never swapped after the interrupt: %+v", st)
		}
		time.Sleep(time.Second)
	}
	checkUpgraded(t, name, dir, c, planHeight, newBin)
}

// swappedOrCompleted reports whether every node is swapped, or an upgrade
// status call already completed the upgrade, which clears the phases.
func swappedOrCompleted(t *testing.T, st upgradeStatusView) bool {
	t.Helper()
	if st.Completed != nil {
		return true
	}
	done := true
	for _, n := range st.Nodes {
		if n.Phase == supervisor.SwapFailed {
			t.Fatalf("%s: %s", n.Name, n.SwapError)
		}
		done = done && n.Phase == supervisor.SwapDone
	}
	return done
}

// labChainID keeps the v11.2.0 handler off its mainnet-only escrow work,
// which fails on a fresh chain that carries the mainnet chain id.
const labChainID = "xrplevm_1449999-1"

// upLab creates a fresh exrpd lab on the old release and brings it up.
func upLab(t *testing.T, name string) (string, lab.Config) {
	t.Helper()
	forklab(t, "lab", "create", name, "--profile", "xrplevm", "--version", exrpdOld, "--validators", "2", "--chain-id", labChainID)
	l, err := labs()
	if err != nil {
		t.Fatal(err)
	}
	c, dir, err := l.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var out bytes.Buffer
		_ = Run([]string{"lab", "down", name}, &out, &out)
		if t.Failed() {
			sup, _ := os.ReadFile(supervisor.Paths{Dir: dir}.Log())
			t.Logf("supervisor.log tail:\n%s", tail(sup, 3000))
			for _, n := range c.Nodes {
				log, _ := os.ReadFile(nodeLog(dir, n))
				t.Logf("%s log tail:\n%s", n.Name, tail(log, 3000))
			}
		}
	})
	forklab(t, "lab", "up", name)
	return dir, c
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func nodeLog(dir string, n lab.Node) string { return filepath.Join(lab.NodeHome(dir, n), "node.log") }

// checkUpgraded asserts what every successful upgrade leaves behind: both
// nodes on the new release in lab.yaml and nodes.json, the new binary's
// "applying upgrade" line in each log, and blocks past the plan height.
func checkUpgraded(t *testing.T, name, dir string, c lab.Config, planHeight int64, newBin string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	for _, n := range c.Nodes {
		client, err := chain.New(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort()), 0)
		if err != nil {
			t.Fatal(err)
		}
		h, err := client.WaitHeight(ctx, planHeight+3)
		if err != nil {
			t.Fatalf("%s: %v", n.Name, err)
		}
		t.Logf("%s at height %d after the plan at %d", n.Name, h, planHeight)
	}
	st := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
	if st.Plan != nil {
		t.Errorf("chain still schedules %+v after applying it", st.Plan)
	}
	if st.Pending != nil || st.Completed == nil || st.Completed.Version != exrpdNew || st.Completed.Height != planHeight {
		t.Errorf("supervisor pending = %+v, completed = %+v; want the %s plan completed", st.Pending, st.Completed, exrpdNew)
	}
	for _, n := range st.Nodes {
		if n.Version != exrpdNew || n.Binary != newBin || n.State != supervisor.StateRunning || n.Phase != supervisor.SwapNone {
			t.Errorf("%s = %+v, want version %s running %s with the upgrade over", n.Name, n, exrpdNew, newBin)
		}
	}
	if code, stdout, _ := run("upgrade", "status", "--lab", name); code != 0 || !strings.Contains(stdout, fmt.Sprintf("last upgrade \"v%s\" to version %s completed at height %d", exrpdNew, exrpdNew, planHeight)) {
		t.Errorf("human status: code %d\n%s", code, stdout)
	}
	applying := fmt.Sprintf(`applying upgrade "v%s" at height: %d`, exrpdNew, planHeight)
	needed := fmt.Sprintf(`UPGRADE "v%s" NEEDED at height: %d`, exrpdNew, planHeight)
	for _, n := range c.Nodes {
		data, _ := os.ReadFile(nodeLog(dir, n))
		log := supervisor.StripANSI(string(data))
		if !strings.Contains(log, applying) {
			t.Errorf("%s log lacks %q", n.Name, applying)
		}
		if got := strings.Count(log, needed); got != 1 {
			t.Errorf("%s log has %d halt lines, want exactly one", n.Name, got)
		}
	}
	specs, err := supervisor.LoadNodes(dir)
	if err != nil || specs[0].Binary != newBin || specs[1].Binary != newBin {
		t.Errorf("nodes.json = %+v, %v; want both on %s", specs, err, newBin)
	}
}

func autoSwap(t *testing.T, name, newBin string) {
	dir, c := upLab(t, name)
	if code, stdout, _ := run("upgrade", "schedule", exrpdNew, "--in", "3", "--lab", name, "--json"); code != 1 || !strings.Contains(stdout, "voting takes") {
		t.Fatalf("a plan inside the voting window: code %d, %s; want it refused", code, stdout)
	}
	v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", name)
	t.Logf("schedule: proposal %d, plan %+v, upgraded %t at height %d", v.ProposalID, v.Plan, v.Upgraded, v.Height)
	if !v.Upgraded || v.Height < v.Plan.Height+3 || v.Plan.Name != "v"+exrpdNew || !v.Plan.AutoSwap || v.Plan.Binary != newBin || len(v.Votes) != 3 {
		t.Fatalf("schedule = %+v", v)
	}
	checkUpgraded(t, name, dir, c, v.Plan.Height, newBin)
	if code, stdout, _ := run("upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", name, "--json"); code != 0 {
		t.Logf("scheduling again after the upgrade: code %d, %s", code, stdout)
	}

	// A reset replays genesis on the creation release, so the upgrade is
	// undone and the chain runs again on 11.1.1.
	r := forklabData[labReset](t, "lab", "reset", name, "--force")
	if r.Version != exrpdOld {
		t.Fatalf("reset = %+v, want version %s", r, exrpdOld)
	}
	upped := forklabData[labUp](t, "lab", "up", name)
	for _, n := range upped.Nodes {
		if n.Height < 2 || n.Binary != oldBin(t) {
			t.Errorf("after reset %s = height %d on %s, want blocks on %s", n.Name, n.Height, n.Binary, oldBin(t))
		}
	}
	st := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
	if st.Pending != nil || st.Plan != nil || st.Completed != nil {
		t.Errorf("after reset status = %+v, want no plan anywhere", st)
	}
	for _, n := range st.Nodes {
		if n.Version != exrpdOld || n.Phase != supervisor.SwapNone {
			t.Errorf("after reset %s = %+v, want version %s with no swap state", n.Name, n, exrpdOld)
		}
	}
	t.Logf("after reset: nodes at heights %d and %d on %s", upped.Nodes[0].Height, upped.Nodes[1].Height, exrpdOld)
}

func oldBin(t *testing.T) string {
	t.Helper()
	return os.Getenv("FORKLAB_EXRPD_OLD")
}

func manualSwap(t *testing.T, name, newBin string) {
	dir, c := upLab(t, name)
	v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--no-auto-swap", "--lab", name)
	if v.Upgraded || v.Plan.AutoSwap {
		t.Fatalf("schedule --no-auto-swap = %+v", v)
	}
	deadline := time.Now().Add(3 * time.Minute)
	var st upgradeStatusView
	for {
		st = forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
		halted := 0
		for _, n := range st.Nodes {
			if n.Phase == supervisor.SwapHalted {
				halted++
			}
		}
		if halted == len(st.Nodes) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes never halted: %+v", st)
		}
		time.Sleep(time.Second)
	}
	for _, n := range st.Nodes {
		if n.Version != exrpdOld || n.Halt == nil || n.Halt.Height != v.Plan.Height {
			t.Errorf("halted %s = %+v, want still on %s with the plan's halt", n.Name, n, exrpdOld)
		}
		t.Logf("%s halted: process %s", n.Name, n.State)
	}
	if code, stdout, _ := run("upgrade", "status", "--lab", name); code != 0 || !strings.Contains(stdout, "waiting for swap") {
		t.Errorf("human status: code %d\n%s", code, stdout)
	}
	for _, n := range c.Nodes {
		nodes := forklabData[[]supervisor.NodeStatus](t, "node", "restart", strconv.Itoa(n.Index), "--binary", exrpdNew, "--lab", dir)
		if nodes[n.Index].Binary != newBin || nodes[n.Index].Upgrade != supervisor.SwapDone {
			t.Errorf("after restart %s = %+v", n.Name, nodes[n.Index])
		}
	}
	checkUpgraded(t, name, dir, c, v.Plan.Height, newBin)
}

func supervisorKilled(t *testing.T, name, newBin string) {
	dir, c := upLab(t, name)
	v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--no-wait", "--lab", name)
	if v.Upgraded || !v.Plan.AutoSwap {
		t.Fatalf("schedule --no-wait = %+v", v)
	}
	lockData, err := os.ReadFile(supervisor.Paths{Dir: dir}.Lock())
	if err != nil {
		t.Fatal(err)
	}
	old, _ := strconv.Atoi(strings.TrimSpace(string(lockData)))
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	needed := fmt.Sprintf(`UPGRADE "v%s" NEEDED at height: %d`, exrpdNew, v.Plan.Height)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		halted := 0
		for _, n := range c.Nodes {
			data, _ := os.ReadFile(nodeLog(dir, n))
			if strings.Contains(supervisor.StripANSI(string(data)), needed) {
				halted++
			}
		}
		if halted == len(c.Nodes) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nodes never halted while unsupervised")
		}
		time.Sleep(time.Second)
	}
	live, _ := supervisor.LiveNodes(dir)
	t.Logf("supervisor %d killed before the halt; nodes still running after it: %v", old, live)

	// upgrade status only dials, so the test spawns the supervisor any
	// mutating command would; it adopts the halted nodes and finishes the
	// swap.
	if _, err := ensureSupervisor(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Minute)
	for {
		st := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
		if swappedOrCompleted(t, st) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new supervisor never finished the swap: %+v", st)
		}
		time.Sleep(time.Second)
	}
	lockData, _ = os.ReadFile(supervisor.Paths{Dir: dir}.Lock())
	if now, _ := strconv.Atoi(strings.TrimSpace(string(lockData))); now == old || now == 0 {
		t.Errorf("supervisor.lock holds %d, want a new supervisor", now)
	}
	checkUpgraded(t, name, dir, c, v.Plan.Height, newBin)
}
