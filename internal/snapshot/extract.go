package snapshot

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// Marker records a finished extraction in <home>/snapshot.json.
type Marker struct {
	Archive string    `json:"archive"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	SHA256  string    `json:"sha256"`
	Format  string    `json:"format"`
	// Found lists the top-level entries of the extracted data directory.
	Found       []string  `json:"found"`
	ExtractedAt time.Time `json:"extracted_at"`
}

const (
	markerFile = "snapshot.json"
	dataDir    = "data"
	partialDir = "data.partial"
)

// ReadMarker returns the marker in home, and false when there is none.
func ReadMarker(home string) (Marker, bool, error) {
	b, err := os.ReadFile(filepath.Join(home, markerFile))
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{}, false, fmt.Errorf("read %s: %w", filepath.Join(home, markerFile), err)
	}
	return m, true, nil
}

// Extract unpacks the data directory of archive into <home>/data, whatever
// the archive's layout (data/, ./data/, or one wrapper directory around
// data/). Entries outside data/ are skipped. It owns <home>/data: a rerun on
// the same archive is a no-op, and anything else found there is replaced.
func Extract(ctx context.Context, archive, home string) (Marker, error) {
	archive, err := filepath.Abs(archive)
	if err != nil {
		return Marker{}, err
	}
	info, err := os.Stat(archive)
	if err != nil {
		return Marker{}, err
	}
	final := filepath.Join(home, dataDir)
	if m, ok, err := ReadMarker(home); err == nil && ok && m.Archive == archive &&
		m.Size == info.Size() && m.ModTime.Equal(info.ModTime()) {
		if _, err := os.Stat(final); err == nil {
			return m, nil
		}
	}

	// The marker goes first so it is never left describing removed data.
	for _, p := range []string{markerFile, dataDir, partialDir} {
		if err := os.RemoveAll(filepath.Join(home, p)); err != nil {
			return Marker{}, err
		}
	}
	partial := filepath.Join(home, partialDir)
	if err := os.MkdirAll(partial, 0o755); err != nil {
		return Marker{}, err
	}

	m, err := extractArchive(ctx, archive, partial)
	if err != nil {
		// A partial extraction cannot resume, and it may be large.
		_ = os.RemoveAll(partial)
		return Marker{}, fmt.Errorf("extract %s: %w", archive, err)
	}
	if err := os.Rename(partial, final); err != nil {
		return Marker{}, err
	}
	m.Archive, m.Size, m.ModTime, m.ExtractedAt = archive, info.Size(), info.ModTime().UTC(), time.Now().UTC()
	return m, writeMarker(home, m)
}

func extractArchive(ctx context.Context, archive, dest string) (Marker, error) {
	f, err := os.Open(archive)
	if err != nil {
		return Marker{}, err
	}
	defer func() { _ = f.Close() }()

	hash := sha256.New()
	raw := bufio.NewReaderSize(io.TeeReader(f, hash), 1<<20)
	format, tarStream, err := decompress(raw)
	if err != nil {
		return Marker{}, err
	}
	defer func() { _ = tarStream.Close() }()

	root, err := os.OpenRoot(dest)
	if err != nil {
		return Marker{}, err
	}
	defer func() { _ = root.Close() }()

	found, err := unpack(ctx, tar.NewReader(tarStream), root)
	if err != nil {
		return Marker{}, err
	}
	if len(found) == 0 {
		return Marker{}, errors.New("archive has no data/ directory")
	}
	if !slices.Contains(found, "application.db") {
		return Marker{}, fmt.Errorf("archive data/ has no application.db (found %s)", strings.Join(found, ", "))
	}
	// Hash the whole file, including what trails the tar end marker.
	if _, err := io.Copy(io.Discard, raw); err != nil {
		return Marker{}, err
	}
	return Marker{SHA256: hex.EncodeToString(hash.Sum(nil)), Format: format, Found: found}, nil
}

// decompress picks the codec from the stream's magic bytes, so a URL or file
// name without a telling extension still works.
func decompress(r *bufio.Reader) (string, io.ReadCloser, error) {
	head, err := r.Peek(262)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", nil, err
	}
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(r)
		return "tar.gz", zr, err
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		// Snapshots are often compressed with --long=31. Concurrency 1 decodes
		// synchronously, so no goroutine still reads r when the caller drains it.
		zr, err := zstd.NewReader(r, zstd.WithDecoderMaxWindow(1<<31), zstd.WithDecoderLowmem(true),
			zstd.WithDecoderConcurrency(1))
		if err != nil {
			return "", nil, err
		}
		return "tar.zst", zr.IOReadCloser(), nil
	case bytes.HasPrefix(head, []byte{0x04, 0x22, 0x4d, 0x18}):
		return "tar.lz4", io.NopCloser(lz4.NewReader(r)), nil
	case len(head) == 262 && string(head[257:262]) == "ustar":
		return "tar", io.NopCloser(r), nil
	}
	return "", nil, errors.New("unknown archive format (want .tar, .tar.gz, .tar.lz4, or .tar.zst)")
}

func unpack(ctx context.Context, tr *tar.Reader, root *os.Root) ([]string, error) {
	var l layout
	top := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, ok, err := l.rel(hdr.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if rel != "." {
			top[strings.SplitN(rel, "/", 2)[0]] = true
			if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
				return nil, err
			}
		}
		if err := writeEntry(tr, hdr, rel, root, &l); err != nil {
			return nil, fmt.Errorf("%s: %w", hdr.Name, err)
		}
	}
	found := make([]string, 0, len(top))
	for name := range top {
		found = append(found, name)
	}
	slices.Sort(found)
	return found, nil
}

func writeEntry(tr *tar.Reader, hdr *tar.Header, rel string, root *os.Root, l *layout) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(rel, 0o755)
	case tar.TypeReg:
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, hdr.FileInfo().Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		return errors.Join(err, f.Close())
	case tar.TypeSymlink:
		// Checked before cleaning: "b/../x" cleans to "x", yet resolves above
		// data/ when b links to ".".
		if path.IsAbs(hdr.Linkname) || slices.Contains(strings.Split(hdr.Linkname, "/"), "..") {
			return fmt.Errorf("symlink target %q leaves its directory", hdr.Linkname)
		}
		return root.Symlink(hdr.Linkname, rel)
	case tar.TypeLink:
		old, ok, err := l.rel(hdr.Linkname)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("hard link target %q is outside data/", hdr.Linkname)
		}
		return root.Link(old, rel)
	}
	return fmt.Errorf("unsupported tar entry type %q", hdr.Typeflag)
}

// layout locates data/ inside an archive. The first entry naming data/, at
// the top or under one wrapper directory, fixes where data/ lives for the
// rest of the archive.
type layout struct {
	prefix string
	known  bool
}

// rel maps an archive path to a path inside data/. It reports false for
// entries outside data/ and fails for paths that escape the archive root.
func (l *layout) rel(name string) (string, bool, error) {
	clean := path.Clean(name)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("archive path %q escapes the extraction directory", name)
	}
	parts := strings.Split(clean, "/")
	if !l.known {
		switch {
		case parts[0] == dataDir:
			l.known = true
		case len(parts) > 1 && parts[1] == dataDir:
			l.prefix, l.known = parts[0], true
		default:
			return "", false, nil
		}
	}
	if l.prefix != "" {
		if parts[0] != l.prefix {
			return "", false, nil
		}
		parts = parts[1:]
	}
	if len(parts) == 0 || parts[0] != dataDir {
		return "", false, nil
	}
	return path.Join(append([]string{"."}, parts[1:]...)...), true, nil
}

func writeMarker(home string, m Marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, markerFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, markerFile))
}
