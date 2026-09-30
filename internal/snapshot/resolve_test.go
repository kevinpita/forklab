package snapshot_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kevinpita/forklab/internal/snapshot"
)

type testProvider struct {
	source, archive string
	err             error
}

func (p testProvider) Supports(source string) bool { return source == p.source }
func (p testProvider) Resolve(_ context.Context, _ string) (string, error) {
	return p.archive, p.err
}

func TestResolverSupportsAdditionalProviders(t *testing.T) {
	failure := errors.New("provider unavailable")
	r := snapshot.Resolver{Providers: []snapshot.Provider{
		snapshot.PolkachuProvider{},
		testProvider{source: "other:latest", archive: "https://other.example/snapshot.tar.zst"},
		testProvider{source: "failed:latest", err: failure},
		// Matching providers must not hide an earlier provider's error.
		testProvider{source: "failed:latest", archive: "https://fallback.example/snapshot.tar.lz4"},
	}}
	got, err := r.Resolve(t.Context(), "other:latest")
	if err != nil || got != "https://other.example/snapshot.tar.zst" {
		t.Fatalf("additional provider = %s, %v", got, err)
	}
	if _, err := r.Resolve(t.Context(), "failed:latest"); !errors.Is(err, failure) {
		t.Fatalf("provider error was hidden: %v", err)
	}
	got, err = (snapshot.Resolver{}).Resolve(t.Context(), "/tmp/local.tar.lz4")
	if err != nil || got != "/tmp/local.tar.lz4" {
		t.Fatalf("resolver without providers = %s, %v", got, err)
	}
}

type routePolkachu struct {
	server string
}

func (r routePolkachu) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.URL.Scheme = "http"
	copy.URL.Host = strings.TrimPrefix(r.server, "http://")
	return http.DefaultTransport.RoundTrip(copy)
}

func TestResolvePolkachu(t *testing.T) {
	const page = "https://polkachu.com/tendermint_snapshots/xrp"
	const archive = "https://snapshots.polkachu.com/snapshots/xrp/xrp_7925443.tar.lz4"
	for _, tc := range []struct {
		name, body, wantErr string
		status              int
	}{
		{"published link", `<a href="` + archive + `">Download</a>`, "", http.StatusOK},
		{"single quotes", `<a href = '` + archive + `'>Download</a>`, "", http.StatusOK},
		{"wrong chain", `<a href="https://snapshots.polkachu.com/snapshots/osmosis/osmosis_42.tar.lz4">Download</a>`, "no xrp archive link", http.StatusOK},
		{"no snapshot", `<p>No snapshots available</p>`, "no xrp archive link", http.StatusOK},
		{"untrusted host", `<a href="https://other.example/snapshots/xrp/xrp_42.tar.lz4">Download</a>`, "no xrp archive link", http.StatusOK},
		{"missing page", "", "404 Not Found", http.StatusNotFound},
		{"oversized page", strings.Repeat("x", (2<<20)+1), "exceeds 2 MiB", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/tendermint_snapshots/xrp" {
					t.Errorf("request path = %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			client := &http.Client{Transport: routePolkachu{server: srv.URL}}
			got, err := snapshot.Resolve(t.Context(), client, page)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Resolve = %q, %v; want %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != archive {
				t.Fatalf("Resolve = %q, %v; want %q", got, err, archive)
			}
		})
	}
}

func TestResolveDirectSourcesDoNotFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, src := range []string{
		"/tmp/snapshot.tar.lz4",
		"https://snapshots.polkachu.com/snapshots/xrp/xrp_42.tar.lz4",
		"https://other.example/snapshot.tar.zst",
		"https://polkachu.com/genesis/xrp",
	} {
		got, err := snapshot.Resolve(ctx, nil, src)
		if err != nil || got != src {
			t.Errorf("Resolve(%q) = %q, %v", src, got, err)
		}
	}
	if _, err := snapshot.Resolve(ctx, nil, "https://polkachu.com/tendermint_snapshots/xrp"); err == nil {
		t.Fatal("canceled page lookup succeeded")
	}
}

func TestFetchPolkachuCachesDatedArchives(t *testing.T) {
	var height atomic.Int64
	height.Store(42)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tendermint_snapshots/xrp" {
			_, _ = fmt.Fprintf(w, `<a href="https://snapshots.polkachu.com/snapshots/xrp/xrp_%d.tar.lz4">Download</a>`, height.Load())
			return
		}
		_, _ = fmt.Fprint(w, r.URL.Path)
	}))
	defer srv.Close()
	client := &http.Client{Transport: routePolkachu{server: srv.URL}}
	dir := t.TempDir()
	const page = "https://polkachu.com/tendermint_snapshots/xrp"
	first, err := snapshot.Fetch(t.Context(), client, page, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	height.Store(43)
	second, err := snapshot.Fetch(t.Context(), client, page, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("new daily snapshot reused the previous archive")
	}
	for file, want := range map[string]string{first: "/snapshots/xrp/xrp_42.tar.lz4", second: "/snapshots/xrp/xrp_43.tar.lz4"} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != want {
			t.Errorf("archive %s = %q, %v; want %q", file, data, err, want)
		}
	}
}
