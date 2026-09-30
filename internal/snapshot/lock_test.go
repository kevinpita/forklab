package snapshot_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/snapshot"
)

// slowChain is fakeChain with an export that takes long enough for the
// other runs to queue behind it.
var slowChain = strings.Replace(fakeChain, `echo run >> "$dir/export.count"`, `echo run >> "$dir/export.count"; sleep 1`, 1)

func TestExportSerializesConcurrentRuns(t *testing.T) {
	bin := fakeBinary(t, slowChain)
	in := exportInput(t, bin)
	const n = 4
	results := make([]snapshot.Exported, n)
	logs := make([]bytes.Buffer, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run := in
			run.Log = &logs[i]
			results[i], errs[i] = snapshot.Export(context.Background(), run)
		}()
	}
	wg.Wait()
	fresh, waited := 0, 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if results[i].Path != results[0].Path {
			t.Fatalf("run %d exported to %s, run 0 to %s", i, results[i].Path, results[0].Path)
		}
		if !results[i].Cached {
			fresh++
		}
		if strings.Contains(logs[i].String(), "waiting for another forklab process") {
			waited++
		}
	}
	if fresh != 1 || runs(t, bin, "export") != 1 {
		t.Fatalf("%d fresh exports and %d export runs, want one of each", fresh, runs(t, bin, "export"))
	}
	if waited == 0 {
		t.Fatal("no run reported waiting for the lock")
	}
	if exists(t, filepath.Join(in.WorkDir, "lock")) {
		t.Fatal("lock file left behind")
	}
}

func TestExportRejectsATamperedCache(t *testing.T) {
	bin := fakeBinary(t, fakeChain)
	in := exportInput(t, bin)
	first := export(t, in)
	f, err := os.OpenFile(first.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	second := export(t, in)
	if second.Cached || runs(t, bin, "export") != 2 {
		t.Fatalf("cached=%v export runs=%d, want a re-export after the export file changed", second.Cached, runs(t, bin, "export"))
	}
}

func TestExportCacheKeyIncludesChainIDAndBinary(t *testing.T) {
	bin := fakeBinary(t, fakeChain)
	in := exportInput(t, bin)
	export(t, in)
	in.ChainID = "other-1"
	if out := export(t, in); out.Cached || runs(t, bin, "export") != 2 {
		t.Fatalf("cached=%v runs=%d after a chain id change, want a re-export", out.Cached, runs(t, bin, "export"))
	}
	if out := export(t, in); !out.Cached {
		t.Fatal("same chain id again was not served from the cache")
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	if out := export(t, in); out.Cached || runs(t, bin, "export") != 3 {
		t.Fatalf("cached=%v runs=%d after the binary changed, want a re-export", out.Cached, runs(t, bin, "export"))
	}
}

func TestDownloadSerializesConcurrentRuns(t *testing.T) {
	var requests atomic.Int32
	body := bytes.Repeat([]byte("s"), 4096)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(body)
	}))
	defer ts.Close()
	dir := t.TempDir()
	const n = 3
	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = snapshot.Download(context.Background(), nil, ts.URL+"/snap.tar.lz4", dir, nil)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("download %d: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Fatalf("download %d landed at %s, download 0 at %s", i, paths[i], paths[0])
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("server saw %d requests, want 1", requests.Load())
	}
	assertFile(t, paths[0], body)
}

func TestReachable(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.HasSuffix(r.URL.Path, "missing.tar.lz4") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-0/4096")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("s"))
	}))
	defer ts.Close()
	dir := t.TempDir()
	ctx := context.Background()
	if err := snapshot.Reachable(ctx, nil, ts.URL+"/ok.tar.lz4", dir); err != nil {
		t.Fatalf("reachable URL: %v", err)
	}
	err := snapshot.Reachable(ctx, nil, ts.URL+"/missing.tar.lz4", dir)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the 404 status", err)
	}
	cached := ts.URL + "/cached.tar.lz4"
	if err := os.WriteFile(filepath.Join(dir, snapshot.Key(cached)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := requests.Load()
	if err := snapshot.Reachable(ctx, nil, cached, dir); err != nil || requests.Load() != before {
		t.Fatalf("cached archive: err=%v requests=%d, want no request", err, requests.Load()-before)
	}
	if err := snapshot.Reachable(ctx, nil, "/some/local/file.tar.lz4", dir); err != nil {
		t.Fatalf("local file: %v", err)
	}
}
