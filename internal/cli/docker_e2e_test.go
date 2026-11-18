//go:build e2e

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
)

// dockerLab is upLab for a lab on the docker runtime. However the test
// ends, the lab's containers and network leave the engine with it. What a
// failed test leaves is kept in a directory the test names: the engine's
// view of the lab and each node's peers while the lab still runs, then the
// logs, configuration, and address books.
func dockerLab(t *testing.T, name string, create ...string) (string, lab.Config) {
	t.Helper()
	l, err := labs()
	if err != nil {
		t.Fatal(err)
	}
	kept := ""
	keep := func(file string, data []byte) {
		if kept == "" {
			if kept, err = os.MkdirTemp("", "forklab-e2e-"+name+"-"); err != nil {
				t.Logf("keep %s: %v", file, err)
				return
			}
			t.Logf("the state and logs of the failed lab are kept in %s", kept)
		}
		if err := os.WriteFile(filepath.Join(kept, file), data, 0o644); err != nil {
			t.Logf("keep %s: %v", file, err)
		}
	}
	// Registered first, so it runs after upLab's own cleanup, which takes
	// the lab down.
	t.Cleanup(func() {
		if dir, err := l.Path(name); err == nil && t.Failed() {
			files, _ := filepath.Glob(filepath.Join(dir, "node*", "node.log"))
			more, _ := filepath.Glob(filepath.Join(dir, "node*", "config", "*"))
			for _, f := range append(append(files, more...), supervisor.Paths{Dir: dir}.Log()) {
				if base := filepath.Base(f); base != "node.log" && base != "config.toml" && base != "addrbook.json" && base != "supervisor.log" {
					continue
				}
				if data, err := os.ReadFile(f); err == nil {
					rel, _ := filepath.Rel(dir, f)
					keep(strings.ReplaceAll(rel, string(filepath.Separator), "-"), data)
				}
			}
		}
		var out bytes.Buffer
		if code := Run([]string{"lab", "delete", name, "--force"}, &out, &out); code != 0 && !strings.Contains(out.String(), "not found") {
			t.Errorf("lab delete %s --force in the cleanup: exit %d\n%s", name, code, out.String())
		}
	})
	dir, c := upLab(t, name, append([]string{"--runtime", "docker"}, create...)...)
	// Registered last, so it runs first, while the lab is still up.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		var engine bytes.Buffer
		for _, args := range [][]string{{"version"}, {"ps", "-a", "--no-trunc"}, {"network", "inspect", supervisor.NetworkName(name)}} {
			out, err := exec.Command("docker", args...).CombinedOutput()
			fmt.Fprintf(&engine, "$ docker %s\n%s(%v)\n\n", strings.Join(args, " "), out, err)
		}
		keep("engine.txt", engine.Bytes())
		for _, n := range c.Nodes {
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/net_info", n.RPCPort()))
			if err != nil {
				keep(n.Name+"-net_info.json", []byte(err.Error()))
				continue
			}
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			keep(n.Name+"-net_info.json", data)
		}
	})
	return dir, c
}

