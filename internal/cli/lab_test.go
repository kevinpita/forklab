package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/supervisor"
)

func TestLabVersionShowsWhatTheNodesRun(t *testing.T) {
	cfg := func(versions ...string) lab.Config {
		c := lab.Config{Version: "11.1.1"}
		for _, v := range versions {
			c.Nodes = append(c.Nodes, lab.Node{Version: v})
		}
		return c
	}
	for _, c := range []struct {
		cfg  lab.Config
		want string
	}{
		{cfg("11.1.1", "11.1.1"), "11.1.1"},
		{cfg("11.2.0", "11.2.0"), "11.2.0 (created 11.1.1)"},
		{cfg("11.2.0", "11.1.1"), "11.2.0, 11.1.1 (created 11.1.1)"},
	} {
		if got := versionText(c.cfg); got != c.want {
			t.Errorf("versionText(%+v) = %q, want %q", c.cfg.Nodes, got, c.want)
		}
	}
}

func TestLabInvalidNameIsAUsageError(t *testing.T) {
	t.Setenv("FORKLAB_HOME", t.TempDir())
	for _, args := range [][]string{
		{"lab", "show", "../x"},
		{"lab", "delete", "Bad"},
		{"lab", "create", "a/b", "--profile", "simd", "--version", "0.53.8"},
	} {
		if code, _, stderr := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, stderr)
		}
	}
}

func TestLabListKeepsBrokenLabsAndDeleteRemovesThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORKLAB_HOME", home)
	broken := filepath.Join(home, "labs", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "lab.yaml"), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run("lab", "list", "--json")
	if code != 0 {
		t.Fatalf("lab list: exit %d: %s", code, stderr)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 || env.Data[0]["name"] != "broken" || env.Data[0]["error"] == nil || env.Data[0]["dir"] != broken {
		t.Fatalf("lab list rows = %v, want the broken lab with name, dir, and error", env.Data)
	}
	if code, _, stderr := run("lab", "delete", "broken"); code != 0 {
		t.Fatalf("lab delete broken: exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Fatalf("broken lab still exists: %v", err)
	}
}

func TestLabCreateChecksItsFlagsBeforeItReachesForASnapshot(t *testing.T) {
	t.Setenv("FORKLAB_HOME", t.TempDir())
	for _, tt := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--cosmovisor-bin", filepath.Join(t.TempDir(), "absent")}, "--cosmovisor-bin"},
		{[]string{"--runtime", "podman"}, "--runtime must be host or docker"},
		{[]string{"--image", "img"}, "--image needs --runtime docker"},
	} {
		args := append([]string{"lab", "create", "demo", "--profile", "simd", "--version", "0.53.8", "--fork", "http://127.0.0.1:1/snapshot.tar.lz4"}, tt.flags...)
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, tt.want) {
			t.Errorf("%v: exit %d, want the usage error 2 with %q: %s", tt.flags, code, tt.want, stderr)
		}
	}
}

// A docker lab whose engine does not answer may still have its validators
// running: it must not be listed as stopped, and it must not be deleted.
func TestDockerLabIsNotTakenForStoppedWhenTheEngineDoesNotAnswer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORKLAB_HOME", home)
	dir := filepath.Join(home, "labs", "dk")
	node := filepath.Join(dir, "node0")
	if err := os.MkdirAll(node, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := supervisor.NodeSpec{
		Name: "node0", Binary: "/cache/chaind", Args: []string{"start", "--home", node}, Home: node,
		LogPath: filepath.Join(node, "node.log"), PidPath: filepath.Join(node, "node.pid"),
		Container: &supervisor.Container{Name: "forklab-dk-node0", Network: "forklab-dk", Subnet: "10.89.3.0/24", IP: "10.89.3.10"},
	}
	if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.PidPath, []byte(`{"pid":0,"binary":"/cache/chaind","container":"abc123"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if code, stdout, _ := run("lab", "list", "--json"); code != 0 || !strings.Contains(stdout, `"running":true`) {
		t.Errorf("lab list: code %d, %s; want the lab listed as running", code, stdout)
	}
	for _, args := range [][]string{{"lab", "delete", "dk"}, {"lab", "delete", "dk", "--force"}} {
		code, _, stderr := run(args...)
		if code == 0 || !strings.Contains(stderr, "Cannot connect to the Docker daemon") {
			t.Errorf("%v: code %d, %s; want it refused with the engine's answer", args, code, stderr)
		}
		if _, err := os.Stat(spec.PidPath); err != nil {
			t.Fatalf("%v removed the lab under containers it could not check: %v", args, err)
		}
	}
	// A supervisor that died before it wrote the pid file leaves a running
	// container that no file records.
	if err := os.Remove(spec.PidPath); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := run("lab", "list", "--json"); code != 0 || !strings.Contains(stdout, `"running":true`) {
		t.Errorf("lab list without a pid file: code %d, %s; want the lab listed as running", code, stdout)
	}
	if code, _, stderr := run("lab", "delete", "dk"); code == 0 || !strings.Contains(stderr, "Cannot connect to the Docker daemon") {
		t.Errorf("lab delete without a pid file: code %d, %s; want it refused with the engine's answer", code, stderr)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("lab delete removed a lab whose containers it could not check: %v", err)
	}

	// The engine answers again: no container is left, and the network
	// cannot be removed. The lab stays until the engine has let go of it.
	engine := func(script string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\ncase \"$*\" in\n"+script+"esac\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	engine(`"container inspect"*) echo 'Error: no such container' >&2; exit 1;;
"network inspect"*) echo '` + dir + `';;
"network rm"*) echo 'network forklab-dk has active endpoints' >&2; exit 1;;
`)
	if code, _, stderr := run("lab", "delete", "dk"); code == 0 || !strings.Contains(stderr, "has active endpoints") {
		t.Errorf("lab delete with a network that stays: code %d, %s; want it refused with the engine's answer", code, stderr)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("lab delete removed a lab whose network the engine kept: %v", err)
	}

	// Without nodes.json nothing names the lab's containers: the lab is
	// deleted only when the engine holds nothing with its label.
	if err := os.Remove(supervisor.Paths{Dir: dir}.Nodes()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte("name: dk\nmode: fresh\nchain_id: c-1\nversion: \"1\"\nvalidators: 0\ncreated_at: 2026-09-30T12:00:00Z\nruntime: docker\nprofile: {}\nnodes: []\naccounts: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine(`"ps -a"*) echo forklab-dk-node0;;
