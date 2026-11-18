package supervisor

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeDocker replaces the docker CLI for the test: every call is recorded
// as its joined arguments and answered by reply. Calls may come from
// several goroutines; the test reads the record once they are done.
func fakeDocker(t *testing.T, reply func(args []string, stdin string) (string, error)) *[]string {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
	)
	real := docker
	t.Cleanup(func() { docker = real })
	docker = func(_ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, strings.Join(args, " "))
		in := ""
		if stdin != nil {
			data, _ := io.ReadAll(stdin)
			in = string(data)
		}
		out, err := reply(args, in)
		return []byte(out), err
	}
	return &calls
}

// runnable is containerSpec with a binary that exists, which start checks.
func runnable(lab string) NodeSpec {
	spec := containerSpec(lab)
	spec.Binary = "/bin/sh"
	return spec
}

func containerSpec(lab string) NodeSpec {
	home := filepath.Join(lab, "node1")
	return NodeSpec{
		Index: 1, Name: "node1", Binary: "/cache/chaind", Args: []string{"start", "--home", home}, Home: home,
		LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid"),
		Container: &Container{Name: "forklab-lab-node1", Image: "img", Network: "forklab-lab", Subnet: "10.89.3.0/24", IP: "10.89.3.11", Publish: []string{"127.0.0.1:26757:26657"}},
	}
}

func TestRunArgsMountByHostPathAndMapTheUserOnlyOnARootfulEngine(t *testing.T) {
	uid := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	store := t.TempDir()
	tests := []struct {
		name       string
		rootless   bool
		cosmovisor bool
		nixStore   string
		want, not  []string
		tail       string
	}{
		{
			name: "rootless engine runs as root, whatever user the image names", rootless: true, nixStore: "/nonexistent",
			want: []string{"--name forklab-lab-node1", "--label forklab.lab=/labs/lab", "--user 0:0", "--hostname node1", "--network forklab-lab", "--ip 10.89.3.11", "-p 127.0.0.1:26757:26657", "-v /labs/lab:/labs/lab", "-v /cache/chaind:/cache/chaind:ro", "-e FORKLAB_KEYS=/labs/lab/keys", "-e FORKLAB_BIN=/cache/chaind", "-e PATH=/cache:/usr/local/sbin"},
			not:  []string{"-e HOME=/tmp", "/nonexistent"},
			tail: "/cache/chaind start --home /labs/lab/node1",
		},
		{
			name: "rootful engine runs as the host user", nixStore: "/nonexistent",
			want: []string{"--user " + uid, "-e HOME=/tmp"},
			tail: "/cache/chaind start --home /labs/lab/node1",
		},
		{
			name: "a host with a nix store shares it", rootless: true, nixStore: store,
			want: []string{"-v " + store + ":" + store + ":ro"},
			tail: "/cache/chaind start --home /labs/lab/node1",
		},
		{
			name: "cosmovisor runs in the container", rootless: true, cosmovisor: true, nixStore: "/nonexistent",
			want: []string{"-v /cache/cosmovisor:/cache/cosmovisor:ro", "-e DAEMON_NAME=chaind", "-e DAEMON_HOME=/labs/lab/node1", "-e FORKLAB_BIN=/labs/lab/node1/cosmovisor/current/bin/chaind", "-e PATH=/labs/lab/node1/cosmovisor/current/bin:"},
			tail: "/cache/cosmovisor run start --home /labs/lab/node1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := nixStore
			nixStore = tt.nixStore
			defer func() { nixStore = previous }()
			spec := containerSpec("/labs/lab")
			if tt.cosmovisor {
				spec.Cosmovisor = &Cosmovisor{Path: "/cache/cosmovisor", Name: "chaind"}
			}
			args := runArgs(spec, tt.rootless, "img")
			line := strings.Join(args, " ")
			for _, w := range tt.want {
				if !strings.Contains(line, w) {
					t.Errorf("run args lack %q:\n%s", w, line)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(line, n) {
					t.Errorf("run args have %q:\n%s", n, line)
				}
			}
			if i := slices.Index(args, containerScript); i < 3 || strings.Join(args[i-2:i], " ") != "img -c" || strings.Join(args[i+1:], " ") != tt.tail {
				t.Errorf("run args do not end with the image, the script, and %q:\n%s", tt.tail, line)
			}
		})
	}
}