// TestE2EDockerExrpd runs exrpd labs on the docker runtime against the
// engine the docker CLI reaches: four validators that an operator can log
// in to, the lifecycle commands and their cleanup, and the 11.1.1 to 11.2.0
// upgrade swapped by the supervisor and by cosmovisor in the container.
// The labs publish their ports on the host, so it cannot run in a private
// network namespace.
func TestE2EDockerExrpd(t *testing.T) {
	oldBin, newBin := os.Getenv("FORKLAB_EXRPD_OLD"), os.Getenv("FORKLAB_EXRPD_NEW")
	if os.Getenv("FORKLAB_E2E_DOCKER") == "" || oldBin == "" || newBin == "" {
		t.Skip("FORKLAB_E2E_DOCKER, FORKLAB_EXRPD_OLD, or FORKLAB_EXRPD_NEW not set")
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
	t.Run("four-validators", func(t *testing.T) { dockerLifecycle(t, "dk4") })
	t.Run("recreated", func(t *testing.T) { dockerRecreated(t, "dkre") })
	t.Run("upgrade", func(t *testing.T) {
		dir, c := dockerLab(t, "dkup")
		v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", "dkup")
		if !v.Upgraded || v.Height < v.Plan.Height+3 {
			t.Fatalf("schedule = %+v", v)
		}
		checkUpgraded(t, "dkup", dir, c, v.Plan.Height, newBin)
		// A running lab deleted with --force is brought down first.
		forklab(t, "lab", "delete", "dkup", "--force")
		engineClean(t, "dkup", "lab delete --force")
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("lab directory survived delete --force: %v", err)
		}
	})
	t.Run("cosmovisor", func(t *testing.T) {
		cosmovisor := os.Getenv("FORKLAB_COSMOVISOR")
		if cosmovisor == "" {
			t.Skip("FORKLAB_COSMOVISOR not set")
		}
		dir, c := dockerLab(t, "dkcv", "--cosmovisor-bin", cosmovisor)
		before := containerIDs(t, "dkcv", 2)
		v := forklabData[scheduleView](t, "upgrade", "schedule", exrpdNew, "--in", planIn, "--lab", "dkcv")
		if !v.Upgraded || v.Height < v.Plan.Height+3 {
			t.Fatalf("schedule = %+v", v)
		}
		checkUpgraded(t, "dkcv", dir, c, v.Plan.Height, newBin)
		if after := containerIDs(t, "dkcv", 2); strings.Join(after, " ") != strings.Join(before, " ") {
			t.Errorf("containers = %v, want %v kept: cosmovisor swaps inside them", after, before)
		}
		for _, n := range c.Nodes {
			log, _ := os.ReadFile(nodeLog(dir, n))
			if !strings.Contains(string(log), "upgrade detected, relaunching") {
				t.Errorf("%s log lacks cosmovisor's relaunch line", n.Name)
			}
		}
		if out := dockerCLI(t, "exec", supervisor.ContainerName("dkcv", "node0"), "sh", "-c", "exrpd version"); strings.TrimSpace(out) != exrpdNew {
			t.Errorf("exrpd on PATH in the container reports %q, want %s", out, exrpdNew)
		}
		// A stop after the upgrade lets the chain binary shut down: with
		// cosmovisor v1.7.3 the container at times exits 1 instead.
		for _, n := range forklabData[[]supervisor.NodeStatus](t, "node", "stop", "all", "--lab", dir) {
			if n.State != supervisor.StateExited || n.ExitCode == nil || *n.ExitCode != 0 {
				t.Errorf("after the upgrade node stop %s = %+v, want exit code 0", n.Name, n)
			}
		}
		downClean(t, "dkcv")
	})
}

