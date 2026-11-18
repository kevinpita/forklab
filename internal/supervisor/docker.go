package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Container makes a node run in a container of the docker runtime instead
// of as a host process. Name is also the node's host name on Network.
// Image is the image the lab was created with, empty for the one forklab
// builds: which image that is depends on the forklab that starts the node.
// Publish lists the port mappings, as docker run -p takes them.
//
// Subnet is the network's address range and IP the node's address in it.
// Both are fixed when the lab is created, and the node's peers dial IP: a
// node resolves a peer's name once per start and keeps the address, in
// memory and in its address book, so an address that changed with a new
// container would cut the node off.
type Container struct {
	Name    string   `json:"name"`
	Image   string   `json:"image,omitempty"`
	Network string   `json:"network"`
	Subnet  string   `json:"subnet"`
	IP      string   `json:"ip"`
	Publish []string `json:"publish,omitempty"`
}

// ContainerName and NetworkName are what a lab's containers and network
// are called.
func ContainerName(lab, node string) string { return "forklab-" + lab + "-" + node }
func NetworkName(lab string) string         { return "forklab-" + lab }

// nodeDockerfile is the image a docker lab's nodes run in unless the lab
// names another one. Chain binaries are not in it: they are bind-mounted,
// so one image serves every chain and version.
const nodeDockerfile = `FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl jq procps \
 && rm -rf /var/lib/apt/lists/*
`

// nodeImage is the tag of the image built from nodeDockerfile; it changes
// with the Dockerfile.
var nodeImage = fmt.Sprintf("forklab-node:%.12s", fmt.Sprintf("%x", sha256.Sum256([]byte(nodeDockerfile))))

// nixStore is bind-mounted read-only when the host has it, so a binary
// built on NixOS finds its interpreter and libraries in the container.
var nixStore = "/nix/store"

// dockerRetry is how long to wait before asking again about a container
// whose state the engine did not tell.
var dockerRetry = time.Second

// dockerTimeout bounds a docker call that only asks or tells the engine
// something. An engine that accepts the call and never answers must not
// hold a node's lock for good.
var dockerTimeout = 30 * time.Second

var errNoDocker = errors.New("the docker CLI is not on PATH; a lab on the docker runtime needs it")

// docker runs the docker CLI and returns its stdout. Tests replace it.
var docker = func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	// A helper of the CLI that keeps the pipes open must not hold the call
	// past its context.
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errNoDocker
	}
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// engine runs a docker call within the time its kind may take. A call that
// asks or tells the engine something gets dockerTimeout. run and build may
// pull an image first. wait lasts as long as the container, so only ctx
// ends it. A call that ran out of time says nothing about the container:
// its error is never the engine's "no such container".
func engine(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	limit := dockerTimeout
	switch args[0] {
	case "wait":
		return docker(ctx, stdin, args...)
	case "run":
		limit = max(limit, 10*time.Minute)
	case "build":
		limit = max(limit, 30*time.Minute)
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, err := docker(ctx, stdin, args...)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("docker %s: the engine did not answer within %s", args[0], limit)
	}
	return out, err
}

// labLabel is on every container and network forklab creates. Its value is
// the lab directory. Two labs of one name under two forklab homes share
// the names of their containers and network, so the label, not the name,
// says whose they are: forklab removes or reuses only what carries its own.
const labLabel = "forklab.lab"

func labOf(spec NodeSpec) string { return filepath.Dir(spec.Home) }

// absent reports whether err is the engine saying that it has no container
// or network of the name asked about. The docker CLI exits 1 for that as
// it does for an engine it cannot reach, so the two are told apart by the
// engine's own words, and by nothing looser: an error that only happens to
// hold "not found" says nothing about the container or network. Docker
// says "No such container: x", "No such network: x" and "network x not
// found"; Podman "no such container" and "network not found".
func absent(err error, kind string) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such "+kind) || (kind == "network" && networkNotFound.MatchString(m))
}

var networkNotFound = regexp.MustCompile(`\bnetwork( \S+)? not found`)