`)
	if code, _, stderr := run("lab", "delete", "dk"); code == 0 || !strings.Contains(stderr, "forklab-dk-node0") {
		t.Errorf("lab delete without nodes.json: code %d, %s; want it refused over the container the engine still has", code, stderr)
	}
	engine("")
	if code, _, stderr := run("lab", "delete", "dk"); code != 0 {
		t.Errorf("lab delete with nothing left in the engine: code %d, %s", code, stderr)
	}
}

// A start request has a deadline of seconds and an image build takes
// minutes, so a command that can start a container builds the image
// before it asks the supervisor for anything.
func TestCommandsThatStartContainersPrepareTheImageFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORKLAB_HOME", home)
	lab := func(name string, container *supervisor.Container) string {
		dir := filepath.Join(home, "labs", name)
		node := filepath.Join(dir, "node0")
		if err := os.MkdirAll(node, 0o755); err != nil {
			t.Fatal(err)
		}
		spec := supervisor.NodeSpec{
			Name: "node0", Binary: "/bin/sh", Args: []string{"start", "--home", node}, Home: node,
			LogPath: filepath.Join(node, "node.log"), PidPath: filepath.Join(node, "node.pid"), Container: container,
		}
		if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{spec}); err != nil {
			t.Fatal(err)
		}
		runtime := ""
		if container != nil {
			runtime = "runtime: docker\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte("name: "+name+"\nmode: fresh\nchain_id: c-1\nversion: \"1\"\nvalidators: 0\ncreated_at: 2026-09-30T12:00:00Z\n"+runtime+"profile: {}\nnodes: []\naccounts: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	docker := lab("dk", &supervisor.Container{Name: "forklab-dk-node0", Network: "forklab-dk", Subnet: "10.89.3.0/24", IP: "10.89.3.10"})
	named := lab("own", &supervisor.Container{Name: "forklab-own-node0", Image: "my/image:1", Network: "forklab-own", Subnet: "10.89.4.0/24", IP: "10.89.4.10"})
	host := lab("host", nil)

	// An engine without the node image, whose build fails.
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + calls + "\ncase \"$1\" in\nimage) echo 'No such image' >&2; exit 1;;\nbuild) echo 'apt-get failed' >&2; exit 1;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	// A supervisor that a regression lets start is this test binary. It
	// must then act as one, not run the tests again, and must not stay.
	t.Setenv("FORKLAB_TEST_CHILD", "1")
	t.Cleanup(func() {
		lock, _ := os.ReadFile(supervisor.Paths{Dir: docker}.Lock())
		if pid, err := strconv.Atoi(strings.TrimSpace(string(lock))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	for _, args := range [][]string{
		{"lab", "up", "dk"},
		{"node", "start", "0", "--lab", docker},
		{"node", "restart", "0", "--lab", docker},
	} {
		_ = os.Remove(calls)
		code, _, stderr := run(args...)
		if code == 0 || !strings.Contains(stderr, "apt-get failed") || !strings.Contains(stderr, "building the node image") {
			t.Errorf("%v: code %d, %s; want the build announced and its failure reported", args, code, stderr)
		}
		if recorded, _ := os.ReadFile(calls); !strings.Contains(string(recorded), "build --load -t forklab-node:") {
			t.Errorf("%v: docker was called with %q, want the image built", args, recorded)
		}
		if _, err := os.Stat(supervisor.Paths{Dir: docker}.Log()); !os.IsNotExist(err) {
			t.Fatalf("%v: a supervisor was started before the image was there (%v)", args, err)
		}
	}

	// A lab with an image of its own, and a host lab, need no image built.
	_ = os.Remove(calls)
	for _, dir := range []string{named, host} {
		if err := prepareImage(t.Context(), dir, io.Discard); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if recorded, err := os.ReadFile(calls); !os.IsNotExist(err) {
		t.Errorf("docker was called for a lab that does not run the forklab image: %q", recorded)
	}
}
