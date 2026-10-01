package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/supervisor"
)

func TestRecoveryArgsPreserveOtherHeights(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		skip bool
		want []string
	}{
		{"add", []string{"start", "--home", "/node", "--unsafe-skip-upgrades", "12,30", "--unsafe-skip-upgrades=12,50"}, true, []string{"start", "--home", "/node", "--unsafe-skip-upgrades", "12,30,50"}},
		{"retry", []string{"start", "--unsafe-skip-upgrades=12,50,30,50"}, false, []string{"start", "--unsafe-skip-upgrades", "12,30"}},
		{"remove last", []string{"start", "--unsafe-skip-upgrades", "50"}, false, []string{"start"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recoveryArgs(tc.args, 50, tc.skip)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%v, %v; want %v", got, err, tc.want)
			}
		})
	}
	for _, args := range [][]string{{"--unsafe-skip-upgrades"}, {"--unsafe-skip-upgrades=-2"}, {"--unsafe-skip-upgrades", "4,no"}} {
		if _, err := recoveryArgs(args, 50, true); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRecoveryRejectsConflictingCommandAndFlags(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockUpgrade(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockUpgrade(dir); err == nil {
		t.Fatal("allowed concurrent upgrade command")
	}
	unlock()
	again, err := lockUpgrade(dir)
	if err != nil {
		t.Fatal(err)
	}
	again()
	for _, args := range [][]string{{"upgrade", "recover"}, {"upgrade", "recover", "--previous", "--version", "2"}, {"upgrade", "recover", "--previous", "--progress", "bad"}} {
		code, out, _ := run(append(args, "--json")...)
		if code != 2 {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
}

func recoveryLab(t *testing.T) (string, *supervisor.Client, *supervisor.Upgrade) {
	t.Helper()
	dir := newShellLab(t)
	fakeProfileLab(t, dir, "1.0.0")
	keyDir := filepath.Join(dir, "node0", "config")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "priv_validator_key.json"), []byte(`{"address":"AA","pub_key":{"type":"tendermint/PubKeyEd25519","value":"ka"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sup, err := ensureSupervisor(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sup.Down(time.Second) })
	plan := &supervisor.Upgrade{Name: "v2", Height: 50, Version: "2.0.0", Binary: "/bin/sh", Previous: []supervisor.UpgradeTarget{{Index: 0, Binary: "/bin/sh", Version: "1.0.0"}}}
	if _, err := sup.SetUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	return dir, sup, plan
}

func TestRecoveryPreviousSkipsAndVerifiesFreshBlocks(t *testing.T) {
	dir, sup, _ := recoveryLab(t)
	var height atomic.Int64
	height.Store(52)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"result":{"sync_info":{"latest_block_height":"%d"}}}`, height.Add(1))
	}))
	defer srv.Close()
	cfg, err := lab.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Nodes[0].Ports = []lab.Port{{Key: "rpc.laddr", Port: srv.Listener.Addr().(*net.TCPAddr).Port}}
	if err := lab.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("upgrade", "recover", "--previous", "--lab", dir, "--json", "--progress=json")
	if code != 0 {
		t.Fatalf("code %d: %s %s", code, out, stderr)
	}
	st, err := sup.Upgrade()
	if err != nil {
		t.Fatal(err)
	}
	if st.Upgrade != nil || st.Completed != nil {
		t.Fatalf("skip falsely completed an upgrade: %+v", st)
	}
	specs, err := supervisor.LoadNodes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(specs[0].Args, " "), "--unsafe-skip-upgrades 50") {
		t.Fatalf("missing skipped height: %v", specs[0].Args)
	}
	cfg, err = lab.Load(dir)
	if err != nil || cfg.Nodes[0].Version != "1.0.0" {
		t.Fatalf("wrong version: %+v %v", cfg.Nodes, err)
	}
	if !strings.Contains(stderr, "recovery.blocks") || !strings.Contains(out, `"mode":"previous"`) {
		t.Fatalf("missing result/progress: %s %s", out, stderr)
	}
}

func TestRecoveryWaitRequiresFreshBlocksAndRejectsExit(t *testing.T) {
	dir, sup, plan := recoveryLab(t)
	next := *plan
	next.Recovery = &supervisor.Recovery{ID: "attempt", Mode: "retry", Targets: plan.Previous}
	if _, err := sup.RecoverUpgrade(plan, &next); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Start(supervisor.All); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result":{"sync_info":{"latest_block_height":"100"}}}`)
	}))
	defer srv.Close()
	rpc, err := chain.New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	e := labEnv{dir: dir, cfg: lab.Config{Nodes: []lab.Node{{Name: "node0"}}}, clients: []*chain.Client{rpc}}
	ctx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
	defer cancel()
	if _, err := e.waitRecovery(ctx, sup, &next, nil); err == nil {
		t.Fatal("accepted stale heights")
	}
	if _, err := sup.Stop(supervisor.All, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.waitRecovery(t.Context(), sup, &next, nil); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("did not detect exited process: %v", err)
	}
	st, _ := sup.Upgrade()
	if st.Upgrade == nil || st.Completed != nil {
		t.Fatal("failed recovery lost pending plan")
	}
}

