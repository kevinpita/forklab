// Package snapshot downloads chain snapshot archives into a cache and extracts
// them into a node home.
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Progress reports bytes done out of total. Total is -1 when unknown.
type Progress func(done, total int64)

// IsURL reports whether src is fetched over HTTP rather than read from disk.
func IsURL(src string) bool {
	u, err := url.Parse(src)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Key names a downloaded URL in the cache: a short hash of the URL keeps
// distinct URLs apart, and the sanitized base name keeps the file
// recognizable.
func Key(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	base := ""
	if u, err := url.Parse(rawURL); err == nil {
		base = path.Base(u.Path)
	}
	base = strings.Trim(unsafeName.ReplaceAllString(base, "_"), "._")
	if base == "" {
		base = "snapshot"
	}
	return hex.EncodeToString(sum[:4]) + "-" + base
}

// Fetch returns a local archive for src. A URL is downloaded into dir (see
// Download); anything else must be an existing file and is used in place.
func Fetch(ctx context.Context, client *http.Client, src, dir string, progress Progress) (string, error) {
	src, err := Resolve(ctx, client, src)
	if err != nil {
		return "", err
	}
	if IsURL(src) {
		return Download(ctx, client, src, dir, progress)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("snapshot file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("snapshot file %s is not a regular file", abs)
	}
	return abs, nil
}

// Reachable fails when a URL snapshot that is not yet in dir cannot be
// fetched, so a bad URL is caught before slower steps run. It asks for the
// first byte: HEAD is not supported by every snapshot host.
func Reachable(ctx context.Context, client *http.Client, src, dir string) error {
	if !IsURL(src) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, Key(src))); err == nil {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", src, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("snapshot %s: %s", src, resp.Status)
	}
	return nil
}

// Download fetches rawURL into dir/Key(rawURL) and returns that path. A
// complete file is reused as is. An interrupted download stays in a .part
// file and resumes with an HTTP Range request guarded by If-Range, so it
// restarts when the remote file changed or the server ignores ranges.
func Download(ctx context.Context, client *http.Client, rawURL, dir string, progress Progress) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(dir, Key(rawURL))
	if _, err := os.Stat(final); err == nil {
		return final, nil
	}
	// One downloader per archive; a second process waits and then finds the
	// finished file.
	unlock, err := lock(ctx, final+".lock", nil)
	if err != nil {
		return "", fmt.Errorf("lock %s: %w", final, err)
	}
	defer unlock()
	if _, err := os.Stat(final); err == nil {
		return final, nil
	}
	part := final + ".part"
	validatorFile := part + ".validator"

	// Without the validator of the file the .part came from, a resume could
	// splice two different files, so start over.
	var offset int64
	validator, _ := os.ReadFile(validatorFile)
	if info, err := os.Stat(part); err == nil && len(validator) > 0 {
		offset = info.Size()
	}
	resp, err := get(ctx, client, rawURL, offset, string(validator))
	if err == nil && offset > 0 && !resumesAt(resp, offset) {
		_ = resp.Body.Close()
		offset = 0
		resp, err = get(ctx, client, rawURL, 0, "")
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var total int64
	flags := os.O_WRONLY | os.O_CREATE
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, size, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return "", err
		}
		if start != offset {
			return "", fmt.Errorf("download %s: server resumed at byte %d, expected %d", rawURL, start, offset)
		}
		total = size
		flags |= os.O_APPEND
	case http.StatusOK:
		offset = 0
		total = resp.ContentLength
		flags |= os.O_TRUNC
		if err := saveValidator(validatorFile, resp.Header); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}

	if total >= 0 {
		if err := checkFreeSpace(dir, total-offset, ""); err != nil {
			return "", err
		}
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return "", err
	}
	w := &progressWriter{w: f, done: offset, total: total, progress: progress}
	_, copyErr := io.Copy(w, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return "", fmt.Errorf("download %s interrupted at %d bytes (rerun to resume): %w", rawURL, w.done, copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	if total >= 0 && w.done != total {
		return "", fmt.Errorf("download %s incomplete: got %d of %d bytes (rerun to resume)", rawURL, w.done, total)
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	_ = os.Remove(validatorFile)
	return final, nil
}

// resumesAt reports whether resp can continue a .part of offset bytes. A 200
// can: it replaces the .part.
func resumesAt(resp *http.Response, offset int64) bool {
	switch resp.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		return false
	case http.StatusPartialContent:
		start, _, err := parseContentRange(resp.Header.Get("Content-Range"))
		return err == nil && start == offset
	}
	return true
}

// saveValidator keeps what identifies this version of the remote file: a
// strong ETag, else Last-Modified. With neither, the .part cannot resume.
func saveValidator(file string, h http.Header) error {
	v := h.Get("ETag")
	if v == "" || strings.HasPrefix(v, "W/") {
		v = h.Get("Last-Modified")
	}
	if v == "" {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(file, []byte(v), 0o644)
}

func get(ctx context.Context, client *http.Client, rawURL string, offset int64, validator string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		req.Header.Set("If-Range", validator)
	}
	// Compressed transfer would make byte offsets and Content-Length refer to
	// the encoded stream, not the archive.
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}
	return resp, nil
}

// parseContentRange reads "bytes start-end/size". An unknown size is -1.
func parseContentRange(v string) (start, size int64, err error) {
	bad := fmt.Errorf("bad Content-Range %q", v)
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, 0, bad
	}
	span, sizeStr, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, 0, bad
	}
	startStr, _, ok := strings.Cut(span, "-")
	if !ok {
		return 0, 0, bad
	}
	if start, err = strconv.ParseInt(startStr, 10, 64); err != nil {
		return 0, 0, bad
	}
	if sizeStr == "*" {
		return start, -1, nil
	}
	if size, err = strconv.ParseInt(sizeStr, 10, 64); err != nil {
		return 0, 0, bad
	}
	return start, size, nil
}

// ErrNoSpace is returned before a download or export that cannot fit on disk.
var ErrNoSpace = errors.New("not enough disk space")

// checkFreeSpace fails with ErrNoSpace when dir has less than need bytes
// free. A non-empty basis explains how need was estimated.
func checkFreeSpace(dir string, need int64, basis string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("check free space in %s: %w", dir, err)
	}
	avail := uint64(st.Bavail) * uint64(st.Bsize)
	if need > 0 && uint64(need) > avail {
		if basis != "" {
			basis = " (" + basis + ")"
		}
		return fmt.Errorf("%w in %s: need %s%s, available %s", ErrNoSpace, dir, humanBytes(uint64(need)), basis, humanBytes(avail))
	}
	return nil
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

type progressWriter struct {
	w           io.Writer
	done, total int64
	progress    Progress
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.progress != nil {
		p.progress(p.done, p.total)
	}
	return n, err
}
