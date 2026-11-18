//go:build e2e

package cli

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
)

// TestE2ECosmovisorExrpd upgrades fresh two-validator exrpd labs that run
// under a real cosmovisor from 11.1.1 to 11.2.0: cosmovisor swaps a binary
// forklab placed, cosmovisor downloads the binary from the plan info, the
// user restarts halted nodes by version after --no-auto-swap, and a halted
// lab is recovered onto the previous binary.
func TestE2ECosmovisorExrpd(t *testing.T) {
	oldBin, newBin, cosmovisor := os.Getenv("FORKLAB_EXRPD_OLD"), os.Getenv("FORKLAB_EXRPD_NEW"), os.Getenv("FORKLAB_COSMOVISOR")
	if oldBin == "" || newBin == "" || cosmovisor == "" {
		t.Skip("FORKLAB_EXRPD_OLD, FORKLAB_EXRPD_NEW, or FORKLAB_COSMOVISOR not set")
	}
	config := t.TempDir()
	t.Setenv("FORKLAB_CONFIG_DIR", config)
	t.Setenv("FORKLAB_HOME", t.TempDir())
	t.Setenv("FORKLAB_TEST_CHILD", "1")
	e, err := profile.Store{Dir: t.TempDir()}.Get("xrplevm")
	if err != nil {
		t.Fatal(err)
	}
	store := profile.Store{Dir: filepath.Join(config, "profiles")}
	e.Doc.Binaries = map[string]profile.BinaryDocument{exrpdOld: {Path: oldBin}, exrpdNew: {Path: newBin}}
	if _, err := store.Save(e.Doc); err != nil {
		t.Fatal(err)
	}
	badBin := filepath.Join(t.TempDir(), "bad-exrpd")
	if err := os.WriteFile(badBin, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 11.3.0; exit 0; fi\necho 'bad binary refuses to start' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.Doc.Binaries["11.3.0"] = profile.BinaryDocument{Path: badBin}
	if _, err := store.Save(e.Doc); err != nil {
		t.Fatal(err)
	}
	create := []string{"--cosmovisor-bin", cosmovisor}
	t.Run("placement", func(t *testing.T) { cosmovisorPlacement(t, "cvplace", newBin, create) })
	t.Run("manual-swap", func(t *testing.T) { manualSwap(t, "cvmanual", newBin, create...) })
	t.Run("recover-previous", func(t *testing.T) { cosmovisorRecoverPrevious(t, "cvprev", create) })
	t.Run("recover-version", func(t *testing.T) { cosmovisorRecoverVersion(t, "cvretry", newBin, create) })
	t.Run("download", func(t *testing.T) {
		var gets atomic.Int32
		files := http.FileServer(http.Dir(releaseArchive(t, newBin)))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/exrpd.tar.gz" {
				gets.Add(1)
			}
			files.ServeHTTP(w, r)
		}))
		defer srv.Close()
		e.Doc.Binaries[exrpdNew] = profile.BinaryDocument{URL: srv.URL + "/exrpd.tar.gz"}
		if _, err := store.Save(e.Doc); err != nil {
			t.Fatal(err)
		}
		cosmovisorDownload(t, "cvdl", srv.URL, &gets, append(create, "--cosmovisor-download"))
	})
}