// inspect runs a docker command that prints fields of the engine's answer,
// args with --format. The Docker CLI and Podman's own docker command name
// some fields differently; podmanFormat is tried when the CLI does not know
// a field of format.
func inspect(ctx context.Context, format, podmanFormat string, command []string, target ...string) ([]byte, error) {
	ask := func(format string) ([]byte, error) {
		return engine(ctx, nil, append(append(slices.Clone(command), "--format", format), target...)...)
	}
	out, err := ask(format)
	if err != nil && strings.Contains(err.Error(), "can't evaluate field") {
		return ask(podmanFormat)
	}
	return out, err
}

// owner returns the lab directory the container or network called name is
// labelled with. found is false when the engine has none of that name.
func owner(ctx context.Context, kind, name string) (lab string, found bool, err error) {
	labels := ".Config.Labels"
	if kind == "network" {
		labels = ".Labels"
	}
	out, err := engine(ctx, nil, kind, "inspect", "--format", `{{index `+labels+` "`+labLabel+`"}}`, name)
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	if absent(err, kind) {
		return "", false, nil
	}
	return "", false, err
}

// own reports whether the engine has a container or network called name
// that this lab created. One that another lab or the user created is an
// error: forklab does not touch it.
func own(ctx context.Context, kind, name, lab string) (bool, error) {
	label, found, err := owner(ctx, kind, name)
	if err != nil || !found {
		return false, err
	}
	if label == lab {
		return true, nil
	}
	whose := "forklab did not create it"
	if label != "" {
		whose = "it belongs to the lab in " + label
	}
	return false, fmt.Errorf("the %s %s is not this lab's: %s. forklab leaves it alone; remove it yourself, or use another lab name", kind, name, whose)
}

// removeContainer removes this lab's container called name, if there is
// one.
func removeContainer(ctx context.Context, name, lab string) error {
	if mine, err := own(ctx, "container", name, lab); err != nil || !mine {
		return err
	}
	_, err := engine(ctx, nil, "rm", "-f", name)
	return err
}

// dockerSetup serializes the checks every start makes, so nodes started
// together build the image and create the network once.
var dockerSetup sync.Mutex

// dockerRootless asks the engine whether it runs rootless, which is also
// how an unreachable engine is found. The Docker CLI lists "rootless"
// among the security options; Podman's own CLI has a field for it.
func dockerRootless(ctx context.Context) (bool, error) {
	out, err := inspect(ctx, "{{json .SecurityOptions}}", "{{.Host.Security.Rootless}}", []string{"info"})
	if errors.Is(err, errNoDocker) {
		return false, err
	}
	if err != nil {
		return false, fmt.Errorf("the docker engine does not answer: %w", err)
	}
	return bytes.Contains(out, []byte("rootless")) || strings.TrimSpace(string(out)) == "true", nil
}

// ensureImage returns the image to run for a lab created with image: that
// one, or for a lab that named none the image this forklab builds, built
// now when the engine lacks it.
func ensureImage(ctx context.Context, image string) (string, error) {
	if image != "" {
		return image, nil
	}
	if hasNodeImage(ctx) {
		return nodeImage, nil
	}
	return nodeImage, buildNodeImage(ctx)
}

func hasNodeImage(ctx context.Context) bool {
	_, err := engine(ctx, nil, "image", "inspect", "--format", "{{.Id}}", nodeImage)
	return err == nil
}

// buildNodeImage builds the node image with whatever docker command there
// is. A buildx container builder leaves the image in its own cache, and
// exits 0, unless told to --load it. The Docker CLI without the buildx
// plugin builds straight into the engine and does not know the flag, so
// the build is then run again without it. Whatever built, the engine is
// asked for the image afterwards.
func buildNodeImage(ctx context.Context) error {
	build := func(flags ...string) error {
		_, err := engine(ctx, strings.NewReader(nodeDockerfile), append(append([]string{"build"}, flags...), "-t", nodeImage, "-")...)
		return err
	}
	err := build("--load")
	if err != nil && strings.Contains(err.Error(), "unknown flag: --load") {
		err = build()
	}
	if err != nil {
		return fmt.Errorf("build the node image %s: %w", nodeImage, err)
	}
	if !hasNodeImage(ctx) {
		return fmt.Errorf("build the node image %s: the build succeeded and the engine does not have it; build it into the engine yourself from the Dockerfile that forklab pipes to docker build, or pass an image with --image", nodeImage)
	}
	return nil
}