func TestDockerRuntimeStartsWatchesAndRemovesAContainer(t *testing.T) {
	spec := runnable(t.TempDir())
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	exit := "137\n"
	calls := fakeDocker(t, func(args []string, _ string) (string, error) {
		switch {
		case args[0] == "info":
			return `["name=seccomp","name=rootless"]`, nil
		case args[0] == "run":
			return "abc123\n", nil
		case args[0] == "wait":
			return exit, nil
		case isLabelInspect(args):
			return "", missing(args)
		case args[0] == "container":
			return "true\n", nil
		}
		return "", nil
	})
	pf, wait, err := dockerRuntime{}.start(spec)
	if err != nil || pf.Container != "abc123" || pf.PID != 0 || pf.Binary != spec.Binary {
		t.Fatalf("start = %+v, %v; want the container id as the identity", pf, err)
	}
	if _, err := os.Stat(spec.LogPath); err != nil {
		t.Errorf("start left no log file for the tail to open: %v", err)
	}
	lab := filepath.Dir(spec.Home)
	want := []string{
		"info --format {{json .SecurityOptions}}",
		`network inspect --format {{index .Labels "forklab.lab"}} forklab-lab`,
		"network create --label forklab.lab=" + lab + " --subnet 10.89.3.0/24 forklab-lab",
		`container inspect --format {{index .Config.Labels "forklab.lab"}} forklab-lab-node1`,
	}
	if got := (*calls)[:len(want)]; !slices.Equal(got, want) || !strings.HasPrefix((*calls)[len(want)], "run -d --name forklab-lab-node1 --label forklab.lab="+lab+" ") {
		t.Errorf("start called %q, want %q then run", *calls, want)
	}
	if up, err := (dockerRuntime{}).alive(spec, pf); !up || err != nil {
		t.Errorf("alive = %t, %v for a running container", up, err)
	}
	if up, err := (dockerRuntime{}).alive(spec, pidFile{PID: 1}); up || err != nil {
		t.Errorf("alive = %t, %v for a pid file without a container", up, err)
	}
	if e, ok := wait(nil); !ok || e.signal != "killed" || e.code != nil {
		t.Errorf("exit 137 = %+v, %t; want it read as SIGKILL", e, ok)
	}
	exit = "3\n"
	if !(dockerRuntime{}).watch(spec, pf, make(chan struct{})) {
		t.Error("watch did not report the exit")
	}
	if got := (*calls)[len(*calls)-2:]; !slices.Equal(got, []string{"wait abc123", "rm -f abc123"}) {
		t.Errorf("after the exit docker was called with %q, want the container removed", got)
	}
}

func TestDockerErrorsNameWhatIsWrong(t *testing.T) {
	down := errors.New("Cannot connect to the Docker daemon")
	fakeDocker(t, func(args []string, _ string) (string, error) { return "", down })
	if err := (Container{}).Preflight(t.Context(), "/cache/chaind"); err == nil || !strings.Contains(err.Error(), "the docker engine does not answer") {
		t.Errorf("unreachable engine: %v", err)
	}
	if _, _, err := (dockerRuntime{}).start(runnable(t.TempDir())); err == nil || !strings.Contains(err.Error(), "the docker engine does not answer") {
		t.Errorf("start on an unreachable engine: %v", err)
	}

	var built string
	calls := fakeDocker(t, func(args []string, stdin string) (string, error) {
		switch args[0] {
		case "image":
			return "", errors.New("No such image")
		case "build":
			built = stdin
			return "", errors.New("apt-get failed")
		case "run":
			return "", errors.New("error while loading shared libraries: libwasmvm.x86_64.so")
		}
		return "[]", nil
	})
	if err := (Container{}).Preflight(t.Context(), "/cache/chaind"); err == nil || !strings.Contains(err.Error(), "build the node image "+nodeImage) || built != nodeDockerfile || !slices.Contains(*calls, "build --load -t "+nodeImage+" -") {
		t.Errorf("failed build: %v after %q; want the Dockerfile built with --load and the failure named", err, *calls)
	}
	err := Container{Image: "my/image:1"}.Preflight(t.Context(), "/cache/chaind")
	if err == nil || !strings.Contains(err.Error(), "/cache/chaind does not run in the image my/image:1") || !strings.Contains(err.Error(), "libwasmvm") || !strings.Contains(err.Error(), "--image") {
		t.Errorf("preflight: %v; want the binary, the image, the loader's message, and the way out", err)
	}
}

