package supervisor_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
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

type fileSubscriber struct {
	mu   sync.Mutex
	path string
}

func (s *fileSubscriber) NodeLine(index int, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%d: %s\n", index, line)
}

func testSpawner(labDir string) (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "supervisor-run", labDir)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	return cmd, nil
}

// fakeNode stands in for a chain binary: it prints a line every 50ms, exits
// 0 on SIGTERM unless --ignore-term, and exits with --crash-code on SIGUSR1.
func fakeNode(args []string) int {
	fs := flag.NewFlagSet("fake-node", flag.ContinueOnError)
	home := fs.String("home", "", "node home")
	ignoreTerm := fs.Bool("ignore-term", false, "keep running after SIGTERM")
	crashCode := fs.Int("crash-code", 3, "exit code on SIGUSR1")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Printf("fake node %d starting in %s\n", os.Getpid(), *home)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGUSR1)
	tick := time.NewTicker(50 * time.Millisecond)
	for i := 0; ; i++ {
		select {
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