// dockerLifecycle brings four validators up in containers and checks what
// an operator relies on: peers, a transaction from the host, a shell in a
// node, files that stay the host user's, adoption by a new supervisor, and
// that stopping, killing, pausing, and taking the lab down leave nothing
// behind in the engine.
func dockerLifecycle(t *testing.T, name string) {
	dir, c := dockerLab(t, name, "--validators", "4")
	waitMesh(t, name, 4, 2)
	nodes := forklabData[[]supervisor.NodeStatus](t, "node", "list", "--lab", dir)
	for _, n := range nodes {
		if n.Container != supervisor.ContainerName(name, n.Name) || n.State != supervisor.StateRunning || n.PID != 0 {
			t.Errorf("node list %s = %+v, want it running in its container", n.Name, n)
		}
	}
	if code, stdout, _ := run("node", "logs", "3", "--tail", "5", "--lab", dir); code != 0 || strings.Count(stdout, "\n") != 5 {
		t.Errorf("node logs: code %d\n%s", code, stdout)
	}
	if code, stdout, _ := run("lab", "show", name); code != 0 || !strings.Contains(stdout, "docker exec -it forklab-"+name+"-node0 bash") {
		t.Errorf("lab show: code %d\n%s", code, stdout)
	}
	if code, stdout, _ := run("lab", "list"); code != 0 || !strings.Contains(stdout, "running, docker") {
		t.Errorf("lab list: code %d\n%s", code, stdout)
	}
	forklab(t, "account", "send", "test0", "test1", "1000", "--lab", name)

	// A shell in the container finds the chain binary, the node, and the
	// lab keys without a flag.
	shell := dockerCLI(t, "exec", supervisor.ContainerName(name, "node2"), "sh", "-c", "command -v exrpd && exrpd status && exrpd keys list")
	if !strings.Contains(shell, "/exrpd\n") || !strings.Contains(shell, c.Nodes[2].NodeID) || !strings.Contains(shell, "name: test0") {
		t.Errorf("in the container: %s\nwant exrpd on PATH, node2's own status, and the lab keys", shell)
	}

	// A new supervisor adopts the containers.
	before := containerIDs(t, name, 4)
	lock, _ := os.ReadFile(supervisor.Paths{Dir: dir}.Lock())
	old, _ := strconv.Atoi(strings.TrimSpace(string(lock)))
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if _, err := ensureSupervisor(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range forklabData[[]supervisor.NodeStatus](t, "node", "list", "--lab", dir) {
		if !n.Adopted || n.State != supervisor.StateRunning {
			t.Errorf("after a new supervisor %s = %+v, want it adopted", n.Name, n)
		}
	}
	if after := containerIDs(t, name, 4); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Errorf("containers = %v, want %v left alone by the new supervisor", after, before)
	}

	// Stop removes the container, kill reads as a signal, restart and the
	// pause barrier bring new ones.
	forklab(t, "node", "stop", "1", "--lab", dir)
	if left := labContainers(t, name); len(left) != 3 {
		t.Errorf("after node stop 1 containers = %v, want the other three", left)
	}
	forklab(t, "node", "start", "1", "--lab", dir)
	killed := forklabData[[]supervisor.NodeStatus](t, "node", "kill", "1", "--lab", dir)
	if killed[1].State != supervisor.StateExited || killed[1].Signal != "killed" {
		t.Errorf("after node kill 1 = %+v, want it killed", killed[1])
	}
	forklab(t, "node", "restart", "1", "--lab", dir)
	forklab(t, "node", "restart", "0", "--lab", dir)
	st := forklabData[statusView](t, "status", "--lab", name)
	forklab(t, "lab", "pause", name, "--height", strconv.FormatInt(st.Height+8, 10))
	forklab(t, "lab", "resume", name)
	if left := labContainers(t, name); len(left) != 4 {
		t.Errorf("after resume containers = %v, want four", left)
	}

	// Every file is the host user's, so the user can delete the lab.
	_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			t.Errorf("walk %s: %v", path, err)
			return nil
		}
		if info, err := os.Lstat(path); err == nil && int(info.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
			t.Errorf("%s is owned by uid %d, want %d", path, info.Sys().(*syscall.Stat_t).Uid, os.Getuid())
		}
		return nil
	})
	downClean(t, name)

	// A lab whose supervisor died and whose containers stopped on their own
	// still has them, and its network, in the engine. Delete removes all.
	forklab(t, "lab", "up", name)
	lock, _ = os.ReadFile(supervisor.Paths{Dir: dir}.Lock())
	old, _ = strconv.Atoi(strings.TrimSpace(string(lock)))
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	dockerCLI(t, append([]string{"kill"}, labContainers(t, name)...)...)
	time.Sleep(2 * time.Second)
	if left := dockerCLI(t, "ps", "-a", "--format", "{{.Names}}", "--filter", "name=forklab-"+name+"-"); strings.Count(left, "\n") != 4 {
		t.Fatalf("exited containers = %q, want four left by the dead supervisor", left)
	}
	forklab(t, "lab", "delete", name)
	if left, nets := dockerCLI(t, "ps", "-a", "--format", "{{.Names}}", "--filter", "name=forklab-"+name+"-"), dockerCLI(t, "network", "ls", "--format", "{{.Name}}", "--filter", "name=forklab-"+name); left != "" || nets != "" {
		t.Errorf("after lab delete containers %q and networks %q remain", left, nets)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("lab directory survived the delete: %v", err)
	}
}

