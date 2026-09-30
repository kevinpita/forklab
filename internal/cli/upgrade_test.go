package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
	"go.yaml.in/yaml/v3"
)

func TestUpgradeStatusReportsACompletedUpgrade(t *testing.T) {
	var b strings.Builder
	done := &supervisor.Upgrade{Name: "v11.2.0", Height: 48, Version: "11.2.0", AutoSwap: true}
	if err := (upgradeStatusView{Completed: done}).WriteHuman(&b); err != nil {
		t.Fatal(err)
	}
	if want := "supervisor: last upgrade \"v11.2.0\" to version 11.2.0 completed at height 48\n"; !strings.Contains(b.String(), want) || strings.Contains(b.String(), "auto swap") {
		t.Errorf("status =\n%s\nwant the line %q and no pending plan", b.String(), want)
	}
}

func TestUpgradeScheduleNeedsExactlyOneHeightFlag(t *testing.T) {
	for _, args := range [][]string{
		{"upgrade", "schedule", "1.0"},
		{"upgrade", "schedule", "1.0", "--height", "10", "--in", "10"},
		{"upgrade", "schedule"},
	} {
		if code, _, stderr := run(args...); code != 2 {
			t.Errorf("%v: code %d, %s; want usage error 2", args, code, stderr)
		}
	}
}

func TestLastErrorLinesStripsColorsAndKeepsTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.log")
	log := "INF committed height=1\n" +
		"\x1b[31mERR\x1b[0m first\n" +
		"INF committed height=2\n" +
		"panic: boom\n" +
		"\x1b[31mERR\x1b[0m CONSENSUS FAILURE!!!\n" +
		"INF service stop\n"
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := lastErrorLines(path, 2), "  panic: boom\n  ERR CONSENSUS FAILURE!!!"; got != want {
		t.Errorf("lastErrorLines = %q, want %q", got, want)
	}
	if got := lastErrorLines(path, 10); strings.Count(got, "\n") != 2 || !strings.Contains(got, "ERR first") {
		t.Errorf("lastErrorLines(10) = %q, want the three error lines", got)
	}
	if got := lastErrorLines(filepath.Join(t.TempDir(), "missing"), 2); !strings.Contains(got, "no such file") {
		t.Errorf("missing log = %q", got)
	}
}

func TestRecordVersionWritesLabYAML(t *testing.T) {
	dir := newShellLab(t)
	record := recordVersion(dir)
	if err := record(1, "2.0"); err == nil {
		t.Error("recording a version for a node the lab lacks succeeded")
	}
	if err := record(0, "2.0"); err != nil {
		t.Fatal(err)
	}
	c, err := lab.Load(dir)
	if err != nil || c.Nodes[0].Version != "2.0" || c.Version != "" {
		t.Errorf("lab.yaml = version %q, node0 %+v, %v; want node0 on 2.0 and the creation version untouched", c.Version, c.Nodes[0], err)
	}
}

// fakeChain is a chain binary stand-in that reports version 2.0.0 and
// otherwise runs its arguments as a shell node.
const fakeChain = `#!/bin/sh
case "$1" in version) echo 2.0.0 ;; *) exec /bin/sh "$@" ;; esac
`

// fakeProfileLab rewrites the shell lab's lab.yaml with a profile snapshot
// whose only binary, version 2.0.0, is fakeChain, created at version
// created. It returns the binary's path.
func fakeProfileLab(t *testing.T, dir, created string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "chaind")
	if err := os.WriteFile(bin, []byte(fakeChain), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := profile.Store{Dir: t.TempDir()}.Get("simd")
	if err != nil {
		t.Fatal(err)
	}
	e.Doc.BinaryName = "chaind"
	e.Doc.Binaries = map[string]profile.BinaryDocument{"2.0.0": {Path: bin}}
	c := lab.Config{Name: "demo", Mode: lab.ModeFresh, Version: created, Validators: 1, Profile: e.Doc, Nodes: []lab.Node{{Name: "node0", Version: "1.0.0"}}}
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestNodeRestartResolvesAVersionAndRecordsIt restarts the shell lab's node
// on a profile version: the binary comes from the lab's profile snapshot
// and the supervisor writes the version into lab.yaml.
func TestNodeRestartResolvesAVersionAndRecordsIt(t *testing.T) {
	dir := newShellLab(t)
	bin := fakeProfileLab(t, dir, "1.0.0")
	t.Cleanup(func() { run("supervisor", "down", "--lab", dir, "--json") })

	if code, stdout, _ := run("node", "restart", "0", "--lab", dir, "--json", "--binary", "9.9.9"); code != 1 || !strings.Contains(stdout, "no binary version 9.9.9") {
		t.Errorf("restart on an unknown version: code %d, %s; want the profile's complaint", code, stdout)
	}
	if code, stdout, _ := run("node", "restart", "0", "--lab", dir, "--json", "--binary", bin); code != 2 || !strings.Contains(stdout, "--version") {
		t.Errorf("restart on a bare path: code %d, %s; want a usage error asking for --version", code, stdout)
	}
	code, stdout, stderr := run("node", "restart", "0", "--lab", dir, "--json", "--binary", "2.0.0", "--timeout", "3s")
	if code != 0 {
		t.Fatalf("node restart: code %d, %s", code, stderr)
	}
	if n := nodesOf(t, stdout)[0]; n.State != supervisor.StateRunning || n.Binary != bin {
		t.Errorf("after restart = %+v, want running on %s", n, bin)
	}
	c, err := lab.Load(dir)
	if err != nil || c.Nodes[0].Version != "2.0.0" || c.Version != "1.0.0" {
		t.Errorf("lab.yaml = %+v, %v; want node0 on 2.0.0 and the lab created at 1.0.0", c, err)
	}
	specs, err := supervisor.LoadNodes(dir)
	if err != nil || specs[0].Binary != bin {
		t.Errorf("nodes.json = %+v, %v; want %s", specs, err, bin)
	}
}

func TestUpgradeCancelWarnsBeforeSubmitting(t *testing.T) {
	dir, port := fakeExecLab(t, 0, false)
	status, err := os.ReadFile("../chain/testdata/simd/status.json")
	if err != nil {
		t.Fatal(err)
	}
	// One block of history: the block time falls back to the profile's.
	status = []byte(strings.Replace(string(status), `"earliest_block_height": "83"`, `"earliest_block_height": "99"`, 1))
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(status) }))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	testdata, err := filepath.Abs("../chain/testdata/simd/cli")
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1 $2 $3" in
"q upgrade plan") echo '{"plan":{"name":"v2","height":"101"}}' ;;
"q gov params") cat ` + testdata + `/gov_params.json ;;
"q auth module-account") cat ` + testdata + `/module_account_gov.json ;;
*) echo "submit refused" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "chaind"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run("upgrade", "cancel", "--lab", dir, "--json")
	if code != 1 || !strings.Contains(stdout, "submit refused") {
		t.Fatalf("code %d, stdout %s; want the submission to fail", code, stdout)
	}
	if !strings.Contains(stderr, "warning: the cancel vote may end after the plan height") {
		t.Errorf("stderr %q lacks the warning printed before submitting", stderr)
	}
}
