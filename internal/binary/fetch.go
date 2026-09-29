package binary

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pierrec/lz4/v4"
)

// httpClient bounds connecting and waiting for response headers but not the
// body, since release downloads can take minutes.
var httpClient = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: t}
}()

// fetch downloads rawURL into stage and leaves the binary named name at dst.
func fetch(ctx context.Context, rawURL, name, stage, dst string, progress func(done, total int64)) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	download := filepath.Join(stage, ".download")
	if err := get(ctx, rawURL, download, progress); err != nil {
		return err
	}
	base := strings.ToLower(path.Base(u.Path))
	switch {
	case strings.HasSuffix(base, ".tar.gz"), strings.HasSuffix(base, ".tgz"):
		err = extractTar(download, name, dst, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) })
	case strings.HasSuffix(base, ".tar.lz4"):
		err = extractTar(download, name, dst, func(r io.Reader) (io.Reader, error) { return lz4.NewReader(r), nil })
	case strings.HasSuffix(base, ".zip"):
		err = extractZip(download, name, dst)
	default:
		err = os.Rename(download, dst)
	}
	if err != nil {
		return err
	}
	if err := os.RemoveAll(download); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755)
}

func get(ctx context.Context, rawURL, dst string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	var body io.Reader = resp.Body
	if progress != nil {
		body = &progressReader{r: resp.Body, total: resp.ContentLength, fn: progress}
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		return fmt.Errorf("GET %s: %w", rawURL, err)
	}
	return f.Close()
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	fn    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	p.fn(p.done, p.total)
	return n, err
}

var errNotInArchive = errors.New("not found in archive")

func extractTar(archive, name, dst string, decompress func(io.Reader) (io.Reader, error)) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r, err := decompress(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	found := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != name {
			continue
		}
		if found != "" {
			return fmt.Errorf("archive has %s at both %s and %s", name, found, h.Name)
		}
		found = h.Name
		if err := writeFile(dst, tr); err != nil {
			return err
		}
	}
	if found == "" {
		return fmt.Errorf("%s: %w", name, errNotInArchive)
	}
	return nil
}

func extractZip(archive, name, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	var match *zip.File
	for _, zf := range zr.File {
		if !zf.Mode().IsRegular() || path.Base(zf.Name) != name {
			continue
		}
		if match != nil {
			return fmt.Errorf("archive has %s at both %s and %s", name, match.Name, zf.Name)
		}
		match = zf
	}
	if match == nil {
		return fmt.Errorf("%s: %w", name, errNotInArchive)
	}
	r, err := match.Open()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return writeFile(dst, r)
}

func writeFile(dst string, r io.Reader) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