// PrepareImage builds the node image when one of specs runs in it and the
// engine does not have it, after calling building. A command does this
// before it asks the supervisor to start a node: a build takes longer than
// a start request may. Specs without such a node need no engine.
func PrepareImage(ctx context.Context, specs []NodeSpec, building func(image string)) error {
	if !slices.ContainsFunc(specs, func(s NodeSpec) bool { return s.Container != nil && s.Container.Image == "" }) {
		return nil
	}
	dockerSetup.Lock()
	defer dockerSetup.Unlock()
	if hasNodeImage(ctx) {
		return nil
	}
	building(nodeImage)
	return buildNodeImage(ctx)
}

// LabNetwork creates the network of the lab in labDir, unless the lab has
// it, and returns its subnet. The network lives until RemoveNetwork or
// Teardown, so the subnet stays the lab's.
func LabNetwork(ctx context.Context, name, labDir string) (string, error) {
	dockerSetup.Lock()
	defer dockerSetup.Unlock()
	if mine, err := own(ctx, "network", name, labDir); err != nil {
		return "", err
	} else if mine {
		return networkSubnet(ctx, name)
	}
	return createNetwork(ctx, name, labDir, "")
}

// createNetwork creates the lab's network on subnet. Without one the
// engine chooses, so the subnet collides with no other network, and the
// network is then created again with that subnet named: Docker gives a
// container a fixed address only on a network whose subnet was named.
func createNetwork(ctx context.Context, name, lab, subnet string) (string, error) {
	create := func(subnet string) error {
		args := []string{"network", "create", "--label", labLabel + "=" + lab}
		if subnet != "" {
			args = append(args, "--subnet", subnet)
		}
		_, err := engine(ctx, nil, append(args, name)...)
		return err
	}
	if subnet != "" {
		if err := create(subnet); err != nil {
			return "", fmt.Errorf("create the network %s on the lab's subnet %s: %w", name, subnet, err)
		}
		return subnet, nil
	}
	var err error
	// Another network can take the subnet between the removal and the
	// second creation; the engine then chooses again.
	for range 3 {
		if err = create(""); err != nil {
			return "", err
		}
		if subnet, err = networkSubnet(ctx, name); err != nil {
			// The caller cleans up only what it was told exists.
			_, _ = engine(ctx, nil, "network", "rm", name)
			return "", err
		}
		if _, err = engine(ctx, nil, "network", "rm", name); err != nil {
			return "", err
		}
		if err = create(subnet); err == nil {
			return subnet, nil
		}
	}
	return "", err
}

// networkSubnet returns the network's IPv4 subnet.
func networkSubnet(ctx context.Context, name string) (string, error) {
	out, err := inspect(ctx, "{{range .IPAM.Config}}{{.Subnet}} {{end}}", "{{range .Subnets}}{{.Subnet}} {{end}}", []string{"network", "inspect"}, name)
	if err != nil {
		return "", err
	}
	for _, s := range strings.Fields(string(out)) {
		if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is4() {
			return p.Masked().String(), nil
		}
	}
	return "", fmt.Errorf("the network %s has no IPv4 subnet: %q", name, strings.TrimSpace(string(out)))
}

// NodeIP returns the address of node index in subnet. The first addresses
// are left to the engine, which puts the gateway there.
func NodeIP(subnet string, index int) (string, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		return "", err
	}
	addr := p.Masked().Addr()
	for range 10 + index {
		addr = addr.Next()
	}
	// The last address of the subnet is its broadcast address.
	if !p.Contains(addr) || !p.Contains(addr.Next()) {
		return "", fmt.Errorf("the subnet %s has no address for node %d", subnet, index)
	}
	return addr.String(), nil
}

// ensureNetwork creates the lab's network again on its subnet when it is
// gone, for example after docker network prune.
func ensureNetwork(ctx context.Context, c *Container, lab string) error {
	if mine, err := own(ctx, "network", c.Network, lab); err != nil || mine {
		return err
	}
	_, err := createNetwork(ctx, c.Network, lab, c.Subnet)
	return err
}

