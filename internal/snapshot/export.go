package snapshot

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ExportInput drives Export.
type ExportInput struct {
	// Archive is the snapshot archive, as returned by Fetch.
	Archive string
	// Binary is the chain binary that runs init and export.
	Binary string
	// Version is the binary version; the export cache is keyed by it and the archive.
	Version string
	// ChainID is written into the scratch home by init. The export takes
	// chain_id from there, not from the state database.
	ChainID string
	// Args are the expanded profile export args, appended to `<bin> export --home <scratch>`.
	Args []string
	// WorkDir is the scratch root for this snapshot ($FORKLAB_HOME/snapshots/work/<key>).
	WorkDir string
	// KeepWork keeps the extracted data after a successful export instead of deleting it.
	KeepWork bool
	// Log receives one line per step (init, extract, export). May be nil.
	Log io.Writer
}

// Exported is the result of Export.
type Exported struct {
	// Path is the exported genesis JSON.
	Path string
	// Cached is true when a previous export of the same archive and version was reused.
	Cached bool
	// Log is the export stderr log path (empty when Cached).
	Log string
}

// exportMeta records what produced an export and what the finished export
// looks like, so a reuse can check both.
type exportMeta struct {
	Archive       string    `json:"archive"`
	Size          int64     `json:"size"`
	ModTime       time.Time `json:"mod_time"`
	Version       string    `json:"version"`
	ChainID       string    `json:"chain_id"`
	Binary        string    `json:"binary"`
	BinarySize    int64     `json:"binary_size"`
	BinaryModTime time.Time `json:"binary_mod_time"`
	Args          []string  `json:"args"`
	ExportSize    int64     `json:"export_size"`
	ExportSHA256  string    `json:"export_sha256"`
	ExportedAt    time.Time `json:"exported_at"`
}

// exportSpaceFactor over the archive size covers the extracted data plus the
// exported JSON.
const exportSpaceFactor = 4

// Export extracts archive into a scratch node home under WorkDir and runs the
// chain binary's export on it. The result is cached per archive, binary,
// version, chain ID, and args, so a rerun with all unchanged returns the
// previous export untouched. WorkDir is locked for the duration, so
// concurrent exports of one snapshot run one after the other and the later
// ones reuse the first result.
func Export(ctx context.Context, in ExportInput) (Exported, error) {
	archive, err := filepath.Abs(in.Archive)
	if err != nil {
		return Exported{}, err
	}
	info, err := os.Stat(archive)
	if err != nil {
		return Exported{}, err
	}
	binary, err := filepath.Abs(in.Binary)
	if err != nil {
		return Exported{}, err
	}
	binInfo, err := os.Stat(binary)
	if err != nil {
		return Exported{}, err
	}
	if err := os.MkdirAll(in.WorkDir, 0o755); err != nil {
		return Exported{}, err
	}
	logf := func(format string, a ...any) {
		if in.Log != nil {
			_, _ = fmt.Fprintf(in.Log, format+"\n", a...)
		}
	}
	unlock, err := lock(ctx, filepath.Join(in.WorkDir, "lock"), func() {
		logf("waiting for another forklab process working on this snapshot")
	})
	if err != nil {
		return Exported{}, fmt.Errorf("lock %s: %w", in.WorkDir, err)
	}
	defer unlock()

	v := unsafeName.ReplaceAllString(in.Version, "_")
	home := filepath.Join(in.WorkDir, "home")
	out := filepath.Join(in.WorkDir, "exported-"+v+".json")
	metaPath := filepath.Join(in.WorkDir, "exported-"+v+".meta.json")
	logPath := filepath.Join(in.WorkDir, "export-"+v+".log")
	want := exportMeta{
		Archive: archive, Size: info.Size(), ModTime: info.ModTime().UTC(),
		Version: in.Version, ChainID: in.ChainID,
		Binary: binary, BinarySize: binInfo.Size(), BinaryModTime: binInfo.ModTime().UTC(),
		Args: append([]string{}, in.Args...),
	}
	if exportCached(metaPath, out, want) {
		return Exported{Path: out, Cached: true}, nil
	}

	if _, ok := extracted(archive, info, home); !ok {
		size := info.Size()
		basis := fmt.Sprintf("%dx the %s archive", exportSpaceFactor, humanBytes(uint64(size)))
		if err := checkFreeSpace(in.WorkDir, exportSpaceFactor*size, basis); err != nil {
			return Exported{}, err
		}
	}
	if _, err := os.Stat(filepath.Join(home, "config", "config.toml")); err != nil {
		logf("init scratch home %s", home)
		if err := initHome(ctx, binary, home, in.ChainID, logPath); err != nil {
			return Exported{}, err
		}
	}
	logf("extract %s into %s", archive, filepath.Join(home, dataDir))
	if _, err := Extract(ctx, archive, home); err != nil {
		return Exported{}, err
	}

	logf("export with %s (log: %s)", binary, logPath)
	if want.ExportSize, want.ExportSHA256, err = runExport(ctx, binary, home, in.Args, out, logPath); err != nil {
		return Exported{}, err
	}
	want.ExportedAt = time.Now().UTC()
	if err := writeJSONAtomic(metaPath, want); err != nil {
		return Exported{}, err
	}

	if !in.KeepWork {
		// The marker goes first so it is never left describing removed data.
		for _, p := range []string{markerFile, dataDir} {
			if err := os.RemoveAll(filepath.Join(home, p)); err != nil {
				return Exported{}, err
			}
		}
		logf("removed %s", filepath.Join(home, dataDir))
	}
	return Exported{Path: out, Log: logPath}, nil
}

