package snapshot_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/snapshot"
)

// fakeChain logs each init and export run to a counter file next to itself.
// Export refuses to run without extracted data and prints its args as JSON.
const fakeChain = `#!/bin/sh
dir=$(dirname "$0")
case "$1" in
init)
	echo run >> "$dir/init.count"
	mkdir -p "$4/config" && printf 'moniker = "%s"\nchain-id = "%s"\n' "$2" "$6" > "$4/config/config.toml"
	echo "initialized $4"
	;;
export)
	echo run >> "$dir/export.count"
	test -d "$3/data/application.db" || { echo "no application.db" >&2; exit 1; }
	shift 3
	echo "exporting at height 42" >&2
	echo "{\"args\":\"$*\"}"
	;;
esac
`

const failingChain = `#!/bin/sh
case "$1" in
init) mkdir -p "$4/config" && touch "$4/config/config.toml" ;;
export)
	echo "starting export" >&2
	echo "partial genesis"
	echo "panic: store is corrupt" >&2
	echo >&2
	echo "goroutine 1 [running]:" >&2
	echo "main.main()" >&2
	exit 1
	;;
esac
`

func fakeBinary(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "chaind")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func runs(t *testing.T, bin, step string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(bin), step+".count"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func exportInput(t *testing.T, bin string) snapshot.ExportInput {
	t.Helper()
	return snapshot.ExportInput{
		Archive: writeArchive(t, "tar.gz", chainData("")),
		Binary:  bin,
		Version: "v1.2.3+dirty",
		ChainID: "lab-1",
		Args:    []string{"--height", "42"},
		WorkDir: filepath.Join(t.TempDir(), "work"),
	}
}

func export(t *testing.T, in snapshot.ExportInput) snapshot.Exported {
	t.Helper()
	got, err := snapshot.Export(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestExportRunsOnceAndReusesTheExport(t *testing.T) {
	bin := fakeBinary(t, fakeChain)
	in := exportInput(t, bin)
	var log bytes.Buffer
	in.Log = &log
	home := filepath.Join(in.WorkDir, "home")

	first := export(t, in)
	wantPath := filepath.Join(in.WorkDir, "exported-v1.2.3_dirty.json")
	if first.Path != wantPath || first.Cached {
		t.Fatalf("first export = %+v, want fresh export at %s", first, wantPath)
	}
	if got := readFile(t, first.Path); got != "{\"args\":\"--height 42\"}\n" {
		t.Fatalf("exported file = %q, want the export stdout", got)
	}
	if got := readFile(t, first.Log); !strings.Contains(got, "exporting at height 42") {
		t.Fatalf("export log = %q, want the export stderr", got)
	}
	for _, want := range []string{
		"init scratch home " + home,
		"extract " + in.Archive + " into " + filepath.Join(home, "data"),
		"export with " + bin + " (log: " + first.Log + ")",
		"removed " + filepath.Join(home, "data"),
	} {
		if !strings.Contains(log.String(), want+"\n") {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
	if exists(t, filepath.Join(home, "data")) || exists(t, filepath.Join(home, "snapshot.json")) {
		t.Fatal("extracted data or marker left behind without KeepWork")
	}

	log.Reset()
	second := export(t, in)
	if second.Path != first.Path || !second.Cached || second.Log != "" {
		t.Fatalf("second export = %+v, want cached %s", second, first.Path)
	}
	if log.Len() != 0 {
		t.Fatalf("cached export logged steps:\n%s", log.String())
	}
	if exists(t, filepath.Join(home, "data")) {
		t.Fatal("cached export re-extracted the archive")
	}
	if n := runs(t, bin, "export"); n != 1 {
		t.Fatalf("export ran %d times, want 1", n)
	}
}

func TestExportKeepWorkKeepsExtractedData(t *testing.T) {
	in := exportInput(t, fakeBinary(t, fakeChain))
	in.KeepWork = true
	export(t, in)
	home := filepath.Join(in.WorkDir, "home")
	if !exists(t, filepath.Join(home, "data", "application.db")) {
		t.Fatal("KeepWork removed the extracted data")
	}
	if _, ok, err := snapshot.ReadMarker(home); err != nil || !ok {
		t.Fatalf("KeepWork lost the extraction marker: ok=%v err=%v", ok, err)
	}
}

func TestExportReusesInitializedHome(t *testing.T) {
	bin := fakeBinary(t, fakeChain)
	in := exportInput(t, bin)
	export(t, in)
	in.Version = "v2"
	export(t, in)
	if n := runs(t, bin, "init"); n != 1 {
		t.Fatalf("init ran %d times, want 1", n)
	}
	config := filepath.Join(in.WorkDir, "home", "config", "config.toml")
	if got := readFile(t, config); got != "moniker = \"forklab-export\"\nchain-id = \"lab-1\"\n" {
		t.Fatalf("config.toml = %q, want the one init wrote with the lab chain id", got)
	}
}

func TestExportPerVersion(t *testing.T) {
	bin := fakeBinary(t, fakeChain)
	in := exportInput(t, bin)
	v1 := export(t, in)
	in.Version = "v2.0.0"
	v2 := export(t, in)
	if v2.Cached || v2.Path == v1.Path {
		t.Fatalf("new version export = %+v, want a fresh export next to %s", v2, v1.Path)
	}
	if n := runs(t, bin, "export"); n != 2 {
		t.Fatalf("export ran %d times, want 2", n)
	}
	for _, p := range []string{v1.Path, v2.Path} {
		if !exists(t, p) {
			t.Fatalf("%s missing, want both exports kept", p)
		}
	}
	in.Version = "v1.2.3+dirty"
	if again := export(t, in); !again.Cached || again.Path != v1.Path {
		t.Fatalf("first version export = %+v, want cached %s", again, v1.Path)
	}
}

func TestExportRerunsWhenArchiveChanges(t *testing.T) {
	bin := fakeBinary(t, fakeChain)
	in := exportInput(t, bin)
	export(t, in)

	changed := append(chainData(""), entry{name: "data/extra", body: "new", typ: tar.TypeReg})
	if err := os.WriteFile(in.Archive, compress(t, "tar.gz", tarball(t, changed)), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(in.Archive, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	got := export(t, in)
	if got.Cached {
		t.Fatal("export reused a cache made from a different archive")
	}
	if n := runs(t, bin, "export"); n != 2 {
		t.Fatalf("export ran %d times, want 2", n)
	}
}

func TestExportFailure(t *testing.T) {
	in := exportInput(t, fakeBinary(t, failingChain))
	_, err := snapshot.Export(context.Background(), in)
	if err == nil {
		t.Fatal("Export succeeded with a failing export command")
	}
	logPath := filepath.Join(in.WorkDir, "export-v1.2.3_dirty.log")
	for _, want := range []string{"export:", "panic: store is corrupt", "(log: " + logPath + ")"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "main.main") {
		t.Errorf("error %q quotes a goroutine frame instead of the panic line", err)
	}
	out := filepath.Join(in.WorkDir, "exported-v1.2.3_dirty.json")
	if exists(t, out) || exists(t, out+".partial") {
		t.Fatal("failed export left an export file behind")
	}
}