// dockerRecreated checks that the nodes find each other again after every
// container was removed and the nodes came back one by one, the first
// while no other container existed. CometBFT keeps an address for each
// peer for as long as the process runs, and one in the address book on
// disk for good, so a node whose address changes when its container is
// recreated can be lost to its peers: every address a node holds for a
// peer must be the one that peer's container has.
func dockerRecreated(t *testing.T, name string) {
	dir, c := dockerLab(t, name, "--validators", "3")
	first := waitMesh(t, name, 3, 2)
	before := containerIPs(t, name, c)

	forklab(t, "node", "stop", "all", "--lab", dir)
	if left := dockerCLI(t, "ps", "-a", "--format", "{{.Names}}", "--filter", "name=forklab-"+name+"-"); left != "" {
		t.Fatalf("containers after node stop all: %q, want none", left)
	}
	forklab(t, "node", "start", "0", "--lab", dir)
	// node0 sets up its peers while it is the only container of the lab.
	deadline := time.Now().Add(time.Minute)
	for {
		log, _ := os.ReadFile(nodeLog(dir, c.Nodes[0]))
		if strings.Count(string(log), "Starting PEX service") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("node0 did not start its p2p services again")
		}
		time.Sleep(200 * time.Millisecond)
	}
	forklab(t, "node", "start", "2", "--lab", dir)
	forklab(t, "node", "start", "1", "--lab", dir)

	after := containerIPs(t, name, c)
	for _, n := range c.Nodes {
		if after[n.NodeID] != before[n.NodeID] {
			t.Errorf("%s came back at %s, was at %s: its peers hold the old address", n.Name, after[n.NodeID], before[n.NodeID])
		}
		config, _ := os.ReadFile(filepath.Join(dir, n.Name, "config", "config.toml"))
		_, peers, _ := strings.Cut(string(config), "\npersistent_peers = \"")
		peers, _, _ = strings.Cut(peers, "\"")
		for _, peer := range strings.Split(peers, ",") {
			id, addr, _ := strings.Cut(peer, "@")
			if host, _, _ := strings.Cut(addr, ":"); host != after[id] {
				t.Errorf("%s dials peer %s at %q, and its container is at %s: the name is looked up once per start and can be gone or stale then", n.Name, id, host, after[id])
			}
		}
	}
	if st := waitMesh(t, name, 3, first.Height+5); t.Failed() {
		t.Logf("status = %+v", st.Nodes)
	}
	// A person in a container still reaches the others by name.
	peer := supervisor.ContainerName(name, "node1")
	if out := dockerCLI(t, "exec", supervisor.ContainerName(name, "node0"), "getent", "hosts", peer); !strings.HasPrefix(out, after[c.Nodes[1].NodeID]+" ") {
		t.Errorf("in node0, %s resolves to %q, want %s", peer, out, after[c.Nodes[1].NodeID])
	}
	for _, n := range c.Nodes {
		var book struct {
			Addrs []struct {
				Addr struct{ ID, IP string }
			}
		}
		data, _ := os.ReadFile(filepath.Join(dir, n.Name, "config", "addrbook.json"))
		_ = json.Unmarshal(data, &book)
		for _, a := range book.Addrs {
			if a.Addr.IP != after[a.Addr.ID] {
				t.Errorf("%s keeps peer %s at %s in its address book, and its container is at %s", n.Name, a.Addr.ID, a.Addr.IP, after[a.Addr.ID])
			}
		}
	}
}

// waitMesh waits until the lab's n nodes are up, each with every other as
// a peer, at height or above.
func waitMesh(t *testing.T, name string, n int, height int64) statusView {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		st := forklabData[statusView](t, "status", "--lab", name)
		ready := len(st.Nodes) == n
		for _, node := range st.Nodes {
			ready = ready && node.Up && node.Peers == n-1 && node.Height >= height
		}
		if ready {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want %d nodes up at height %d with %d peers each", st.Nodes, n, height, n-1)
		}
		time.Sleep(time.Second)
	}
}

// containerIPs maps each node's id to the address of its container.
func containerIPs(t *testing.T, name string, c lab.Config) map[string]string {
	t.Helper()
	ips := map[string]string{}
	for _, n := range c.Nodes {
		ips[n.NodeID] = strings.TrimSpace(dockerCLI(t, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", supervisor.ContainerName(name, n.Name)))
	}
	return ips
}

func dockerCLI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// labContainers names the lab's running containers. It asks for the lab's
// label, not for the name, so that nothing else is ever killed by a test.
func labContainers(t *testing.T, name string) []string {
	t.Helper()
	l, err := labs()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := l.Path(name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(dockerCLI(t, "ps", "--format", "{{.Names}}", "--filter", "label=forklab.lab="+dir))
}

func containerIDs(t *testing.T, lab string, n int) []string {
	t.Helper()
	var ids []string
	for i := range n {
		ids = append(ids, strings.TrimSpace(dockerCLI(t, "inspect", "--format", "{{.Id}}", supervisor.ContainerName(lab, "node"+strconv.Itoa(i)))))
	}
	return ids
}

// downClean takes the lab down and checks that the engine keeps no
// container of it, and keeps its network: the subnet stays the lab's
// until the lab is deleted.
func downClean(t *testing.T, lab string) {
	t.Helper()
	forklab(t, "lab", "down", lab)
	if left, nets := dockerCLI(t, "ps", "-a", "--format", "{{.Names}}", "--filter", "name=forklab-"+lab+"-"), dockerCLI(t, "network", "ls", "--format", "{{.Name}}", "--filter", "name=forklab-"+lab); left != "" || strings.TrimSpace(nets) != supervisor.NetworkName(lab) {
		t.Errorf("after lab down containers %q and networks %q, want none and the lab's", left, nets)
	}
}

func engineClean(t *testing.T, lab, after string) {
	t.Helper()
	if left, nets := dockerCLI(t, "ps", "-a", "--format", "{{.Names}}", "--filter", "name=forklab-"+lab+"-"), dockerCLI(t, "network", "ls", "--format", "{{.Name}}", "--filter", "name=forklab-"+lab); left != "" || nets != "" {
		t.Errorf("after %s containers %q and networks %q remain", after, left, nets)
	}
}
