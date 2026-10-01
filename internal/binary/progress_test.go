package binary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/progress"
)

func TestDownloadProgressReportsRealBytesAndCacheReuse(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known", false: "unknown"}[known], func(t *testing.T) {
			body := script("1.2.3")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !known {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			p := profile.Profile{Name: "example", BinaryName: "example", Binaries: map[string]profile.Source{"1.2.3": profile.URLSource{URL: profile.Template(srv.URL)}}}
			c := Cache{Dir: t.TempDir()}
			var events []progress.Event
			o := Options{Reporter: func(e progress.Event) { events = append(events, e) }}
			if _, err := c.Resolve(context.Background(), p, "1.2.3", o); err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(events, func(e progress.Event) bool { return e.Unit == "bytes" && e.Done == int64(len(body)) })
			if i < 0 {
				t.Fatalf("no final byte count: %+v", events)
			}
			e := events[i]
			if (e.Total != nil) != known || (known && *e.Total != int64(len(body))) {
				t.Fatalf("total: %+v", e)
			}
			events = nil
			if _, err := c.Resolve(context.Background(), p, "1.2.3", o); err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[1].State != progress.Completed || events[1].Message != "Reused cached binary" {
				t.Fatalf("cache invented work: %+v", events)
			}
		})
	}
}

func TestFailedDownloadNeverReportsSuccessOrVerification(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	p := profile.Profile{Name: "example", BinaryName: "example", Binaries: map[string]profile.Source{"1": profile.URLSource{URL: profile.Template(srv.URL)}}}
	var events []progress.Event
	_, err := (Cache{Dir: t.TempDir()}).Resolve(context.Background(), p, "1", Options{Reporter: func(e progress.Event) { events = append(events, e) }})
	if err == nil {
		t.Fatal("download succeeded")
	}
	for _, e := range events {
		if e.Phase == "binary.verify" || (e.Phase == "binary.download" && e.State == progress.Completed) {
			t.Fatalf("false success: %+v", events)
		}
	}
}
