package supervisor_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/supervisor"
)

// childEnv gates the roles below: the test binary re-executes itself as the
// supervisor, as a fake node, or as a short-lived client. The role is
// argv[1], so nodes spawned by a child supervisor inherit the gate safely.
const childEnv = "FORKLAB_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" && len(os.Args) > 1 {
		os.Exit(runChild(os.Args[1], os.Args[2:]))
	}
	os.Exit(m.Run())
}

func runChild(role string, args []string) int {
	switch role {
	case "supervisor-run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		opts := supervisor.Options{LabDir: args[0]}
		if path := os.Getenv(sublogEnv); path != "" {
			opts.Subscriber = &fileSubscriber{path: path}
		}
		if path := os.Getenv(versionLogEnv); path != "" {
			opts.RecordVersion = slowRecorder(path)
		}
		err := supervisor.Run(ctx, opts)
		if errors.Is(err, supervisor.ErrAlreadyRunning) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "ensure":
		if _, err := supervisor.EnsureRunning(context.Background(), args[0], testSpawner); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "fake-node":
		return fakeNode(args)
	}
	fmt.Fprintln(os.Stderr, "unknown child role", role)
	return 2
}

// sublogEnv names a file the child supervisor's subscriber appends
// "<index>: <line>" to, so a test can see what the subscriber received.
const sublogEnv = "FORKLAB_TEST_SUBLOG"

// versionLogEnv names a file the child supervisor's RecordVersion appends
// "<index> <version>" to, or "overlap" when two calls run at once.
const versionLogEnv = "FORKLAB_TEST_VERSIONLOG"

// slowRecorder stands in for the lab.yaml writer, which is a read-modify-
// write that two concurrent swaps would corrupt; it takes long enough that
// unserialized calls overlap.
func slowRecorder(path string) func(int, string) error {
	var busy atomic.Bool
	return func(index int, version string) error {
		if !busy.CompareAndSwap(false, true) {
			return appendLine(path, "overlap\n")
		}
		defer busy.Store(false)
		time.Sleep(300 * time.Millisecond)
		return appendLine(path, fmt.Sprintf("%d %s\n", index, version))
	}
}

type fileSubscriber struct {
	mu   sync.Mutex
	path string
}

func (s *fileSubscriber) NodeLine(index int, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = appendLine(s.path, fmt.Sprintf("%d: %s\n", index, line))
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line)
	return err
}

func testSpawner(labDir string) (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "supervisor-run", labDir)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	return cmd, nil
}

// fakeNode stands in for a chain binary: it prints a line every 50ms, exits
// 0 on SIGTERM unless --ignore-term, and exits with --crash-code on SIGUSR1.
// With --halt name:height[,name:height...] it logs, after --halt-delay,
// the upgrade halt x/upgrade would for the first plan its release lacks
// and exits 1, or keeps running with --halt-stay. A copy of the binary
// named *-<name> stands for the release that carries plan <name> and all
// plans listed before it.
func fakeNode(args []string) int {
	fs := flag.NewFlagSet("fake-node", flag.ContinueOnError)
	home := fs.String("home", "", "node home")
	ignoreTerm := fs.Bool("ignore-term", false, "keep running after SIGTERM")
	crashCode := fs.Int("crash-code", 3, "exit code on SIGUSR1")
	halt := fs.String("halt", "", "plans to halt on, as name:height, comma separated")
	haltDelay := fs.Duration("halt-delay", 0, "how long after starting to halt")
	haltStay := fs.Bool("halt-stay", false, "keep running after the halt")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Printf("fake node %d starting in %s\n", os.Getpid(), *home)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGUSR1)
	var haltAt <-chan time.Time
	var name, height string
	plans := strings.Split(*halt, ",")
	next := 0
	for i, plan := range plans {
		if n, _, _ := strings.Cut(plan, ":"); strings.HasSuffix(os.Args[0], "-"+n) {
			next = i + 1
		}
	}
	if *halt != "" && next < len(plans) {
		name, height, _ = strings.Cut(plans[next], ":")
		haltAt = time.After(*haltDelay)
	}
	tick := time.NewTicker(50 * time.Millisecond)
	for i := 0; ; i++ {
		select {
		case <-haltAt:
			fmt.Printf("\x1b[90m1:02PM\x1b[0m \x1b[31mERR\x1b[0m \x1b[1mUPGRADE %q NEEDED at height: %s: module=x/upgrade\x1b[0m\n", name, height)
			fmt.Printf("ERR CONSENSUS FAILURE!!! err=\"failed to apply block; error UPGRADE \\\"%s\\\" NEEDED at height: %s\" module=consensus\n", name, height)
			if !*haltStay {
				return 1
			}
			haltAt = nil
		case <-tick.C:
			fmt.Printf("tick %d\n", i)
		case sig := <-sigs:
			switch {
			case sig == syscall.SIGUSR1:
				fmt.Println("crashing")
				return *crashCode
			case *ignoreTerm:
				fmt.Println("ignoring SIGTERM")
			default:
				fmt.Println("bye")
				return 0
			}
		}
	}
}
