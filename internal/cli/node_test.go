package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/supervisor"
	"go.yaml.in/yaml/v3"
)

// TestMain lets the node commands spawn this test binary as the supervisor:
// EnsureRunning re-executes os.Executable() with "supervisor run".
func TestMain(m *testing.M) {
	if os.Getenv("FORKLAB_TEST_CHILD") != "" && len(os.Args) > 1 && (os.Args[1] == "supervisor" || os.Args[1] == "upgrade") {
		os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// shellNode is a node that prints a greeting and exits 0 on SIGTERM.
const shellNode = `trap 'echo bye; exit 0' TERM; echo "hello from $1"; while :; do sleep 0.05; done`

func newShellLab(t *testing.T) string {
	t.Helper()
	t.Setenv("FORKLAB_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("FORKLAB_TEST_CHILD", "1")
	return addShellLab(t, "demo")
}

// addShellLab writes a one-node lab named name under $FORKLAB_HOME/labs whose
// node is shellNode, and kills its processes when the test ends.
func addShellLab(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("FORKLAB_HOME"), "labs", name)
	home := filepath.Join(dir, "node0")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := supervisor.NodeSpec{
		Index: 0, Name: "node0", Binary: "/bin/sh",
		Args:    []string{"-c", shellNode, "node", home},
		Home:    home,
		LogPath: filepath.Join(home, "node.log"),
		PidPath: filepath.Join(home, "node.pid"),
	}
	if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	config, err := yaml.Marshal(lab.Config{Name: name, Mode: lab.ModeFresh, Validators: 1, Nodes: []lab.Node{{Name: "node0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), config, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, p := range []string{supervisor.Paths{Dir: dir}.Lock(), spec.PidPath} {
			if pid := pidIn(p); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return dir
}

// pidIn reads a node pid file or supervisor.lock; 0 when there is none.
func pidIn(path string) int {
	data, _ := os.ReadFile(path)
	var pf struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(data, &pf) != nil {
		pf.PID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	return pf.PID
}

func nodesOf(t *testing.T, stdout string) []supervisor.NodeStatus {
	t.Helper()
	var env struct {
		OK   bool                    `json:"ok"`
		Data []supervisor.NodeStatus `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || !env.OK {
		t.Fatalf("not an ok envelope: %v: %q", err, stdout)
	}
	return env.Data
}

func TestNodeCommandsDriveASupervisor(t *testing.T) {
	dir := newShellLab(t)

	// Reading never starts a supervisor, so a poller cannot bring a stopped
	// lab back to life.
	code, stdout, stderr := run("node", "list", "--lab", dir, "--json")
	if code != 3 || !strings.Contains(stdout, `"lab_not_running"`) {
		t.Fatalf("node list of a stopped lab: code %d, %s%s; want 3", code, stdout, stderr)
	}
	if _, err := supervisor.Dial(dir); err == nil {
		t.Fatal("node list spawned a supervisor")
	}
	if running(dir) {
		t.Fatal("a lab with no supervisor and no node reports running")
	}

	code, stdout, stderr = run("node", "start", "0", "--lab", dir, "--json")
	if code != 0 {
		t.Fatalf("node start: code %d, %s", code, stderr)
	}
	pid := nodesOf(t, stdout)[0].PID
	if pid == 0 {
		t.Fatalf("node start reported no pid: %s", stdout)
	}
	logPath := filepath.Join(dir, "node0", "node.log")
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "hello from") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node never logged: %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, stdout, _ = run("node", "logs", "0", "--lab", dir, "--json")
	var line logLine
	if err := json.Unmarshal([]byte(strings.SplitN(stdout, "\n", 2)[0]), &line); code != 0 || err != nil || line.Node != 0 || !strings.HasPrefix(line.Line, "hello from") {
		t.Errorf("node logs --json: code %d, first line %q (%v)", code, stdout, err)
	}
	code, stdout, _ = run("node", "logs", "0", "--lab", dir)
	if code != 0 || !strings.HasPrefix(stdout, "hello from") {
		t.Errorf("node logs: code %d, %q", code, stdout)
	}

	code, stdout, _ = run("node", "logs", "0", "--tail", "1", "--lab", dir)
	if code != 0 || strings.Count(stdout, "\n") != 1 {
		t.Errorf("node logs --tail 1: code %d, %q; want one line", code, stdout)
	}

	code, stdout, _ = run("node", "list", "--lab", dir)
	if code != 0 || !strings.Contains(stdout, "running") || !strings.Contains(stdout, strconv.Itoa(pid)) {
		t.Errorf("human node list: code %d\n%s", code, stdout)
	}
	if !running(dir) {
		t.Error("a lab with a live node reports stopped")
	}

	code, stdout, stderr = run("node", "stop", "all", "--lab", dir, "--json", "--timeout", "3s")
	if code != 0 {
		t.Fatalf("node stop: code %d, %s", code, stderr)
	}
	if n := nodesOf(t, stdout)[0]; n.State != supervisor.StateExited || n.ExitCode == nil || *n.ExitCode != 0 {
		t.Errorf("after stop = %+v", n)
	}
	if running(dir) {
		t.Error("a lab whose supervisor answers but whose nodes all stopped reports running")
	}

	code, _, stderr = run("supervisor", "down", "--lab", dir, "--json")
	if code != 0 {
		t.Fatalf("supervisor down: code %d, %s", code, stderr)
	}
	code, stdout, _ = run("supervisor", "exit", "--lab", dir, "--json")
	if code != 3 || !strings.Contains(stdout, `"lab_not_running"`) {
		t.Errorf("supervisor exit after down: code %d, %s; want 3", code, stdout)
	}
}
