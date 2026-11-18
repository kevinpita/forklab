package supervisor

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A profile's binary may be a wrapper script that sets up the environment
// and execs the chain binary. The process then no longer names the wrapper,
// which is what the node was started with. It is still the supervisor's own
// child, and is signalled as such.
func TestNodeStartedThroughAWrapperThatExecsIsStopped(t *testing.T) {
	for _, tt := range []struct {
		name string
		sig  syscall.Signal
		// trap is what the wrapped program does about SIGTERM.
		trap string
	}{
		{"SIGTERM", syscall.SIGTERM, ""},
		{"SIGKILL", syscall.SIGKILL, "trap '' TERM\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "node0")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			wrapper, wrapped := filepath.Join(dir, "wrapper"), filepath.Join(dir, "chaind")
			for path, body := range map[string]string{
				wrapper: "#!/bin/sh\nexec " + wrapped + " \"$@\"\n",
				wrapped: "#!/bin/sh\n" + tt.trap + "while :; do sleep 0.05; done\n",
			} {
				if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			spec := NodeSpec{
				Name: "node0", Binary: wrapper, Args: []string{"start", "--home", home}, Home: home,
				LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid"),
			}
			stop := make(chan struct{})
			var wg sync.WaitGroup
			n := &node{log: log.New(io.Discard, "", 0), stop: stop, wg: &wg, rt: hostRuntime{}, spec: spec}
			if err := n.start(); err != nil {
				t.Fatal(err)
			}
			pid := n.status().PID
			t.Cleanup(func() {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				close(stop)
				wg.Wait()
			})
			// The wrapper has become the wrapped program.
			for deadline := time.Now().Add(3 * time.Second); cmdlineMatches(pid, wrapper, home) && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			if err := n.signal(tt.sig, 3*time.Second); err != nil {
				t.Errorf("%s did not stop the node: %v", tt.name, err)
			}
			if st := n.status(); st.State != StateExited {
				t.Errorf("node = %+v, want it exited", st)
			}
		})
	}
}