func TestHostNodesNeverCallDocker(t *testing.T) {
	dir := t.TempDir()
	host := containerSpec(dir)
	host.Index, host.Container = 0, nil
	if err := os.MkdirAll(host.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveNodes(dir, []NodeSpec{host}); err != nil {
		t.Fatal(err)
	}
	if err := writePidFile(host.PidPath, pidFile{PID: 1 << 30, Binary: host.Binary}); err != nil {
		t.Fatal(err)
	}
	calls := fakeDocker(t, func(args []string, _ string) (string, error) {
		if isLabelInspect(args) {
			return dir + "\n", nil
		}
		return "", nil
	})
	if live, err := LiveNodes(dir); err != nil || len(live) != 0 {
		t.Errorf("LiveNodes = %v, %v", live, err)
	}
	if err := Teardown(dir); err != nil || len(*calls) != 0 {
		t.Errorf("a host lab called docker: %q, %v", *calls, err)
	}

	docker := containerSpec(dir)
	docker.Index = 0
	if err := SaveNodes(dir, []NodeSpec{docker}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`container inspect --format {{index .Config.Labels "forklab.lab"}} forklab-lab-node1`, "rm -f forklab-lab-node1",
		`network inspect --format {{index .Labels "forklab.lab"}} forklab-lab`, "network rm forklab-lab",
	}
	if err := Teardown(dir); err != nil || !slices.Equal(*calls, want) {
		t.Errorf("teardown of a docker lab called %q, %v; want %q", *calls, err, want)
	}
}

// errEngineDown is what the docker CLI prints when the engine does not
// answer. errContainerMissing and errPodmanMissing are how Docker and
// Podman answer that the container is gone, errNetworkMissing how Podman
// answers that the network is.
var (
	errEngineDown       = errors.New("docker container: exit status 1: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")
	errContainerMissing = errors.New("docker container: exit status 1: Error response from daemon: No such container: abc123")
	errPodmanMissing    = errors.New(`docker container: exit status 1: Error response from daemon: no container with name or ID "abc123" found: no such container`)
	errNetworkMissing   = errors.New("docker network: exit status 1: Error response from daemon: unable to find network with name or ID forklab-lab: network not found")
)

// missing is the engine's answer about a container or network it lacks.
func missing(args []string) error {
	if args[0] == "network" {
		return errNetworkMissing
	}
	return errPodmanMissing
}

func fastDockerRetry(t *testing.T) {
	t.Helper()
	previous := dockerRetry
	dockerRetry = time.Millisecond
	t.Cleanup(func() { dockerRetry = previous })
}

func isInspect(args []string) bool {
	return args[0] == "inspect" || (args[0] == "container" && args[1] == "inspect")
}

func TestContainerIsNotGivenUpWhileTheEngineDoesNotAnswer(t *testing.T) {
	fastDockerRetry(t)
	tests := []struct {
		name string
		// then is what the engine says about the container once it answers
		// again, after two failed inspects: nil for "it runs".
		then     error
		wantExit bool
	}{
		{"the engine recovers and the container still runs", nil, false},
		{"the engine recovers and the container is gone", errContainerMissing, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inspects := 0
			stop := make(chan struct{})
			calls := fakeDocker(t, func(args []string, _ string) (string, error) {
				switch {
				case args[0] == "wait":
					return "", errEngineDown
				case isInspect(args):
					inspects++
					if inspects <= 2 {
						return "", errEngineDown
					}
					if tt.then != nil {
						return "", tt.then
					}
					if inspects == 4 {
						close(stop)
					}
					return "true\n", nil
				}
				return "", nil
			})
			exited := dockerRuntime{}.watch(containerSpec(t.TempDir()), pidFile{Container: "abc123"}, stop)
			removals := 0
			for _, c := range *calls {
				if strings.HasPrefix(c, "rm ") {
					removals++
				}
			}
			if exited != tt.wantExit || inspects < 3 {
				t.Errorf("watch reported exit %t after %d inspects; want %t, and only once the engine answered", exited, inspects, tt.wantExit)
			}
			if want := map[bool]int{false: 0, true: 1}[tt.wantExit]; removals != want {
				t.Errorf("the container was removed %d times, want %d: %q", removals, want, *calls)
			}
		})
	}
}

func TestSignalFailsWhenTheEngineCannotConfirmIt(t *testing.T) {
	pf := pidFile{Container: "abc123"}
	fakeDocker(t, func([]string, string) (string, error) { return "", errEngineDown })
	if err := (dockerRuntime{}).signal(NodeSpec{}, pf, syscall.SIGTERM, false); err == nil {
		t.Error("a kill the engine never answered was reported as delivered")
	}
	fakeDocker(t, func([]string, string) (string, error) { return "", errContainerMissing })
	if err := (dockerRuntime{}).signal(NodeSpec{}, pf, syscall.SIGKILL, false); err != nil {
		t.Errorf("signalling a container that is gone: %v", err)
	}
}