func TestOfflineUpgradeStatusRetainsFailureAndLab(t *testing.T) {
	dir, sup, plan := recoveryLab(t)
	if err := os.WriteFile(filepath.Join(dir, "node0", "node.log"), []byte("--usage flags\nerror during handshake: failed migration\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sup.Exit(); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("upgrade", "status", "--lab", dir, "--json")
	if code != 0 {
		t.Fatalf("code %d %s %s", code, out, stderr)
	}
	var envelope struct {
		Data upgradeStatusView `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	view := envelope.Data
	if view.Lab != "demo" || view.Pending == nil || view.Pending.Name != plan.Name || !strings.Contains(view.Nodes[0].SwapError, "failed migration") {
		t.Fatalf("lost offline evidence: %+v", view)
	}
	if _, err := supervisor.Dial(dir); err == nil {
		t.Fatal("status started a supervisor")
	}
}

func TestDelayedRetryExitRetainsOriginalTargetsAcrossRestart(t *testing.T) {
	dir, sup, original := recoveryLab(t)
	cfg, err := lab.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin := cfg.Profile.Binaries["2.0.0"].Path
	script := `#!/bin/sh
if [ "$1" = version ]; then echo 2.0.0; exit; fi
sleep 2
echo 'error during handshake: delayed migration failure'
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result":{"sync_info":{"latest_block_height":"49"}}}`)
	}))
	defer srv.Close()
	cfg.Nodes[0].Ports = []lab.Port{{Key: "rpc.laddr", Port: srv.Listener.Addr().(*net.TCPAddr).Port}}
	if err := lab.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	code, out, stderr := run("upgrade", "recover", "--version", "2.0.0", "--lab", dir, "--json")
	if code == 0 || !strings.Contains(out, "delayed migration failure") {
		t.Fatalf("code %d: %s %s", code, out, stderr)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("waited for block timeout after process exited")
	}
	st, err := sup.Upgrade()
	if err != nil {
		t.Fatal(err)
	}
	if st.Upgrade == nil || st.Upgrade.AutoSwap || !reflect.DeepEqual(st.Upgrade.Previous, original.Previous) || st.Upgrade.Recovery.Mode != "retry" {
		t.Fatalf("lost recovery evidence: %+v", st.Upgrade)
	}
	if err := sup.Exit(); err != nil {
		t.Fatal(err)
	}
	sup, err = ensureSupervisor(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err = sup.Upgrade()
	if err != nil {
		t.Fatal(err)
	}
	if st.Upgrade == nil || !reflect.DeepEqual(st.Upgrade.Previous, original.Previous) {
		t.Fatal("original target lost on supervisor restart")
	}
	if _, err := sup.CompleteUpgrade(original.Name); err == nil {
		t.Fatal("status could complete failed recovery")
	}
}

func TestOfflineRecoveryFreeze(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			dir, sup, plan := recoveryLab(t)
			if err := sup.Exit(); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "failed-target-started")
			bad := filepath.Join(dir, "failed-binary")
			if !missing {
				if err := os.WriteFile(bad, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			plan.Binary = bad
			plan.AutoSwap = true
			state := struct {
				Plan  *supervisor.Upgrade     `json:"plan"`
				Halts map[int]supervisor.Halt `json:"halts"`
			}{plan, map[int]supervisor.Halt{0: {Name: plan.Name, Height: plan.Height, Binary: "/bin/sh"}}}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(supervisor.Paths{Dir: dir}.Upgrade(), data, 0o600); err != nil {
				t.Fatal(err)
			}
			var height atomic.Int64
			height.Store(52)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"result":{"sync_info":{"latest_block_height":"%d"}}}`, height.Add(1))
			}))
			defer srv.Close()
			cfg, err := lab.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Nodes[0].Ports = []lab.Port{{Key: "rpc.laddr", Port: srv.Listener.Addr().(*net.TCPAddr).Port}}
			if err := lab.Save(dir, cfg); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := run("upgrade", "recover", "--previous", "--lab", dir, "--json")
			if code != 0 {
				t.Fatalf("code %d: %s %s", code, out, stderr)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("failed target ran during recovery supervisor startup")
			}
		})
	}
}