// exportCached reports whether the meta on disk describes want's inputs and
// the export file still has the recorded size and hash.
func exportCached(metaPath, out string, want exportMeta) bool {
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var m exportMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	if m.Archive != want.Archive || m.Size != want.Size || !m.ModTime.Equal(want.ModTime) ||
		m.Version != want.Version || m.ChainID != want.ChainID ||
		m.Binary != want.Binary || m.BinarySize != want.BinarySize || !m.BinaryModTime.Equal(want.BinaryModTime) ||
		strings.Join(m.Args, "\x00") != strings.Join(want.Args, "\x00") {
		return false
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 || st.Size() != m.ExportSize {
		return false
	}
	size, sum, err := fileDigest(out)
	return err == nil && size == m.ExportSize && sum == m.ExportSHA256
}

func fileDigest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func initHome(ctx context.Context, bin, home, chainID, logPath string) error {
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.CommandContext(ctx, bin, "init", "forklab-export", "--home", home, "--chain-id", chainID)
	cmd.Stdout, cmd.Stderr = log, log
	return stepError("init", bin, cmd.Run(), logPath)
}

// runExport streams the export to out and returns its size and sha256.
func runExport(ctx context.Context, bin, home string, args []string, out, logPath string) (int64, string, error) {
	log, err := os.Create(logPath)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = log.Close() }()
	partial := out + ".partial"
	f, err := os.Create(partial)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	cmd := exec.CommandContext(ctx, bin, append([]string{"export", "--home", home}, args...)...)
	cmd.Stdout, cmd.Stderr = io.MultiWriter(f, h), log
	runErr := cmd.Run()
	if err := errors.Join(stepError("export", bin, runErr, logPath), f.Close()); err != nil {
		_ = os.Remove(partial)
		return 0, "", err
	}
	st, err := os.Stat(partial)
	if err != nil {
		return 0, "", err
	}
	return st.Size(), hex.EncodeToString(h.Sum(nil)), os.Rename(partial, out)
}

// stepError names the failed step, its command, and the line of its log
// that says why: the first panic or error, else the last line.
func stepError(step, bin string, err error, logPath string) error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf("%s: %s %s: %v", step, bin, step, err)
	if reason := problemLine(logPath); reason != "" {
		msg += ": " + reason
	}
	return fmt.Errorf("%s (log: %s)", msg, logPath)
}

func problemLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.Contains(line, "panic:") || strings.Contains(line, "Error:") || strings.Contains(line, " ERR ") {
			return line
		}
		last = line
	}
	return last
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