// RemoveNetwork removes the lab's network, if the engine has it.
func RemoveNetwork(ctx context.Context, name, labDir string) error {
	mine, err := own(ctx, "network", name, labDir)
	if err == nil && mine {
		_, err = engine(ctx, nil, "network", "rm", name)
	}
	return err
}

// userArgs keep the lab's files owned by the host user. On a rootless
// engine the container's root is that user, and any other uid, such as the
// one an image's USER names, maps to a subordinate one that cannot write
// the lab and whose files the user cannot delete. On a rootful engine the
// container runs as the user, with a writable HOME.
func userArgs(rootless bool) []string {
	if rootless {
		return []string{"--user", "0:0"}
	}
	return []string{"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "-e", "HOME=/tmp"}
}

// hostArgs fit a container to the host: userArgs, and SELinux label
// separation off. An enforcing host denies a container every bind mount
// that lacks a container label. The other way, relabelling with :z, would
// rewrite the labels of the lab directory, the binaries, and /nix/store.
// Not tried on an SELinux host.
func hostArgs(rootless bool) []string {
	return append([]string{"--security-opt", "label=disable"}, userArgs(rootless)...)
}

// mountArgs bind-mount each file read-only at its host path, so every path
// in nodes.json and the node's configuration means the same in the
// container.
func mountArgs(files ...string) []string {
	var args []string
	for _, f := range files {
		args = append(args, "-v", f+":"+f+":ro")
	}
	if _, err := os.Stat(nixStore); err == nil {
		args = append(args, "-v", nixStore+":"+nixStore+":ro")
	}
	return args
}

// Preflight runs binary in the container's image, with `version` unless
// other arguments are given, so a binary the image cannot run (a missing
// library, another libc) fails with the loader's message before any node
// depends on it.
func (c Container) Preflight(ctx context.Context, binary string, args ...string) error {
	rootless, err := dockerRootless(ctx)
	if err != nil {
		return err
	}
	image, err := ensureImage(ctx, c.Image)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"version"}
	}
	run := append([]string{"run", "--rm", "--network", "none", "--entrypoint", binary}, hostArgs(rootless)...)
	run = append(append(append(run, mountArgs(binary)...), image), args...)
	if _, err := engine(ctx, nil, run...); err != nil {
		return fmt.Errorf("%s does not run in the image %s: %w; the image needs every library the binary loads, so pass one that has them with --image", binary, image, err)
	}
	return nil
}

// Labelled names the containers and networks the engine holds for the lab
// in labDir, whatever they are called. It serves a lab whose nodes.json,
// which names them, is gone.
func Labelled(ctx context.Context, labDir string) ([]string, error) {
	var names []string
	for _, list := range [][]string{{"ps", "-a", "--format", "{{.Names}}"}, {"network", "ls", "--format", "{{.Name}}"}} {
		out, err := engine(ctx, nil, append(list, "--filter", "label="+labLabel+"="+labDir)...)
		if err != nil {
			return nil, err
		}
		names = append(names, strings.Fields(string(out))...)
	}
	return names, nil
}

// containerScript is what the container runs. It first links the chain
// binary's default home to the node home, so that in a shell in the
// container the binary finds the node without --home, then becomes the
// node with its output appended to the log file in the lab directory. The
// default home is $HOME/<dir>; <dir> is read from the help text under a
// scratch HOME, because some binaries create their default home whenever
// they run.
const containerScript = `d=$(HOME=/tmp/.forklab-probe "$FORKLAB_BIN" --help 2>/dev/null | sed -n 's|.*--home string.*(default "/tmp/.forklab-probe/\(.*\)").*|\1|p' | head -n 1)
if [ -n "$d" ] && [ ! -e "$HOME/$d" ]; then ln -s "$FORKLAB_NODE_HOME" "$HOME/$d"; fi
exec "$0" "$@" >>"$FORKLAB_NODE_LOG" 2>&1`

