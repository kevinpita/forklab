// Package binary resolves a profile's binary version to an executable on
// disk, downloading or building it into a cache when needed.
//
// The cache holds <dir>/<profile>/<version-source-digest>/ with the binary and a meta.json
// describing it. A version directory is staged next to its final place and
// renamed in whole, so it either exists complete or not at all.
package binary

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kevinpita/forklab/internal/paths"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/progress"
)

type Kind string

const (
	KindURL  Kind = "url"
	KindPath Kind = "path"
	KindGit  Kind = "git"
	KindSrc  Kind = "src"
)

// Check is the outcome of comparing `<bin> version` with the requested version.
type Check string

const (
	CheckMatched Check = "matched"
	// CheckUnknown means the binary printed no version, as git-tag simd
	// builds do. The binary is accepted.
	CheckUnknown Check = "unknown"
	// CheckSkipped is for src builds, whose version is only recorded, and
	// for --no-verify when `version` itself failed.
	CheckSkipped Check = "skipped"
	// CheckMismatch is only ever stored under --no-verify.
	CheckMismatch Check = "mismatch"
)

// ErrVersionMismatch means the binary reports a version other than the
// requested one.
var ErrVersionMismatch = errors.New("version mismatch")

// Binary is a resolved binary and the meta.json stored next to it.
type Binary struct {
	Profile string `json:"profile"`
	Version string `json:"version"`
	Kind    Kind   `json:"kind"`
	// Source is the expanded URL, path, repo@ref, or checkout directory.
	Source string `json:"source"`
	// Path is the absolute path of the executable.
	Path            string `json:"path"`
	ReportedVersion string `json:"reported_version"`
	Check           Check  `json:"version_check"`
	// Size and ModTime fingerprint the file at Path when it was checked. A
	// cached entry is reused only while the file still matches.
	Size    int64 `json:"size"`
	ModTime int64 `json:"mtime_unix_nano"`
}

type Options struct {
	Reporter progress.Reporter
	// NoVerify accepts a binary whose reported version differs. The
	// acceptance is stored, so later calls without NoVerify reuse the binary
	// until its file changes; this holds for path sources too.
	NoVerify bool
	// Rebuild ignores a cached git or src build and builds again.
	Rebuild bool
	// Progress receives downloaded and total bytes; total is -1 when unknown.
	Progress func(done, total int64)
	// BuildLog receives build command output. Nil discards it.
	BuildLog io.Writer
}

// Cache is the binary cache rooted at Dir, normally $FORKLAB_HOME/bin.
type Cache struct {
	Dir string
}

const metaFile = "meta.json"

