package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/supervisor"
)

func writeLog(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "node0", "node.log"), []byte(text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gone reports whether pid no longer runs; a zombie has an empty cmdline.
func gone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err != nil || len(data) == 0
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type downOut struct {
	OK   bool `json:"ok"`
	Data struct {
		AlreadyDown bool                    `json:"already_down"`
		Nodes       []supervisor.NodeStatus `json:"nodes"`
	} `json:"data"`
}

func labDownJSON(t *testing.T, name string) downOut {
	t.Helper()
	code, stdout, stderr := run("lab", "down", name, "--json")
	var out downOut
	if code != 0 || json.Unmarshal([]byte(stdout), &out) != nil || !out.OK {
		t.Fatalf("lab down %s: code %d\n%s%s", name, code, stdout, stderr)
	}
	return out
}

// node logs needs no supervisor, so it shows which lab --lab resolved to.
func TestNodeLabResolvesNamesPathsAndDefaults(t *testing.T) {
	t.Setenv("FORKLAB_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("FORKLAB_TEST_CHILD", "1")

	code, _, stderr := run("node", "logs", "0")
	if code != 2 || !strings.Contains(stderr, "--lab") || !strings.Contains(stderr, "forklab lab create") {
		t.Errorf("no labs: code %d, %q; want usage error naming --lab", code, stderr)
	}

	alpha := addShellLab(t, "alpha")
	writeLog(t, alpha, "hello alpha")
	if code, stdout, stderr := run("node", "logs", "0"); code != 0 || stdout != "hello alpha\n" {
		t.Errorf("only lab: code %d, %q %q; want alpha picked", code, stdout, stderr)
	}

	beta := addShellLab(t, "beta")
	writeLog(t, beta, "hello beta")
	code, _, stderr = run("node", "logs", "0")
	if code != 2 || !strings.Contains(stderr, "alpha, beta") {
		t.Errorf("two labs: code %d, %q; want usage error listing both", code, stderr)
	}
	if code, stdout, _ := run("node", "logs", "0", "--lab", "beta"); code != 0 || stdout != "hello beta\n" {
		t.Errorf("--lab beta: code %d, %q", code, stdout)
	}
	if code, stdout, _ := run("node", "logs", "0", "--lab", alpha); code != 0 || stdout != "hello alpha\n" {
		t.Errorf("--lab <path>: code %d, %q", code, stdout)
	}
	t.Chdir(filepath.Join(alpha, "node0"))
	if code, stdout, _ := run("node", "logs", "0", "--lab", ".."); code != 0 || stdout != "hello alpha\n" {
		t.Errorf("--lab ..: code %d, %q", code, stdout)
	}
	t.Chdir(alpha)
	if code, stdout, _ := run("node", "logs", "0", "--lab", "."); code != 0 || stdout != "hello alpha\n" {
		t.Errorf("--lab .: code %d, %q", code, stdout)
	}
	if code, _, stderr := run("node", "logs", "0", "--lab", "gamma"); code != 1 || !strings.Contains(stderr, "lab not found") {
		t.Errorf("--lab gamma: code %d, %q; want lab not found", code, stderr)
	}

	if code, _, stderr := run("node", "list", "--lab", "beta"); code != 0 {
		t.Fatalf("node list --lab beta: code %d, %s", code, stderr)
	}
	if code, stdout, _ := run("node", "logs", "0"); code != 0 || stdout != "hello beta\n" {
		t.Errorf("beta running: code %d, %q; want the running lab picked", code, stdout)
	}
	if out := labDownJSON(t, "beta"); out.Data.AlreadyDown {
		t.Errorf("first down reported already down")
	}
	if out := labDownJSON(t, "beta"); !out.Data.AlreadyDown {
		t.Errorf("second down did not report already down")
	}
	if code, _, _ := run("node", "logs", "0"); code != 2 {
		t.Errorf("after down: code %d; want usage error again", code)
	}
}

func TestLabDownOfANeverStartedLabIsOK(t *testing.T) {
	newShellLab(t)
	if out := labDownJSON(t, "demo"); !out.Data.AlreadyDown || len(out.Data.Nodes) != 0 {
		t.Errorf("down of a never started lab = %+v", out.Data)
	}
	if code, _, stderr := run("lab", "down", "nope"); code != 1 || !strings.Contains(stderr, "lab not found") {
		t.Errorf("down of a missing lab: code %d, %q", code, stderr)
	}
}

func TestOrphanedNodesBlockDeleteUntilLabDown(t *testing.T) {
	dir := newShellLab(t)
	code, stdout, stderr := run("node", "start", "0", "--lab", "demo", "--json")
	if code != 0 {
		t.Fatalf("node start: code %d, %s", code, stderr)
	}
	node := nodesOf(t, stdout)[0].PID
	sup := pidIn(supervisor.Paths{Dir: dir}.Lock())
	if err := syscall.Kill(sup, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, sup)
	if gone(node) {
		t.Fatal("node died with its supervisor")
	}
	code, _, stderr = run("lab", "delete", "demo")
	if code != 1 || !strings.Contains(stderr, "forklab lab down demo") {
		t.Fatalf("delete with an orphaned node: code %d, %q; want refused with the down hint", code, stderr)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("refused delete removed the lab: %v", err)
	}

	out := labDownJSON(t, "demo")
	if out.Data.AlreadyDown || len(out.Data.Nodes) != 1 || out.Data.Nodes[0].State != supervisor.StateExited {
		t.Errorf("down after supervisor kill = %+v, want the adopted node stopped", out.Data)
	}
	waitGone(t, node)
	if out := labDownJSON(t, "demo"); !out.Data.AlreadyDown {
		t.Errorf("second down = %+v, want already down", out.Data)
	}
	if code, _, stderr := run("lab", "delete", "demo"); code != 0 {
		t.Fatalf("delete after down: code %d, %s", code, stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("lab dir after delete: %v", err)
	}
}

func TestLabDeleteIgnoresAStalePidFile(t *testing.T) {
	dir := newShellLab(t)
	dead := exec.Command("/bin/sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	stale := fmt.Sprintf(`{"pid":%d,"binary":"/bin/sh"}`, dead.Process.Pid)
	if err := os.WriteFile(filepath.Join(dir, "node0", "node.pid"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run("lab", "delete", "demo"); code != 0 {
		t.Fatalf("delete with a stale pid file: code %d, %s", code, stderr)
	}
}

func TestLabResetRequiresDownAndKeepsGenesis(t *testing.T) {
	dir := newShellLab(t)
	bin := fakeProfileLab(t, dir, "2.0.0")
	home := filepath.Join(dir, "node0")
	files := map[string]string{
		"data/blockstore.db/000001.log":   "blocks",
		"data/application.db/000001.log":  "state",
		"data/priv_validator_state.json":  `{"height":"42","round":0,"step":3}`,
		"config/genesis.json":             `{"chain_id":"demo"}`,
		"config/priv_validator_key.json":  "key",
		"config/node_key.json":            "node key",
		"config/addrbook.json":            "peers",
		"data/cs.wal/wal":                 "wal",
		"data/snapshots/metadata.db/0001": "snap",
	}
	for name, content := range files {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if code, _, stderr := run("node", "start", "0", "--lab", "demo"); code != 0 {
		t.Fatalf("node start: code %d, %s", code, stderr)
	}
	code, _, stderr := run("lab", "reset", "demo")
	if code != 1 || !strings.Contains(stderr, "forklab lab down demo") {
		t.Fatalf("reset of a running lab: code %d, %q; want refused with the down hint", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "data/blockstore.db")); err != nil {
		t.Fatalf("refused reset touched data: %v", err)
	}

	code, stdout, stderr := run("lab", "reset", "demo", "--force", "--json")
	if code != 0 || !strings.Contains(stdout, `"method":"manual"`) || !strings.Contains(stdout, `"version":"2.0.0"`) {
		t.Fatalf("reset --force: code %d\n%s%s", code, stdout, stderr)
	}
	if running(dir) {
		t.Error("supervisor still answers after reset --force")
	}
	if specs, err := supervisor.LoadNodes(dir); err != nil || specs[0].Binary != bin {
		t.Errorf("nodes.json after reset = %+v, %v; want the creation binary %s", specs, err, bin)
	}
	if c, err := lab.Load(dir); err != nil || c.Nodes[0].Version != "2.0.0" {
		t.Errorf("lab.yaml after reset = %+v, %v; want node0 back on 2.0.0", c, err)
	}
	entries, _ := os.ReadDir(filepath.Join(home, "data"))
	if len(entries) != 1 || entries[0].Name() != "priv_validator_state.json" {
		t.Errorf("data/ after reset = %v, want only priv_validator_state.json", entries)
	}
	var pv struct {
		Height string `json:"height"`
		Round  int    `json:"round"`
		Step   int    `json:"step"`
	}
	data, _ := os.ReadFile(filepath.Join(home, "data/priv_validator_state.json"))
	if err := json.Unmarshal(data, &pv); err != nil || pv.Height != "0" || pv.Round != 0 || pv.Step != 0 {
		t.Errorf("priv_validator_state.json = %s, want height 0", data)
	}
	for name, content := range files {
		if !strings.HasPrefix(name, "config/") {
			continue
		}
		if got, _ := os.ReadFile(filepath.Join(home, name)); string(got) != content {
			t.Errorf("%s = %q after reset, want it kept", name, got)
		}
	}
	if code, _, stderr := run("lab", "reset", "demo"); code != 0 {
		t.Errorf("reset of a down lab: code %d, %s", code, stderr)
	}
}