// cosmovisorPlacement checks that cosmovisor, not forklab, swaps the binary
// forklab placed: the supervised pids outlive the upgrade. It then resets
// the lab onto the creation release and checks that neither lab down, nor
// node kill, nor a cosmovisor killed behind forklab's back leaves a chain
// process.
func cosmovisorPlacement(t *testing.T, name, newBin string, create []string) {
	dir, c := upLab(t, name, create...)
	before := forklabData[[]supervisor.NodeStatus](t, "node", "list", "--lab", dir)
	v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", name)
	if !v.Upgraded || v.Height < v.Plan.Height+3 || v.Plan.Binary != newBin {
		t.Fatalf("schedule = %+v", v)
	}
	checkUpgraded(t, name, dir, c, v.Plan.Height, newBin)
	after := forklabData[[]supervisor.NodeStatus](t, "node", "list", "--lab", dir)
	for i, n := range after {
		if !n.Cosmovisor || n.PID != before[i].PID {
			t.Errorf("%s = %+v, want cosmovisor pid %d kept across the upgrade", n.Name, n, before[i].PID)
		}
		home := filepath.Join(dir, n.Name)
		if link, _ := os.Readlink(filepath.Join(home, "cosmovisor", "current")); link != "upgrades/v"+exrpdNew {
			t.Errorf("%s current -> %q, want the plan's slot", n.Name, link)
		}
		log, _ := os.ReadFile(filepath.Join(home, "node.log"))
		if !strings.Contains(string(log), "upgrade detected, relaunching") {
			t.Errorf("%s log lacks cosmovisor's relaunch line", n.Name)
		}
	}
	if code, stdout, _ := run("lab", "show", name); code != 0 || !strings.Contains(stdout, "forklab places upgrade binaries") {
		t.Errorf("lab show: code %d\n%s", code, stdout)
	}
	if code, stdout, _ := run("node", "list", "--lab", name); code != 0 || !strings.Contains(stdout, "(cosmovisor)") {
		t.Errorf("node list: code %d\n%s", code, stdout)
	}

	// A stop after the upgrade reaches the chain binary through cosmovisor:
	// it shuts down by itself and cosmovisor exits 0. Cosmovisor v1.7.3
	// instead exits 1 at once and leaves the binary to be killed.
	logPath := filepath.Join(dir, "node1", "node.log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	stopped := forklabData[[]supervisor.NodeStatus](t, "node", "stop", "1", "--lab", dir)
	log, _ := os.ReadFile(logPath)
	if n := stopped[1]; n.State != supervisor.StateExited || n.ExitCode == nil || *n.ExitCode != 0 || !strings.Contains(string(log[info.Size():]), "Closing application.db") {
		t.Errorf("after the upgrade node stop 1 = %+v, want exit code 0 and a chain binary that shut down by itself; it logged:\n%s", n, tail(log[info.Size():], 1500))
	}
	if procs := labProcesses(filepath.Join(dir, "node1")); len(procs) != 0 {
		t.Errorf("after node stop 1: %v still run", procs)
	}
	forklab(t, "node", "start", "1", "--lab", dir)

	r := forklabData[labReset](t, "lab", "reset", name, "--force")
	if r.Version != exrpdOld {
		t.Fatalf("reset = %+v, want version %s", r, exrpdOld)
	}
	if procs := labProcesses(dir); len(procs) != 0 {
		t.Fatalf("after lab reset --force: %v still run", procs)
	}
	upped := forklabData[labUp](t, "lab", "up", name)
	for _, n := range upped.Nodes {
		link, _ := os.Readlink(filepath.Join(dir, n.Name, "cosmovisor", "current"))
		if n.Height < 2 || n.Binary != oldBin(t) || link != "genesis" {
			t.Errorf("after reset %s = height %d on %s, current -> %q; want blocks on %s from genesis", n.Name, n.Height, n.Binary, link, oldBin(t))
		}
	}

	forklab(t, "node", "kill", "1", "--lab", dir)
	if err := syscall.Kill(upped.Nodes[0].PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		procs := labProcesses(filepath.Join(dir, "node"))
		if len(procs) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after node kill and a SIGKILL of cosmovisor: %v still run", procs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	forklab(t, "lab", "down", name)
	if procs := labProcesses(dir); len(procs) != 0 {
		t.Errorf("after lab down: %v still run", procs)
	}
}

// labProcesses lists the processes whose command line mentions prefix.
func labProcesses(prefix string) []string {
	var out []string
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if cmdline, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline"); strings.Contains(string(cmdline), prefix) {
			out = append(out, e.Name()+" "+strings.ReplaceAll(string(cmdline), "\x00", " "))
		}
	}
	return out
}

// cosmovisorRecoverPrevious lets the lab halt with no binary for the plan,
// so cosmovisor exits, and recovers it onto the previous release, which
// skips the plan height. A later down and up must stay on that release:
// the chain's upgrade-info.json still names the plan.
func cosmovisorRecoverPrevious(t *testing.T, name string, create []string) {
	dir, c := upLab(t, name, create...)
	v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--no-auto-swap", "--lab", name)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		st := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
		stopped := 0
		for _, n := range st.Nodes {
			if n.Phase == supervisor.SwapHalted && n.State == supervisor.StateExited {
				stopped++
			}
		}
		if stopped == len(st.Nodes) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes never halted and exited: %+v", st)
		}
		time.Sleep(time.Second)
	}
	r := forklabData[recoveryView](t, "upgrade", "recover", "--previous", "--lab", name)
	if r.Mode != "previous" || r.Height <= v.Plan.Height {
		t.Fatalf("recover = %+v, want blocks past %d on the previous binary", r, v.Plan.Height)
	}
	forklab(t, "lab", "down", name)
	upped := forklabData[labUp](t, "lab", "up", name)
	for _, n := range upped.Nodes {
		link, _ := os.Readlink(filepath.Join(dir, n.Name, "cosmovisor", "current"))
		if n.Height <= v.Plan.Height || n.Binary != oldBin(t) || link != "forklab-manual" {
			t.Errorf("after recovery %s = height %d on %s, current -> %q; want blocks past %d on %s", n.Name, n.Height, n.Binary, link, v.Plan.Height, oldBin(t))
		}
	}
	st := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
	if st.Pending != nil {
		t.Errorf("after recovery status = %+v, want no pending plan", st)
	}
	for _, n := range c.Nodes {
		log, _ := os.ReadFile(nodeLog(dir, n))
		if !strings.Contains(supervisor.StripANSI(string(log)), `UPGRADE "v`+exrpdNew+`" SKIPPED`) {
			t.Errorf("%s log lacks the skipped upgrade", n.Name)
		}
	}
}

