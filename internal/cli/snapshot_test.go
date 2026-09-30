package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSnapshotFetchEndsTheProgressLineBeforeTheResult(t *testing.T) {
	t.Setenv("FORKLAB_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(bytes.Repeat([]byte("s"), 4096))
	}))
	defer srv.Close()
	// One buffer for both streams, as a terminal shows them.
	var term bytes.Buffer
	if code := Run([]string{"snapshot", "fetch", srv.URL + "/snap.tar.lz4"}, &term, &term); code != 0 {
		t.Fatalf("exit %d: %s", code, term.String())
	}
	out := term.String()
	if !strings.Contains(out, "downloading 100%") || !strings.Contains(out, "MiB)\narchive ") || !strings.HasSuffix(out, "\n") || strings.Contains(out, "\n\n") {
		t.Fatalf("progress and result are not on their own lines:\n%q", out)
	}
}