// runArgs is the docker run command line of spec's node in image.
func runArgs(spec NodeSpec, rootless bool, image string) []string {
	c := spec.Container
	lab := filepath.Dir(spec.Home)
	program, programArgs, files := spec.Binary, spec.Args, []string{spec.Binary}
	chain := spec.Binary
	env := []string{"FORKLAB_NODE_HOME=" + spec.Home, "FORKLAB_NODE_LOG=" + spec.LogPath, "FORKLAB_KEYS=" + filepath.Join(lab, "keys")}
	if cv := spec.Cosmovisor; cv != nil {
		chain = filepath.Join(spec.cosmovisorDir().root(), "current", "bin", cv.Name)
		program, programArgs, files = cv.Path, append([]string{"run"}, spec.Args...), append(files, cv.Path)
		env = append(env, cv.env(spec.Home)...)
	}
	env = append(env, "FORKLAB_BIN="+chain, "PATH="+filepath.Dir(chain)+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	args := []string{"run", "-d", "--name", c.Name, "--label", labLabel + "=" + lab, "--hostname", spec.Name, "--network", c.Network, "--ip", c.IP, "--workdir", spec.Home, "--entrypoint", "sh"}
	for _, p := range c.Publish {
		args = append(args, "-p", p)
	}
	args = append(args, hostArgs(rootless)...)
	args = append(args, "-v", lab+":"+lab)
	args = append(args, mountArgs(files...)...)
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, image, "-c", containerScript, program)
	return append(args, programArgs...)
}

// dockerRuntime runs a node as a container identified by its id. The node
// writes its log into the bind-mounted lab directory itself, so the log
// tail and everything built on it work as for a host process.
type dockerRuntime struct{}

func (dockerRuntime) start(spec NodeSpec) (pidFile, func(<-chan struct{}) (exitInfo, bool), error) {
	ctx := context.Background()
	c, lab := spec.Container, labOf(spec)
	// The engine mounts a path that does not exist as an empty directory,
	// and the node then fails with nothing that says why.
	mounted := []string{spec.Binary}
	if cv := spec.Cosmovisor; cv != nil {
		mounted = append(mounted, cv.Path)
	}
	for _, file := range mounted {
		if _, err := os.Stat(file); err != nil {
			return pidFile{}, nil, fmt.Errorf("the container needs %s mounted: %w", file, err)
		}
	}
	dockerSetup.Lock()
	rootless, err := dockerRootless(ctx)
	image := ""
	if err == nil {
		image, err = ensureImage(ctx, c.Image)
	}
	if err == nil {
		err = ensureNetwork(ctx, c, lab)
	}
	dockerSetup.Unlock()
	if err != nil {
		return pidFile{}, nil, err
	}
	// The log tail opens the file as soon as the node is attached.
	logf, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return pidFile{}, nil, err
	}
	_ = logf.Close()
	// A container of an earlier run that nobody saw exit still has the name.
	if err := removeContainer(ctx, c.Name, lab); err != nil {
		return pidFile{}, nil, err
	}
	out, err := engine(ctx, nil, runArgs(spec, rootless, image)...)
	if err != nil {
		_ = removeContainer(ctx, c.Name, lab)
		return pidFile{}, nil, err
	}
	id := strings.TrimSpace(string(out))
	wait := func(stop <-chan struct{}) (exitInfo, bool) { return containerExitUnless(stop, id) }
	return pidFile{Binary: spec.Binary, Container: id, StartedAt: time.Now()}, wait, nil
}

// alive asks about the id, not the name, so a container created since for
// the same node is never taken for the recorded one. Without a recorded id
// it asks about this lab's container of the node's name: a supervisor that
// died right after docker run left one that no file records. An engine
// that does not tell is an error, never "not alive".
func (dockerRuntime) alive(spec NodeSpec, pf pidFile) (bool, error) {
	ctx := context.Background()
	id := pf.Container
	if id == "" {
		id = spec.Container.Name
		if label, found, err := owner(ctx, "container", id); err != nil || !found || label != labOf(spec) {
			return false, err
		}
	}
	state, err := containerStatus(ctx, id)
	return state == containerUp, err
}

// containerState is what the engine says about a container. Unknown is
// every answer that is not about the container, such as an engine that is
// down: the container may well be running, so nothing may be concluded.
type containerState int

const (
	containerUnknown containerState = iota
	containerUp
	containerGone
)

