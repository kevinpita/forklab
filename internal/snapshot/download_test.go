package snapshot_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/snapshot"
)

var (
	payload = bytes.Repeat([]byte("0123456789abcdef"), 64<<10)
	half    = len(payload) / 2
)

// server answers its nth request with steps[n], repeating the last step, and
// records the resume headers of each request.
type server struct {
	steps []http.HandlerFunc

	mu       sync.Mutex
	requests []string
}

func newServer(t *testing.T, steps ...http.HandlerFunc) (*server, string) {
	s := &server{steps: steps}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts.URL + "/snapshots/xrp_latest.tar.lz4"
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := min(len(s.requests), len(s.steps)-1)
	s.requests = append(s.requests, strings.TrimSpace(r.Header.Get("Range")+" "+r.Header.Get("If-Range")))
	s.mu.Unlock()
	s.steps[n](w, r)
}

func (s *server) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// cut sends the first half of body, then drops the connection.
func cut(body []byte, etag string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
}

// serve serves body, honoring Range and If-Range only when ranged is set.
func serve(body []byte, etag string, ranged bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		if !ranged {
			r.Header.Del("Range")
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	}
}

// partial answers any request with n bytes of body from start.
func partial(body []byte, etag string, start, n int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+n-1, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : start+n])
	}
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("downloaded %d bytes, want the %d-byte body", len(got), len(want))
	}
	if matches, _ := filepath.Glob(path + ".part*"); len(matches) != 0 {
		t.Fatalf("left behind %v", matches)
	}
}

func assertPayload(t *testing.T, path string) {
	t.Helper()
	assertFile(t, path, payload)
}

func TestDownloadResumesInterruptedTransfer(t *testing.T) {
	rotated := bytes.Repeat([]byte("fedcba9876543210"), 70<<10)
	larger := bytes.Repeat(payload, 3)
	resume := fmt.Sprintf("bytes=%d- \"a\"", half)
	for _, tc := range []struct {
		name  string
		steps []http.HandlerFunc
		want  []byte
		log   []string
	}{
		{
			name:  "same file resumes at the cut",
			steps: []http.HandlerFunc{cut(payload, `"a"`), serve(payload, `"a"`, true)},
			want:  payload,
			log:   []string{"", resume},
		},
		{
			name:  "server ignores ranges",
			steps: []http.HandlerFunc{cut(payload, `"a"`), serve(payload, `"a"`, false)},
			want:  payload,
			log:   []string{"", resume},
		},
		{
			name:  "remote file replaced at the same URL",
			steps: []http.HandlerFunc{cut(payload, `"a"`), serve(rotated, `"b"`, true)},
			want:  rotated,
			log:   []string{"", resume},
		},
		{
			name:  "no validator restarts from zero",
			steps: []http.HandlerFunc{cut(payload, ""), serve(payload, "", true)},
			want:  payload,
			log:   []string{"", ""},
		},
		{
			name:  "remote file shrank below the part",
			steps: []http.HandlerFunc{cut(larger, `"a"`), serve(payload, `"a"`, true)},
			want:  payload,
			log:   []string{"", fmt.Sprintf("bytes=%d- \"a\"", len(larger)/2), ""},
		},
		{
			name:  "server resumes at the wrong offset",
			steps: []http.HandlerFunc{cut(payload, `"a"`), partial(payload, `"a"`, half+5, 10), serve(payload, `"a"`, true)},
			want:  payload,
			log:   []string{"", resume, ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, url := newServer(t, tc.steps...)
			dir := t.TempDir()

			_, err := snapshot.Download(context.Background(), nil, url, dir, nil)
			if err == nil || !strings.Contains(err.Error(), "rerun to resume") {
				t.Fatalf("first download: want interruption error, got %v", err)
			}

			var last [2]int64
			path, err := snapshot.Download(context.Background(), nil, url, dir, func(done, total int64) {
				last = [2]int64{done, total}
			})
			if err != nil {
				t.Fatal(err)
			}
			assertFile(t, path, tc.want)
			if got := s.log(); !slices.Equal(got, tc.log) {
				t.Fatalf("requests (Range If-Range) = %q, want %q", got, tc.log)
			}
			if n := int64(len(tc.want)); last != [2]int64{n, n} {
				t.Fatalf("last progress %v, want done == total == %d", last, n)
			}
		})
	}
}

func TestDownloadShortRangeIsResumable(t *testing.T) {
	// A server may answer a range with fewer bytes than asked for.
	s, url := newServer(t, cut(payload, `"a"`), partial(payload, `"a"`, half, 10), serve(payload, `"a"`, true))
	dir := t.TempDir()

	for _, want := range []string{"rerun to resume", fmt.Sprintf("got %d of %d", half+10, len(payload))} {
		if _, err := snapshot.Download(context.Background(), nil, url, dir, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want an error containing %q, got %v", want, err)
		}
	}
	path, err := snapshot.Download(context.Background(), nil, url, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPayload(t, path)
	if got := s.log()[2]; got != fmt.Sprintf("bytes=%d- \"a\"", half+10) {
		t.Fatalf("third request %q should resume after the short range", got)
	}
}

func TestDownloadIsCachedByURL(t *testing.T) {
	s, url := newServer(t, serve(payload, `"a"`, true))
	dir := t.TempDir()

	first, err := snapshot.Download(context.Background(), nil, url, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.Download(context.Background(), nil, url, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(s.log()) != 1 {
		t.Fatalf("rerun should reuse %s without a request; got %s after %d requests", first, second, len(s.log()))
	}
	assertPayload(t, first)

	other, err := snapshot.Download(context.Background(), nil, strings.Replace(url, "/snapshots/", "/mirror/", 1), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("different URLs with the same base name share a cache file")
	}
	if !strings.HasSuffix(other, "-xrp_latest.tar.lz4") {
		t.Fatalf("cache name %s should keep the archive name", other)
	}
}

func TestDownloadFailsFastWithoutDiskSpace(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4611686018427387904")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	dir := t.TempDir()
	url := ts.URL + "/huge.tar.lz4"

	_, err := snapshot.Download(context.Background(), nil, url, dir, nil)
	if !errors.Is(err, snapshot.ErrNoSpace) || !strings.Contains(err.Error(), "need 4.0 EiB, available") {
		t.Fatalf("want a no-space error naming needed and available bytes, got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("nothing should be written, found %v", entries)
	}
}

func TestDownloadReportsHTTPErrors(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	_, err := snapshot.Download(context.Background(), nil, ts.URL+"/missing.tar.lz4", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
}

func TestFetchUsesLocalFileInPlace(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap.tar")
	if err := os.WriteFile(file, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	got, err := snapshot.Fetch(context.Background(), nil, file, cache, nil)
	if err != nil || got != file {
		t.Fatalf("Fetch(local) = %q, %v; want %q", got, err, file)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("a local file must not be copied into the cache, found %v", entries)
	}
	if _, err := snapshot.Fetch(context.Background(), nil, file+".missing", cache, nil); err == nil {
		t.Fatal("missing local file should fail")
	}
}