func TestAdoptKeepsANodeTheEngineCannotConfirm(t *testing.T) {
	fastDockerRetry(t)
	spec := containerSpec(t.TempDir())
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePidFile(spec.PidPath, pidFile{Container: "abc123", Binary: spec.Binary, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fakeDocker(t, func([]string, string) (string, error) { return "", errEngineDown })
	stop := make(chan struct{})
	var wg sync.WaitGroup
	n := &node{log: log.New(io.Discard, "", 0), stop: stop, wg: &wg, rt: dockerRuntime{}, spec: spec}
	if err := n.adopt(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	st := n.status()
	// Stopping it must not wait for an exit that nobody can observe: the
	// engine did not take the signal, and says nothing about the container.
	stopped := make(chan error, 1)
	go func() { stopped <- n.signal(syscall.SIGTERM, 50*time.Millisecond) }()
	select {
	case err := <-stopped:
		if err == nil {
			t.Error("a node the engine cannot confirm was reported as stopped")
		}
	case <-time.After(3 * time.Second):
		t.Error("stopping a node the engine cannot confirm waits for its exit")
	}
	close(stop)
	wg.Wait()
	if st.State != StateRunning || !st.Adopted {
		t.Errorf("node = %+v, want it kept as running and adopted until the engine says otherwise", st)
	}
	if _, err := os.Stat(spec.PidPath); err != nil {
		t.Errorf("the pid file, the only record of the container, is gone: %v", err)
	}
}

func TestAdoptRemovesAContainerThatNoPidFileRecords(t *testing.T) {
	spec := containerSpec(t.TempDir())
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := fakeDocker(t, func(args []string, _ string) (string, error) {
		if isLabelInspect(args) {
			return filepath.Dir(spec.Home) + "\n", nil
		}
		return "", nil
	})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	n := &node{log: log.New(io.Discard, "", 0), stop: stop, wg: &wg, rt: dockerRuntime{}, spec: spec}
	if err := n.adopt(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	if got := removals(*calls); len(got) != 1 || got[0] != "rm -f forklab-lab-node1" {
		t.Errorf("adopt called %q, want the lab's unrecorded container removed", *calls)
	}
}

func TestLiveNodesFailsWhenTheEngineCannotConfirm(t *testing.T) {
	dir := t.TempDir()
	spec := containerSpec(dir)
	spec.Index = 0
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveNodes(dir, []NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if err := writePidFile(spec.PidPath, pidFile{Container: "abc123", Binary: spec.Binary}); err != nil {
		t.Fatal(err)
	}
	fakeDocker(t, func([]string, string) (string, error) { return "", errEngineDown })
	if live, err := LiveNodes(dir); !errors.Is(err, ErrLiveness) || !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("LiveNodes = %v, %v; want an error, not a lab that reads as stopped", live, err)
	}
	fakeDocker(t, func([]string, string) (string, error) { return "", errPodmanMissing })
	if live, err := LiveNodes(dir); err != nil || len(live) != 0 {
		t.Errorf("LiveNodes with the container gone = %v, %v", live, err)
	}
	fakeDocker(t, func([]string, string) (string, error) { return "true\n", nil })
	if live, err := LiveNodes(dir); err != nil || len(live) != 1 {
		t.Errorf("LiveNodes with the container running = %v, %v", live, err)
	}
}

func isLabelInspect(args []string) bool {
	return len(args) > 3 && args[1] == "inspect" && strings.Contains(args[3], "forklab.lab")
}

func removals(calls []string) []string {
	return slices.DeleteFunc(slices.Clone(calls), func(c string) bool {
		return !strings.HasPrefix(c, "rm ") && !strings.HasPrefix(c, "network rm ") && !strings.HasPrefix(c, "run ")
	})
}

// Two labs of the same name under two forklab homes share the names of
// their containers and network. Only the label tells whose they are.
func TestWhatAnotherLabCreatedIsNeverRemovedOrReplaced(t *testing.T) {
	for _, tt := range []struct{ name, label string }{
		{"created for a lab in another directory", "/another/home/labs/lab"},
		{"not created by forklab", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := runnable(dir)
			spec.Index = 0
			if err := os.MkdirAll(spec.Home, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := SaveNodes(dir, []NodeSpec{spec}); err != nil {
				t.Fatal(err)
			}
			calls := fakeDocker(t, func(args []string, _ string) (string, error) {
				switch {
				case args[0] == "info":
					return `["name=rootless"]`, nil
				case isLabelInspect(args):
					return tt.label + "\n", nil
				}
				return "true\n", nil
			})
			if err := Teardown(dir); err == nil || !strings.Contains(err.Error(), "forklab-lab-node1") || !strings.Contains(err.Error(), "forklab-lab ") {
				t.Errorf("teardown = %v, want it to name the container and the network it left alone", err)
			}
			if _, _, err := (dockerRuntime{}).start(spec); err == nil || !strings.Contains(err.Error(), "forklab-lab") {
				t.Errorf("start = %v, want it refused over what is not this lab's", err)
			}
			if got := removals(*calls); len(got) != 0 {
				t.Errorf("docker removed or replaced what this lab did not create: %q", got)
			}
		})
	}
}

// A supervisor that dies between docker run and the pid file leaves a
// running container that no file records.
func TestDockerNodeWithoutAPidFileIsStillAskedAbout(t *testing.T) {
	dir := t.TempDir()
	spec := containerSpec(dir)
	spec.Index = 0
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveNodes(dir, []NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		reply    func(args []string) (string, error)
		wantLive int
		wantErr  bool
	}{
		{"the engine does not answer", func([]string) (string, error) { return "", errEngineDown }, 0, true},
		{"no container of that name", func([]string) (string, error) { return "", errPodmanMissing }, 0, false},
		{"this lab's container runs", func(args []string) (string, error) {
			if isLabelInspect(args) {
				return dir + "\n", nil
			}
			return "true\n", nil
		}, 1, false},
		{"another lab's container runs", func(args []string) (string, error) {
			if isLabelInspect(args) {
				return "/another/home/labs/lab\n", nil
			}
			return "true\n", nil
		}, 0, false},
	} {
		fakeDocker(t, func(args []string, _ string) (string, error) { return tt.reply(args) })
		live, err := LiveNodes(dir)
		if len(live) != tt.wantLive || (err != nil) != tt.wantErr || (tt.wantErr && !errors.Is(err, ErrLiveness)) {
			t.Errorf("%s: LiveNodes = %v, %v; want %d live, error %t", tt.name, live, err, tt.wantLive, tt.wantErr)
		}
	}
	fakeDocker(t, func([]string, string) (string, error) { return "", errEngineDown })
	if err := Teardown(dir); err == nil {
		t.Error("teardown reported success with an engine that did not answer")
	}
}

func TestPreflightRunsAsRootOnARootlessEngine(t *testing.T) {
	calls := fakeDocker(t, func(args []string, _ string) (string, error) {
		if args[0] == "info" {
			return `["name=rootless"]`, nil
		}
		return "", nil
	})
	if err := (Container{Image: "img"}).Preflight(t.Context(), "/cache/chaind"); err != nil {
		t.Fatal(err)
	}
	if run := (*calls)[len(*calls)-1]; !strings.Contains(run, "--user 0:0") {
		t.Errorf("preflight leaves the user to the image: %s", run)
	}
	if err := (Container{Image: "img"}).Preflight(t.Context(), "/cache/cosmovisor", "help"); err != nil {
		t.Fatal(err)
	}
	if run := (*calls)[len(*calls)-1]; !strings.Contains(run, "--entrypoint /cache/cosmovisor ") || !strings.HasSuffix(run, " img help") {
		t.Errorf("preflight of another binary ran %s, want it run with the arguments given", run)
	}
}

// hangingDocker replaces the docker CLI with an engine that accepts the
// call and never answers. It reports each call that ends.
func hangingDocker(t *testing.T, hang func(args []string) bool) <-chan string {
	t.Helper()
	ended := make(chan string, 16)
	real := docker
	t.Cleanup(func() { docker = real })
	docker = func(ctx context.Context, _ io.Reader, args ...string) ([]byte, error) {
		if !hang(args) {
			switch {
			case args[0] == "info":
				return []byte(`["name=rootless"]`), nil
			case args[0] == "run":
				return []byte("abc123\n"), nil
			case len(args) > 1 && args[1] == "inspect" && args[0] != "image":
				return nil, missing(args)
			}
			return nil, nil
		}
		<-ctx.Done()
		ended <- strings.Join(args, " ")
		return nil, ctx.Err()
	}
	return ended
}

func TestAnEngineThatHangsIsNotWaitedForForever(t *testing.T) {
	previous := dockerTimeout
	dockerTimeout = 20 * time.Millisecond
	t.Cleanup(func() { dockerTimeout = previous })
	hangingDocker(t, func([]string) bool { return true })
	spec, pf := runnable(t.TempDir()), pidFile{Container: "abc123"}
	for name, call := range map[string]func() error{
		"alive":    func() error { _, err := (dockerRuntime{}).alive(spec, pf); return err },
		"signal":   func() error { return (dockerRuntime{}).signal(spec, pf, syscall.SIGKILL, false) },
		"teardown": func() error { return removeContainers([]NodeSpec{spec}) },
		"start":    func() error { _, _, err := (dockerRuntime{}).start(spec); return err },
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: an engine that never answered was taken for an answer", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s still waits for the engine", name)
		}
	}
}

// A node keeps the address it first got for a peer, so a node's address
// must not change with its container: the lab's network has a subnet that
// the engine chose once, and every node a fixed address in it.
func TestNodesHaveFixedAddressesInASubnetTheEngineChoseOnce(t *testing.T) {
	for _, tt := range []struct {
		subnet string
		index  int
		want   string
	}{
		{"10.89.3.0/24", 0, "10.89.3.10"},
		{"10.89.3.0/24", 3, "10.89.3.13"},
		{"172.18.0.0/16", 250, "172.18.1.4"},
		{"10.89.3.0/28", 4, "10.89.3.14"},
		{"10.89.3.0/28", 5, ""},
		{"not a subnet", 0, ""},
	} {
		if got, err := NodeIP(tt.subnet, tt.index); got != tt.want || (err == nil) != (tt.want != "") {
			t.Errorf("NodeIP(%s, %d) = %q, %v; want %q", tt.subnet, tt.index, got, err, tt.want)
		}
	}

	create := "network create --label forklab.lab=/labs/lab"
	label := `network inspect --format {{index .Labels "forklab.lab"}} forklab-lab`
	subnet := "network inspect --format {{range .IPAM.Config}}{{.Subnet}} {{end}} forklab-lab"
	for _, tt := range []struct {
		name string
		// exists is whether the lab has its network; taken how often
		// another network takes the chosen subnet before forklab names it.
		exists bool
		taken  int
		want   []string
	}{
		{"a new lab", false, 0, []string{label, create + " forklab-lab", subnet, "network rm forklab-lab", create + " --subnet 10.89.3.0/24 forklab-lab"}},
		{"the subnet is taken in between", false, 1, []string{
			label, create + " forklab-lab", subnet, "network rm forklab-lab", create + " --subnet 10.89.3.0/24 forklab-lab",
			create + " forklab-lab", subnet, "network rm forklab-lab", create + " --subnet 10.89.3.0/24 forklab-lab",
		}},
		{"the lab has its network", true, 0, []string{label, subnet}},
	} {
		taken := tt.taken
		calls := fakeDocker(t, func(args []string, _ string) (string, error) {
			switch {
			case isLabelInspect(args) && !tt.exists:
				return "", errNetworkMissing
			case isLabelInspect(args):
				return "/labs/lab\n", nil
			case args[1] == "inspect":
				return "fd00:3::/64 10.89.3.0/24 \n", nil
			case slices.Contains(args, "--subnet") && taken > 0:
				taken--
				return "", errors.New("subnet 10.89.3.0/24 is already used on the host or by another config")
			}
			return "", nil
		})
		if got, err := LabNetwork(t.Context(), "forklab-lab", "/labs/lab"); got != "10.89.3.0/24" || err != nil || !slices.Equal(*calls, tt.want) {
			t.Errorf("%s: LabNetwork = %q, %v after %q; want 10.89.3.0/24 after %q", tt.name, got, err, *calls, tt.want)
		}
	}

	// A network whose subnet cannot be read is not left behind: the caller
	// was never told that it exists.
	calls := fakeDocker(t, func(args []string, _ string) (string, error) {
		switch {
		case isLabelInspect(args):
			return "", errNetworkMissing
		case args[1] == "inspect":
			return "", errEngineDown
		}
		return "", nil
	})
	if _, err := LabNetwork(t.Context(), "forklab-lab", "/labs/lab"); err == nil || (*calls)[len(*calls)-1] != "network rm forklab-lab" {
		t.Errorf("LabNetwork = %v after %q; want it to fail and remove the network it created", err, *calls)
	}

	// Taking the lab down keeps the network, and with it the subnet.
	calls = fakeDocker(t, func(args []string, _ string) (string, error) {
		if isLabelInspect(args) {
			return "/labs/lab\n", nil
		}
		return "", nil
	})
	if err := removeContainers([]NodeSpec{containerSpec("/labs/lab")}); err != nil || len(removals(*calls)) != 1 || removals(*calls)[0] != "rm -f forklab-lab-node1" {
		t.Errorf("down called %q, %v; want the container removed and the network kept", *calls, err)
	}
}

func TestSupervisorThatExitsEndsTheWaitOfTheNodesItStarted(t *testing.T) {
	spec := runnable(t.TempDir())
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	ended := hangingDocker(t, func(args []string) bool { return args[0] == "wait" })
	stop := make(chan struct{})
	var wg sync.WaitGroup
	n := &node{log: log.New(io.Discard, "", 0), stop: stop, wg: &wg, rt: dockerRuntime{}, spec: spec}
	if err := n.start(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	select {
	case call := <-ended:
		if call != "wait abc123" {
			t.Errorf("ended %q, want the wait for the container", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("docker wait outlives the supervisor that started the node")
	}
	wg.Wait()
	if st := n.status(); st.State != StateRunning {
		t.Errorf("node = %+v, want it left running", st)
	}
}

// Podman's own docker command names two fields differently from the
// Docker CLI: where the engine says whether it is rootless, and a
// network's subnets.
func TestPodmansOwnCLIIsAskedWithItsFieldNames(t *testing.T) {
	fakeDocker(t, func(args []string, _ string) (string, error) {
		switch format := args[slices.Index(args, "--format")+1]; {
		case strings.Contains(format, ".SecurityOptions"):
			return "", errors.New(`docker info: exit status 125: Error: template: info:1:7: executing "info" at <.SecurityOptions>: can't evaluate field SecurityOptions in type system.infoReport`)
		case strings.Contains(format, ".Host.Security.Rootless"):
			return "true\n", nil
		case strings.Contains(format, ".IPAM.Config"):
			return "", errors.New(`docker network: exit status 125: Error: template: inspect:1:24: executing "inspect" at <.IPAM.Config>: can't evaluate field IPAM in type interface {}`)
		case strings.Contains(format, ".Subnets"):
			return "10.89.3.0/24 \n", nil
		}
		return "", errEngineDown
	})
	if rootless, err := dockerRootless(t.Context()); !rootless || err != nil {
		t.Errorf("dockerRootless = %t, %v; want true from Podman's own field", rootless, err)
	}
	if subnet, err := networkSubnet(t.Context(), "forklab-lab"); subnet != "10.89.3.0/24" || err != nil {
		t.Errorf("networkSubnet = %q, %v; want the subnet from Podman's own field", subnet, err)
	}
}

// Only the engine's own words for "there is none" mean that a container or
// network is absent. Any other failure leaves that open, and nothing may be
// reported removed on the strength of it.
func TestOnlyTheEnginesWordsMeanAbsent(t *testing.T) {
	for _, tt := range []struct {
		kind, message string
		absent        bool
	}{
		{"container", "docker container: exit status 1: Error response from daemon: No such container: x", true},
		{"container", `docker container: exit status 1: Error: no such container "x"`, true},
		{"network", "docker network: exit status 1: Error response from daemon: network x not found", true},
		{"network", "docker network: exit status 1: Error response from daemon: unable to find network with name or ID x: network not found", true},
		{"network", "docker network: exit status 1: Error: No such network: x", true},
		{"network", `docker network: exit status 1: load context "broken": context not found: open /home/u/.docker/contexts/meta/x/meta.json: no such file or directory`, false},
		{"network", "docker network: exit status 1: Cannot connect to the Docker daemon at unix:///var/run/docker.sock", false},
		{"container", "docker container: exit status 1: error during connect: host not found", false},
	} {
		fakeDocker(t, func([]string, string) (string, error) { return "", errors.New(tt.message) })
		_, found, err := owner(t.Context(), tt.kind, "x")
		if found || (err == nil) != tt.absent {
			t.Errorf("%s, %q: found %t, error %v; want absent %t", tt.kind, tt.message, found, err, tt.absent)
		}
	}
}

func TestStartMountsWhatExistsAndLeavesFileLabelsAlone(t *testing.T) {
	dir := t.TempDir()
	spec := containerSpec(dir)
	spec.Binary = filepath.Join(dir, "chaind")
	spec.Container.Image = ""
	if err := os.MkdirAll(spec.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := fakeDocker(t, func(args []string, _ string) (string, error) {
		switch {
		case args[0] == "info":
			return `["name=rootless"]`, nil
		case isLabelInspect(args) && args[0] == "network":
			return dir + "\n", nil
		case isLabelInspect(args):
			return "", errPodmanMissing
		case args[0] == "run":
			return "abc123\n", nil
		}
		return "", nil
	})

	// A binary that is not there would be mounted as an empty directory.
	if _, _, err := (dockerRuntime{}).start(spec); err == nil || !strings.Contains(err.Error(), spec.Binary) || len(removals(*calls)) != 0 {
		t.Errorf("start without the binary = %v after %q; want it refused before docker run, naming the file", err, *calls)
	}
	if err := os.WriteFile(spec.Binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (dockerRuntime{}).start(spec); err != nil {
		t.Fatal(err)
	}
	run := (*calls)[len(*calls)-1]
	// An SELinux host denies a container its bind mounts unless they are
	// relabelled, which must not be done to a lab or to /nix/store.
	if !strings.Contains(run, " --security-opt label=disable ") {
		t.Errorf("run does not turn SELinux label separation off: %s", run)
	}
	// A lab without an image of its own runs the one forklab builds now.
	if !strings.Contains(run, " "+nodeImage+" -c ") {
		t.Errorf("run does not use the forklab image %s for a lab that names none: %s", nodeImage, run)
	}
	if err := (Container{Image: nodeImage}).Preflight(t.Context(), spec.Binary); err != nil {
		t.Fatal(err)
	}
	if preflight := (*calls)[len(*calls)-1]; !strings.Contains(preflight, " --security-opt label=disable ") {
		t.Errorf("preflight does not turn SELinux label separation off: %s", preflight)
	}
}

// The node image is built by whatever docker command the user has. With
// buildx and a container builder the result reaches the engine only with
// --load; the Docker CLI without buildx does not know that flag.
func TestNodeImageIsBuiltByEveryKindOfDockerCommand(t *testing.T) {
	unknownFlag := errors.New("docker build: exit status 125: DEPRECATED: The legacy builder is deprecated and will be removed in a future release.\n\nunknown flag: --load")
	for _, tt := range []struct {
		name string
		// build answers a docker build; ok says whether it put the image
		// into the engine.
		build      func(args []string) (ok bool, err error)
		wantBuilds []string
		wantErr    string
	}{
		{
			name:       "Docker CLI with buildx, or Podman's own",
			build:      func([]string) (bool, error) { return true, nil },
			wantBuilds: []string{"build --load -t " + nodeImage + " -"},
		},
		{
			name: "Docker CLI without buildx",
			build: func(args []string) (bool, error) {
				if slices.Contains(args, "--load") {
					return false, unknownFlag
				}
				return true, nil
			},
			wantBuilds: []string{"build --load -t " + nodeImage + " -", "build -t " + nodeImage + " -"},
		},
		{
			name:       "the build fails",
			build:      func([]string) (bool, error) { return false, errors.New("apt-get failed") },
			wantBuilds: []string{"build --load -t " + nodeImage + " -"},
			wantErr:    "apt-get failed",
		},
		{
			name:       "the build succeeds and the engine does not get the image",
			build:      func([]string) (bool, error) { return false, nil },
			wantBuilds: []string{"build --load -t " + nodeImage + " -"},
			wantErr:    "the engine does not have it",
		},
	} {
		built := false
		calls := fakeDocker(t, func(args []string, stdin string) (string, error) {
			switch args[0] {
			case "image":
				if !built {
					return "", errors.New("docker image: exit status 1: Error response from daemon: No such image: " + nodeImage)
				}
				return "sha256:abc\n", nil
			case "build":
				if stdin != nodeDockerfile {
					t.Errorf("%s: the build got %q on stdin, want the Dockerfile", tt.name, stdin)
				}
				ok, err := tt.build(args)
				built = built || ok
				return "", err
			}
			return "[]", nil
		})
		err := (Container{}).Preflight(t.Context(), "/cache/chaind")
		builds := slices.DeleteFunc(slices.Clone(*calls), func(c string) bool { return !strings.HasPrefix(c, "build ") })
		if !slices.Equal(builds, tt.wantBuilds) || (err == nil) != (tt.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
			t.Errorf("%s: %v after builds %q; want builds %q and error %q", tt.name, err, builds, tt.wantBuilds, tt.wantErr)
		}
	}
}