// Resolve returns the binary for version of p, fetching or building it into
// the cache on first use. Calls for the same profile, version, and source configuration,
// in any process, run one at a time; later calls find the cached result.
func (c Cache) Resolve(ctx context.Context, p profile.Profile, version string, o Options) (Binary, error) {
	o.Reporter.Emit("binary.resolve", "Checking binary cache", progress.Started)
	src, ok := p.Binaries[version]
	if !ok {
		return Binary{}, fmt.Errorf("profile %s has no binary version %s", p.Name, version)
	}
	vars := profile.Vars{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, ChainID: p.ChainID}
	switch s := src.(type) {
	case profile.PathSource:
		path, err := paths.UserPath(s.Path.Expand(vars))
		if err != nil {
			return Binary{}, err
		}
		src = profile.PathSource{Path: profile.Template(path)}
	case profile.SrcSource:
		dir, err := paths.UserPath(s.Dir.Expand(vars))
		if err != nil {
			return Binary{}, err
		}
		s.Dir = profile.Template(dir)
		src = s
	}
	b := Binary{Profile: p.Name, Version: version}
	b.Kind, b.Source = describe(src, vars)

	parent := filepath.Join(c.Dir, p.Name)
	slot := sourceSlot(p, version, src, vars)
	dir := filepath.Join(parent, slot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Binary{}, err
	}
	unlock, err := lock(ctx, filepath.Join(parent, "."+slot+".lock"))
	if err != nil {
		return Binary{}, err
	}
	defer unlock()

	// Under the lock no other resolve owns these, so any that exist were
	// left by a crash.
	stage, work, old := filepath.Join(parent, ".stage-"+slot), filepath.Join(parent, ".work-"+slot), filepath.Join(parent, ".old-"+slot)
	for _, d := range []string{stage, work, old} {
		if err := os.RemoveAll(d); err != nil {
			return Binary{}, err
		}
	}

	if !o.Rebuild {
		if cached, err := readMeta(dir); err == nil && cached.Kind == b.Kind && cached.Source == b.Source && unchanged(cached) {
			o.Reporter.Emit("binary.resolve", "Reused cached binary", progress.Completed)
			return cached, nil
		}
	}

	o.Reporter.Emit("binary.resolve", "Binary source resolved", progress.Completed)
	if err := os.Mkdir(stage, 0o755); err != nil {
		return Binary{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()

	staged := filepath.Join(stage, p.BinaryName)
	b.Path = filepath.Join(dir, p.BinaryName)
	switch s := src.(type) {
	case profile.URLSource:
		o.Reporter.Emit("binary.download", "Downloading binary", progress.Started)
		previous := o.Progress
		err = fetch(ctx, b.Source, p.BinaryName, stage, staged, func(done, total int64) {
			if previous != nil {
				previous(done, total)
			}
			o.Reporter.Bytes("binary.download", "Downloading binary", done, total)
		})
		if err == nil {
			o.Reporter.Emit("binary.download", "Binary downloaded", progress.Completed)
		}
	case profile.PathSource:
		staged, err = filepath.Abs(b.Source)
		b.Path = staged
	case profile.GitSource:
		err = buildGit(ctx, s, vars, work, staged, o.BuildLog, o.Reporter)
	case profile.SrcSource:
		o.Reporter.Emit("binary.build", "Building local binary", progress.Started)
		err = c.buildSrc(ctx, s, vars, staged, o.BuildLog)
		if err == nil {
			o.Reporter.Emit("binary.build", "Local binary built", progress.Completed)
		}
	}
	if err == nil {
		err = isExecutable(staged)
	}
	if err != nil {
		return Binary{}, fmt.Errorf("%s %s %s: %w", p.Name, version, b.Kind, err)
	}

	o.Reporter.Emit("binary.verify", "Verifying binary", progress.Started)
	b.ReportedVersion, b.Check, err = verify(ctx, staged, p.BinaryName, version, b.Kind, o.NoVerify)
	if err != nil {
		return Binary{}, fmt.Errorf("%s %s: %w", p.Name, version, err)
	}
	fi, err := os.Stat(staged)
	if err != nil {
		return Binary{}, err
	}
	b.Size, b.ModTime = fi.Size(), fi.ModTime().UnixNano()
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return Binary{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, metaFile), append(data, '\n'), 0o644); err != nil {
		return Binary{}, err
	}
	if err := commit(stage, dir, old); err != nil {
		return Binary{}, err
	}
	o.Reporter.Emit("binary.verify", "Binary verified and ready", progress.Completed)
	return b, nil
}

func sourceSlot(p profile.Profile, version string, src profile.Source, vars profile.Vars) string {
	concrete := profile.BinaryDocument{}
	switch s := src.(type) {
	case profile.URLSource:
		concrete.URL = s.URL.Expand(vars)
	case profile.PathSource:
		concrete.Path = s.Path.Expand(vars)
	case profile.GitSource:
		concrete.Git, concrete.Ref, concrete.Build, concrete.Out, concrete.Env = s.Repo.Expand(vars), s.Ref.Expand(vars), s.Build, s.Out.Expand(vars), s.Env
	case profile.SrcSource:
		concrete.Src, concrete.Build, concrete.Out, concrete.Env = s.Dir.Expand(vars), s.Build, s.Out.Expand(vars), s.Env
	}
	data, _ := json.Marshal(struct {
		BinaryName string
		Vars       profile.Vars
		Source     profile.BinaryDocument
	}{p.BinaryName, vars, concrete})
	return fmt.Sprintf("%s-%x", version, sha256.Sum256(data))
}

// lock takes an exclusive flock on path. It polls so that cancelling ctx
// ends the wait for another process.
func lock(ctx context.Context, path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// commit replaces dir with stage. A crash between the renames leaves no
// dir, which the next Resolve treats as not cached.
func commit(stage, dir, old string) error {
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, dir); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

func unchanged(b Binary) bool {
	fi, err := os.Stat(b.Path)
	return err == nil && isExecutable(b.Path) == nil && fi.Size() == b.Size && fi.ModTime().UnixNano() == b.ModTime
}

func describe(src profile.Source, v profile.Vars) (Kind, string) {
	switch s := src.(type) {
	case profile.URLSource:
		return KindURL, s.URL.Expand(v)
	case profile.PathSource:
		return KindPath, s.Path.Expand(v)
	case profile.GitSource:
		return KindGit, s.Repo.Expand(v) + "@" + s.Ref.Expand(v)
	case profile.SrcSource:
		return KindSrc, s.Dir.Expand(v)
	}
	panic(fmt.Sprintf("unknown source %T", src))
}

func readMeta(dir string) (Binary, error) {
	var b Binary
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(data, &b)
}

func isExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}

// List returns every cached binary sorted by profile and version.
func (c Cache) List() ([]Binary, error) {
	metas, err := filepath.Glob(filepath.Join(c.Dir, "*", "*", metaFile))
	if err != nil {
		return nil, err
	}
	out := []Binary{}
	for _, m := range metas {
		if strings.HasPrefix(filepath.Base(filepath.Dir(m)), ".") {
			continue
		}
		b, err := readMeta(filepath.Dir(m))
		// A concurrent delete can remove the entry between the glob and the read.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b Binary) int {
		return strings.Compare(a.Profile+"\x00"+a.Version, b.Profile+"\x00"+b.Version)
	})
	return out, nil
}

// verify runs `<bin> version` and compares its first non-empty output line
// with version, ignoring a leading v on either side. name is the binary's
// name for errors.
func verify(ctx context.Context, bin, name, version string, kind Kind, noVerify bool) (string, Check, error) {
	reported, err := reportedVersion(ctx, bin, name)
	switch {
	case err != nil && noVerify:
		return "", CheckSkipped, nil
	case err != nil:
		return "", "", err
	case kind == KindSrc:
		return reported, CheckSkipped, nil
	case reported == "":
		return "", CheckUnknown, nil
	case strings.TrimPrefix(reported, "v") == strings.TrimPrefix(version, "v"):
		return reported, CheckMatched, nil
	case noVerify:
		return reported, CheckMismatch, nil
	}
	return "", "", fmt.Errorf("%w: %s reports %s, want %s", ErrVersionMismatch, name, reported, version)
}

// reportedVersion reads stdout, falling back to stderr only when stdout is
// empty, since some binaries print the version to stderr.
func reportedVersion(ctx context.Context, bin, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := command(ctx, bin, "version")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s version: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	if v := firstLine(out); v != "" {
		return v, nil
	}
	return firstLine(stderr.Bytes()), nil
}

func firstLine(b []byte) string {
	for line := range strings.Lines(string(b)) {
		if v := strings.TrimSpace(line); v != "" {
			return v
		}
	}
	return ""
}

// command runs in its own process group, and cancelling ctx kills the whole
// group so build tools that fork do not outlive forklab.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
