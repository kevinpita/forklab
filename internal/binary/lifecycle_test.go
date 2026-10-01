package binary

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
)

// scratchDirs lists stage, work, and old directories under a profile dir.
func scratchDirs(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{".stage-*", ".work-*", ".old-*"} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m...)
	}
	return out
}

// slowServer serves body after delay and counts requests.
func slowServer(t *testing.T, body []byte, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(delay)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestConcurrentResolveDownloadsOnce(t *testing.T) {
	srv, hits := slowServer(t, tarGz(t, entry{"bin/chaind", script("1.2.3\n")}), 300*time.Millisecond)
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/c.tar.gz")})

	var wg sync.WaitGroup
	results := make([]Binary, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { results[i], errs[i] = c.Resolve(context.Background(), p, "1.2.3", Options{}) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors: %v, %v", errs[0], errs[1])
	}
	if hits.Load() != 1 || results[0] != results[1] {
		t.Errorf("downloads = %d, results %+v and %+v; want one download shared by both", hits.Load(), results[0], results[1])
	}
}

// TestHelperResolve is a child process for TestConcurrentResolveAcrossProcesses.
func TestHelperResolve(t *testing.T) {
	url := os.Getenv("FORKLAB_TEST_RESOLVE_URL")
	if url == "" {
		t.Skip("helper process only")
	}
	c := Cache{Dir: os.Getenv("FORKLAB_TEST_RESOLVE_CACHE")}
	p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(url)})
	if _, err := c.Resolve(context.Background(), p, "1.2.3", Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentResolveAcrossProcesses(t *testing.T) {
	srv, hits := slowServer(t, tarGz(t, entry{"bin/chaind", script("1.2.3\n")}), 300*time.Millisecond)
	cache := t.TempDir()
	var procs []*exec.Cmd
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperResolve$")
		cmd.Env = append(os.Environ(), "FORKLAB_TEST_RESOLVE_URL="+srv.URL+"/c.tar.gz", "FORKLAB_TEST_RESOLVE_CACHE="+cache)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		procs = append(procs, cmd)
	}
	for _, cmd := range procs {
		if err := cmd.Wait(); err != nil {
			t.Errorf("helper process: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("downloads = %d, want 1", hits.Load())
	}
	if listed, err := (Cache{Dir: cache}).List(); err != nil || len(listed) != 1 {
		t.Errorf("list = %+v, %v", listed, err)
	}
}

func TestCancelMidDownloadLeavesNothing(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write(make([]byte, 1000))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/c.tar.gz")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := c.Resolve(ctx, p, "1.2.3", Options{Progress: func(int64, int64) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if left := scratchDirs(t, filepath.Join(c.Dir, "chain")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if listed, _ := c.List(); len(listed) != 0 {
		t.Errorf("cancelled download was cached: %+v", listed)
	}
}

func TestCancelMidBuildKillsTheProcessGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "sleeper.pid")
	src := profile.GitSource{
		Repo:  profile.Template("file://" + bareRepo(t, root)),
		Ref:   "v{version}",
		Build: `sleep 60 & echo $! > "$PIDFILE"; wait`,
		Out:   "out/chaind",
		Env:   map[string]string{"PIDFILE": pidFile},
	}
	c := Cache{Dir: filepath.Join(root, "cache")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(pidFile); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	start := time.Now()
	_, err := c.Resolve(ctx, chainProfile("1.0.0", src), "1.0.0", Options{})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %s, want a prompt cancellation error", err, time.Since(start))
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("build grandchild %d outlived the cancelled build", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if left := scratchDirs(t, filepath.Join(c.Dir, "chain")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestResolveSweepsOnlyItsOwnCrashLeftovers(t *testing.T) {
	srv, _ := serve(t, script("1.2.3\n"))
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/chaind")})
	b, err := c.Resolve(t.Context(), p, "1.2.3", Options{})
	if err != nil {
		t.Fatal(err)
	}
	parent, slot := filepath.Dir(filepath.Dir(b.Path)), filepath.Base(filepath.Dir(b.Path))
	for _, name := range []string{".stage-" + slot, ".work-" + slot, ".old-" + slot, ".stage-1.2.3"} {
		if err := os.MkdirAll(filepath.Join(parent, name, "half"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Resolve(t.Context(), p, "1.2.3", Options{}); err != nil {
		t.Fatal(err)
	}
	left := scratchDirs(t, parent)
	if len(left) != 1 || filepath.Base(left[0]) != ".stage-1.2.3" {
		t.Fatalf("left %v, want legacy scratch unchanged", left)
	}
}

func TestPathNoVerifyIsRemembered(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "chaind")
	if err := os.WriteFile(bin, script("9.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.0.0", profile.PathSource{Path: profile.Template(bin)})

	if _, err := c.Resolve(context.Background(), p, "1.0.0", Options{}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want a mismatch", err)
	}
	if _, err := c.Resolve(context.Background(), p, "1.0.0", Options{NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	b, err := c.Resolve(context.Background(), p, "1.0.0", Options{})
	if err != nil || b.Check != CheckMismatch || b.ReportedVersion != "9.0.0" {
		t.Fatalf("after --no-verify: %+v, %v; want the accepted mismatch reused", b, err)
	}

	if err := os.WriteFile(bin, script("9.0.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(context.Background(), p, "1.0.0", Options{}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("after the file changed err = %v, want a fresh mismatch", err)
	}
}

func TestReportedVersionPrefersStdout(t *testing.T) {
	tests := []struct {
		name, script, want string
	}{
		{"stdout wins over stderr noise", "echo 'warning: x' >&2; echo 1.0.0", "1.0.0"},
		{"stderr when stdout is empty", "echo 1.0.0 >&2", "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "chaind")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := reportedVersion(context.Background(), bin, "chaind")
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	bin := filepath.Join(t.TempDir(), "chaind")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho boom >&2; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := reportedVersion(context.Background(), bin, "chaind"); err == nil || !strings.Contains(err.Error(), "chaind version") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the name and stderr", err)
	}
}
