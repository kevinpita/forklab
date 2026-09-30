package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
)

func TestForkSourceResolvesSnapshotNamesURLsAndFiles(t *testing.T) {
	p := profile.Profile{Name: "demo", Snapshots: map[string]profile.Template{"polkachu": "https://snap.example/{version}/latest.tar.lz4"}}
	vars := profile.Vars{Version: "1.2.3"}
	file := filepath.Join(t.TempDir(), "local.tar.lz4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for arg, want := range map[string]string{
		"polkachu":                        "https://snap.example/1.2.3/latest.tar.lz4",
		"https://other.example/s.tar.zst": "https://other.example/s.tar.zst",
		file:                              file,
	} {
		got, err := forkSource(p, arg, vars)
		if err != nil || got != want {
			t.Errorf("forkSource(%s) = %q, %v; want %q", arg, got, err, want)
		}
	}
	_, err := forkSource(p, "missing.tar.lz4", vars)
	if err == nil || !strings.Contains(err.Error(), "snapshots: polkachu") || !strings.Contains(err.Error(), "missing.tar.lz4") {
		t.Fatalf("err = %v, want the argument and the profile's snapshot names", err)
	}
	_, err = forkSource(profile.Profile{Name: "bare"}, "missing.tar.lz4", vars)
	if err == nil || !strings.Contains(err.Error(), "profile bare has no snapshots") {
		t.Fatalf("err = %v, want a note that the profile has no snapshots", err)
	}
}