// cosmovisorRecoverVersion schedules a plan whose placed binary cannot
// start: cosmovisor switches to it and exits when it dies. The recovery
// retries the plan on the real release, which forklab puts in the plan's
// slot before it starts cosmovisor again.
func cosmovisorRecoverVersion(t *testing.T, name, newBin string, create []string) {
	dir, c := upLab(t, name, create...)
	code, stdout, _ := run("upgrade", "schedule", "11.3.0", "--name", "v"+exrpdNew, "--in", planIn, "--lab", name, "--json")
	if code != 1 {
		t.Fatalf("schedule onto a bad binary: code %d, %s; want a failure", code, stdout)
	}
	var st upgradeStatusView
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) {
		st = forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
		stopped := 0
		for _, n := range st.Nodes {
			if n.State == supervisor.StateExited {
				stopped++
			}
		}
		if stopped == len(st.Nodes) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes = %+v, want cosmovisor to exit on every one", st.Nodes)
		}
	}
	for _, n := range c.Nodes {
		log, _ := os.ReadFile(nodeLog(dir, n))
		if !strings.Contains(string(log), "bad binary refuses to start") {
			t.Errorf("%s: cosmovisor never ran the bad binary", n.Name)
		}
	}
	r := forklabData[recoveryView](t, "upgrade", "recover", "--version", exrpdNew, "--lab", name)
	if r.Mode != "retry" || r.Height <= st.Pending.Height {
		t.Fatalf("recover = %+v, want blocks past %d on %s", r, st.Pending.Height, exrpdNew)
	}
	for _, n := range r.Nodes {
		link, _ := os.Readlink(filepath.Join(dir, n.Name, "cosmovisor", "current"))
		if n.Version != exrpdNew || n.Binary != newBin || n.State != supervisor.StateRunning || link != "upgrades/v"+exrpdNew {
			t.Errorf("after recovery %s = %+v, current -> %q; want %s running from the plan's slot", n.Name, n, link, newBin)
		}
	}
	after := forklabData[upgradeStatusView](t, "upgrade", "status", "--lab", name)
	if after.Pending != nil || after.Completed == nil || after.Completed.Version != exrpdNew {
		t.Errorf("after recovery pending = %+v, completed = %+v; want the plan completed on %s", after.Pending, after.Completed, exrpdNew)
	}
}

// cosmovisorDownload checks that cosmovisor fetches the release from the
// link in the plan info, checksum included: the server sees forklab's own
// download and then one from each node's cosmovisor. A binary that forklab
// placed would leave the server with the first one only.
func cosmovisorDownload(t *testing.T, name, url string, gets *atomic.Int32, create []string) {
	dir, c := upLab(t, name, create...)
	v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", name)
	if !v.Upgraded || v.Height < v.Plan.Height+3 {
		t.Fatalf("schedule = %+v", v)
	}
	checkUpgraded(t, name, dir, c, v.Plan.Height, v.Plan.Binary)
	if code, stdout, _ := run("gov", "show", "1", "--lab", name, "--json"); code != 0 || !strings.Contains(stdout, url+"/exrpd.tar.gz?checksum=sha256:") {
		t.Errorf("proposal lacks the download link: code %d\n%s", code, stdout)
	}
	for _, n := range c.Nodes {
		log, _ := os.ReadFile(nodeLog(dir, n))
		if !strings.Contains(string(log), "upgrade detected, relaunching") {
			t.Errorf("%s log lacks cosmovisor's relaunch line", n.Name)
		}
		if _, err := os.Stat(filepath.Join(dir, n.Name, "cosmovisor", "upgrades", "v"+exrpdNew, "bin", "exrpd")); err != nil {
			t.Errorf("%s: cosmovisor left no downloaded binary: %v", n.Name, err)
		}
	}
	if got, want := int(gets.Load()), 1+len(c.Nodes); got < want {
		t.Errorf("the release was downloaded %d times, want at least %d: forklab once and each cosmovisor once", got, want)
	}
}

// releaseArchive packs bin as bin/exrpd in exrpd.tar.gz, the shape of an
// xrplevm release, and returns the directory holding it.
func releaseArchive(t *testing.T, bin string) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "exrpd.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "bin/exrpd", Mode: 0o755, Size: info.Size()}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tw, src); err != nil {
		t.Fatal(err)
	}
	for _, c := range []io.Closer{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
