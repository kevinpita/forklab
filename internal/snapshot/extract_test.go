package snapshot_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/snapshot"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

type entry struct {
	name string
	body string
	typ  byte
	link string
}

func tarball(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.link, Mode: 0o644, ModTime: time.Unix(0, 0)}
		switch e.typ {
		case tar.TypeDir:
			hdr.Mode = 0o755
		case tar.TypeReg:
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func compress(t *testing.T, format string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var err error
	switch format {
	case "tar":
		return data
	case "tar.gz":
		w := gzip.NewWriter(&buf)
		_, err = w.Write(data)
		err = errors.Join(err, w.Close())
	case "tar.zst":
		w, _ := zstd.NewWriter(&buf)
		_, err = w.Write(data)
		err = errors.Join(err, w.Close())
	case "tar.lz4":
		w := lz4.NewWriter(&buf)
		_, err = w.Write(data)
		err = errors.Join(err, w.Close())
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeArchive(t *testing.T, format string, entries []entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snap."+format)
	if err := os.WriteFile(path, compress(t, format, tarball(t, entries)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// chainData is a node data dir under prefix, with neighbors a real snapshot
// may carry next to data/.
func chainData(prefix string) []entry {
	return []entry{
		{name: prefix + "data/", typ: tar.TypeDir},
		{name: prefix + "data/application.db/", typ: tar.TypeDir},
		{name: prefix + "data/application.db/000001.log", body: "app", typ: tar.TypeReg},
		{name: prefix + "data/blockstore.db/CURRENT", body: "blocks", typ: tar.TypeReg},
		{name: prefix + "data/state.db/CURRENT", body: "state", typ: tar.TypeReg},
		{name: prefix + "data/priv_validator_state.json", body: `{"height":"0"}`, typ: tar.TypeReg},
		{name: prefix + "data/latest", typ: tar.TypeSymlink, link: "application.db/000001.log"},
		{name: prefix + "data/self", typ: tar.TypeSymlink, link: "."},
		{name: prefix + "wasm/code", body: "wasm", typ: tar.TypeReg},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExtractNormalizesLayouts(t *testing.T) {
	formats := []string{"tar", "tar.gz", "tar.zst", "tar.lz4"}
	layouts := map[string][]entry{
		"data at top":     chainData(""),
		"dot slash":       chainData("./"),
		"wrapper dir":     append([]entry{{name: "xrp_123/", typ: tar.TypeDir}}, chainData("xrp_123/")...),
		"data after junk": append([]entry{{name: "README", body: "hi", typ: tar.TypeReg}}, chainData("")...),
	}
	for _, format := range formats {
		for name, entries := range layouts {
			t.Run(format+"/"+name, func(t *testing.T) {
				archive := writeArchive(t, format, entries)
				home := t.TempDir()

				m, err := snapshot.Extract(context.Background(), archive, home)
				if err != nil {
					t.Fatal(err)
				}
				data := filepath.Join(home, "data")
				for file, want := range map[string]string{
					"application.db/000001.log": "app",
					"blockstore.db/CURRENT":     "blocks",
					"state.db/CURRENT":          "state",
					"priv_validator_state.json": `{"height":"0"}`,
					"latest":                    "app",
				} {
					if got := readFile(t, filepath.Join(data, file)); got != want {
						t.Errorf("data/%s = %q, want %q", file, got, want)
					}
				}
				wantFound := []string{"application.db", "blockstore.db", "latest", "priv_validator_state.json", "self", "state.db"}
				if !slices.Equal(m.Found, wantFound) {
					t.Errorf("Found = %v, want %v", m.Found, wantFound)
				}
				if m.Format != format {
					t.Errorf("Format = %q, want %q", m.Format, format)
				}
				sum := sha256.Sum256([]byte(readFile(t, archive)))
				if m.SHA256 != hex.EncodeToString(sum[:]) {
					t.Errorf("SHA256 = %s, want the archive's sha256", m.SHA256)
				}
				stored, ok, err := snapshot.ReadMarker(home)
				if err != nil || !ok || stored.SHA256 != m.SHA256 || stored.Archive != archive {
					t.Errorf("ReadMarker = %+v, %v, %v; want the returned marker", stored, ok, err)
				}
				homeEntries, _ := os.ReadDir(home)
				var names []string
				for _, e := range homeEntries {
					names = append(names, e.Name())
				}
				if !slices.Equal(names, []string{"data", "snapshot.json"}) {
					t.Errorf("home holds %v, want only data and snapshot.json", names)
				}
			})
		}
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	for name, bad := range map[string]entry{
		"parent path":       {name: "../evil", body: "x", typ: tar.TypeReg},
		"nested parent":     {name: "data/../../evil", body: "x", typ: tar.TypeReg},
		"absolute path":     {name: "/tmp/evil", body: "x", typ: tar.TypeReg},
		"symlink upward":    {name: "data/evil", typ: tar.TypeSymlink, link: "../../evil"},
		"symlink sideways":  {name: "data/evil", typ: tar.TypeSymlink, link: "state.db/../../evil"},
		"symlink absolute":  {name: "data/evil", typ: tar.TypeSymlink, link: "/etc/passwd"},
		"symlink via self":  {name: "data/evil", typ: tar.TypeSymlink, link: "self/../../evil"},
		"hard link outside": {name: "data/evil", typ: tar.TypeLink, link: "../outside"},
	} {
		t.Run(name, func(t *testing.T) {
			archive := writeArchive(t, "tar", append(chainData(""), bad))
			parent := t.TempDir()
			home := filepath.Join(parent, "home")

			_, err := snapshot.Extract(context.Background(), archive, home)
			if err == nil {
				t.Fatal("want an error for an escaping entry")
			}
			for _, dir := range []string{"data", "data.partial"} {
				if _, err := os.Stat(filepath.Join(home, dir)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s must not remain after a rejected archive: %v", dir, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(parent, "evil")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("an entry escaped the home: %v", err)
			}
		})
	}
}

func TestExtractRequiresApplicationDB(t *testing.T) {
	for name, tc := range map[string]struct {
		entries []entry
		want    string
	}{
		"no data dir": {
			entries: []entry{{name: "config/app.toml", body: "x", typ: tar.TypeReg}},
			want:    "no data/ directory",
		},
		"data without application.db": {
			entries: []entry{{name: "data/state.db/CURRENT", body: "x", typ: tar.TypeReg}},
			want:    "no application.db (found state.db)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			_, err := snapshot.Extract(context.Background(), writeArchive(t, "tar.gz", tc.entries), home)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(filepath.Join(home, "data")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("data/ must not appear after a failed extraction: %v", err)
			}
		})
	}
}

func TestExtractRejectsUnknownFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.tar.lz4")
	if err := os.WriteFile(path, []byte("not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Extract(context.Background(), path, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "unknown archive format") {
		t.Fatalf("want unknown format error, got %v", err)
	}
}

func TestExtractIsIdempotent(t *testing.T) {
	archive := writeArchive(t, "tar.lz4", chainData(""))
	home := t.TempDir()
	sentinel := filepath.Join(home, "data", "sentinel")

	first, err := snapshot.Extract(context.Background(), archive, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.Extract(context.Background(), archive, home)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ExtractedAt.Equal(first.ExtractedAt) {
		t.Error("rerun on the same archive should return the existing marker")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Error("rerun on the same archive should not touch data/")
	}

	if err := os.RemoveAll(filepath.Join(home, "data")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Extract(context.Background(), archive, home); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, "data", "state.db", "CURRENT")); got != "state" {
		t.Errorf("a marker without data/ should re-extract, got state.db/CURRENT %q", got)
	}

	// A changed archive replaces data/ wholesale.
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(archive, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Extract(context.Background(), archive, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Error("a changed archive should re-extract into a fresh data/")
	}
}

func TestExtractRecoversFromCrash(t *testing.T) {
	archive := writeArchive(t, "tar.zst", chainData(""))
	home := t.TempDir()
	// A crash mid-extract leaves data.partial; one between rename and marker
	// write leaves data/ without snapshot.json.
	for _, dir := range []string{"data.partial/application.db", "data/half"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := snapshot.Extract(context.Background(), archive, home); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"data.partial", "data/half"} {
		if _, err := os.Stat(filepath.Join(home, stale)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived re-extraction: %v", stale, err)
		}
	}
	if got := readFile(t, filepath.Join(home, "data", "application.db", "000001.log")); got != "app" {
		t.Errorf("application.db content %q, want app", got)
	}
}

func TestExtractHashesWholeArchive(t *testing.T) {
	// Tools pad tarballs past the end-of-archive marker; the hash must cover
	// the padding the tar reader never asks for.
	data := append(tarball(t, chainData("")), make([]byte, 3<<20)...)
	archive := filepath.Join(t.TempDir(), "snap.tar")
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := snapshot.Extract(context.Background(), archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if m.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 = %s, want %s", m.SHA256, hex.EncodeToString(sum[:]))
	}
}