// containerStatus returns containerUp or containerGone with a nil error,
// and containerUnknown with the reason. Gone covers a container that
// exists but does not run and one the engine has no record of.
func containerStatus(ctx context.Context, id string) (containerState, error) {
	out, err := engine(ctx, nil, "container", "inspect", "--format", "{{.State.Running}}", id)
	switch running := strings.TrimSpace(string(out)); {
	case err == nil && running == "true":
		return containerUp, nil
	case err == nil && running == "false":
		return containerGone, nil
	case err == nil:
		return containerUnknown, fmt.Errorf("docker container inspect %s: unexpected answer %q", id, running)
	case absent(err, "container"):
		return containerGone, nil
	}
	return containerUnknown, err
}

func (dockerRuntime) watch(_ NodeSpec, pf pidFile, stop <-chan struct{}) bool {
	_, exited := containerExitUnless(stop, pf.Container)
	return exited
}

// containerExitUnless is containerExit that gives up when stop closes, so
// no docker wait outlives the supervisor.
func containerExitUnless(stop <-chan struct{}, id string) (exitInfo, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return containerExit(ctx, id)
}

// containerExit blocks until the container is confirmed to have exited,
// then removes it. It reports false when ctx ends first. While the engine
// does not tell, because it is down or the wait broke off, it keeps asking:
// a container is only removed, and its node only reported exited, on the
// engine's word. An exit code above 128 is the shell convention for death
// by a signal.
func containerExit(ctx context.Context, id string) (exitInfo, bool) {
	for {
		out, err := engine(ctx, nil, "wait", id)
		if ctx.Err() != nil {
			return exitInfo{}, false
		}
		e := exitInfo{at: time.Now()}
		if code, convErr := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && convErr == nil {
			if code > 128 {
				e.signal = syscall.Signal(code - 128).String()
			} else {
				e.code = &code
			}
		} else if state, _ := containerStatus(ctx, id); state != containerGone {
			select {
			case <-ctx.Done():
				return exitInfo{}, false
			case <-time.After(dockerRetry):
			}
			continue
		}
		_, _ = engine(context.Background(), nil, "rm", "-f", id)
		return e, true
	}
}

// signal reports an error unless the engine took the signal or confirms
// that the container is gone. It addresses the container by its id, which
// no other container can come to have, so it needs no check of its own for
// an adopted one.
func (dockerRuntime) signal(_ NodeSpec, pf pidFile, sig syscall.Signal, _ bool) error {
	ctx := context.Background()
	name := "TERM"
	if sig == syscall.SIGKILL {
		name = "KILL"
	}
	_, err := engine(ctx, nil, "kill", "--signal", name, pf.Container)
	if err == nil {
		return nil
	}
	if state, _ := containerStatus(ctx, pf.Container); state == containerGone {
		return nil
	}
	return err
}

// reap removes the lab's container of the node that no pid file records:
// a supervisor that died right after docker run left it, and nothing else
// would stop it.
func (dockerRuntime) reap(spec NodeSpec) {
	if err := removeContainer(context.Background(), spec.Container.Name, labOf(spec)); err != nil {
		log.Printf("node %d: remove the unrecorded container %s: %v", spec.Index, spec.Container.Name, err)
	}
}

// Teardown removes what the docker runtime created for labDir's nodes: any
// container left behind and the lab's network. It fails when the engine
// does not confirm that they are gone, and when something of those names
// is not this lab's. A lab without container nodes needs no docker and is
// left alone.
func Teardown(labDir string) error {
	specs, err := LoadNodes(labDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	errs := []error{removeContainers(specs)}
	networks := map[string]bool{}
	for _, s := range specs {
		if c := s.Container; c != nil && !networks[c.Network] {
			networks[c.Network] = true
			errs = append(errs, RemoveNetwork(ctx, c.Network, labOf(s)))
		}
	}
	return errors.Join(errs...)
}

// removeContainers removes the containers the nodes left behind. The
// network stays: the lab keeps its subnet for as long as it exists.
func removeContainers(specs []NodeSpec) error {
	ctx := context.Background()
	var errs []error
	for _, s := range specs {
		if s.Container != nil {
			errs = append(errs, removeContainer(ctx, s.Container.Name, labOf(s)))
		}
	}
	return errors.Join(errs...)
}
