package binary

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
	"github.com/pierrec/lz4/v4"
)

// script is a fake chain binary whose `version` prints out.
func script(out string) []byte {
	return []byte("#!/bin/sh\nprintf '" + out + "'\n")
}

type entry struct {
	name string
	body []byte
}

func tarGz(t *testing.T, entries ...entry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	writeTar(t, gz, entries)
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarLz4(t *testing.T, entries ...entry) []byte {
	var buf bytes.Buffer
	zw := lz4.NewWriter(&buf)
	writeTar(t, zw, entries)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTar(t *testing.T, w interface{ Write([]byte) (int, error) }, entries []entry) {
	tw := tar.NewWriter(w)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

func zipOf(t *testing.T, entries ...entry) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serve serves body at every path and counts requests.
func serve(t *testing.T, body []byte) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func chainProfile(version string, src profile.Source) profile.Profile {
	return profile.Profile{Name: "chain", BinaryName: "chaind", ChainID: "chain-1", Binaries: map[string]profile.Source{version: src}}
}

func versionOf(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("running %s: %v", bin, err)
	}
	return string(out)
}

func TestResolveURLFormats(t *testing.T) {
	bin := script("1.2.3\n")
	tests := []struct {
		name, file string
		body       []byte
	}{
		{"tar.gz nested like the exrpd release", "chain_{version}_{os}_{arch}.tar.gz", tarGz(t, entry{"README.md", []byte("hi")}, entry{"bin/chaind", bin})},
		{"tgz at the root", "chain.tgz", tarGz(t, entry{"chaind", bin})},
		{"zip", "chain.zip", zipOf(t, entry{"dist/linux/chaind", bin}, entry{"dist/linux/chaind.sha256", []byte("x")})},
		{"tar.lz4", "chain.tar.lz4", tarLz4(t, entry{"./bin/chaind", bin})},
		{"raw binary", "chaind-{version}", bin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.body)
			c := Cache{Dir: t.TempDir()}
			p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/" + tt.file)})
			var lastDone int64
			b, err := c.Resolve(context.Background(), p, "1.2.3", Options{Progress: func(done, _ int64) { lastDone = done }})
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(c.Dir, "chain")
			if !strings.HasPrefix(b.Path, want+string(filepath.Separator)) || filepath.Base(b.Path) != "chaind" || b.Kind != KindURL || b.ReportedVersion != "1.2.3" || b.Check != CheckMatched {
				t.Errorf("resolved %+v, want path %s matched 1.2.3", b, want)
			}
			if got := versionOf(t, b.Path); got != "1.2.3\n" {
				t.Errorf("cached binary prints %q", got)
			}
			if lastDone != int64(len(tt.body)) {
				t.Errorf("progress ended at %d of %d bytes", lastDone, len(tt.body))
			}
			entries, _ := os.ReadDir(filepath.Dir(b.Path))
			if len(entries) != 2 {
				t.Errorf("version dir holds %v, want only the binary and meta.json", entries)
			}
		})
	}
}

func TestResolveIsIdempotent(t *testing.T) {
	srv, hits := serve(t, tarGz(t, entry{"bin/chaind", script("v1.2.3\n")}))
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/c.tar.gz")})
	first, err := c.Resolve(context.Background(), p, "1.2.3", Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Resolve(context.Background(), p, "1.2.3", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Errorf("downloaded %d times, want 1", hits.Load())
	}
	if first != second || second.ReportedVersion != "v1.2.3" || second.Check != CheckMatched {
		t.Errorf("second resolve %+v differs from first %+v", second, first)
	}
	listed, err := c.List()
	if err != nil || len(listed) != 1 || listed[0] != first {
		t.Errorf("list = %+v, %v", listed, err)
	}
}

func TestListSkipsMetaThatVanishes(t *testing.T) {
	srv, _ := serve(t, tarGz(t, entry{"bin/chaind", script("v1.2.3\n")}))
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/c.tar.gz")})
	kept, err := c.Resolve(context.Background(), p, "1.2.3", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A dangling meta.json is what List sees when a delete lands between its
	// glob and its read.
	gone := filepath.Join(c.Dir, "chain", "9.9.9")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(gone, "missing"), filepath.Join(gone, "meta.json")); err != nil {
		t.Fatal(err)
	}
	listed, err := c.List()
	if err != nil || len(listed) != 1 || listed[0] != kept {
		t.Errorf("list = %+v, %v; want only %+v", listed, err, kept)
	}
}

func TestResolveNeverWritesArchivePaths(t *testing.T) {
	bin := script("1.2.3\n")
	tests := []struct {
		name, file string
		body       []byte
	}{
		{"tar parent escape", "c.tar.gz", tarGz(t, entry{"bin/chaind", bin}, entry{"../../evil", []byte("x")})},
		{"zip binary outside the root", "c.zip", zipOf(t, entry{"../../chaind", bin})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.body)
			root := t.TempDir()
			c := Cache{Dir: filepath.Join(root, "cache", "bin")}
			p := chainProfile("1.2.3", profile.URLSource{URL: profile.Template(srv.URL + "/" + tt.file)})
			b, err := c.Resolve(context.Background(), p, "1.2.3", Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(b.Path, filepath.Join(c.Dir, "chain")+string(filepath.Separator)) || filepath.Base(b.Path) != "chaind" {
				t.Errorf("binary at %s, want inside the cache", b.Path)
			}
			for _, escaped := range []string{"evil", "chaind", "cache/evil", "cache/chaind"} {
				if _, err := os.Stat(filepath.Join(root, escaped)); err == nil {
					t.Errorf("archive entry was written to %s", escaped)
				}
			}
		})
	}
}

func TestResolveMissingBinaryInArchive(t *testing.T) {
	srv, _ := serve(t, tarGz(t, entry{"bin/otherd", script("1\n")}))
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1", profile.URLSource{URL: profile.Template(srv.URL + "/c.tar.gz")})
	if _, err := c.Resolve(context.Background(), p, "1", Options{}); !errors.Is(err, errNotInArchive) {
		t.Errorf("err = %v, want not found in archive", err)
	}
}

func TestResolveVersionCheck(t *testing.T) {
	tests := []struct {
		name      string
		prints    string
		noVerify  bool
		wantErr   error
		wantCheck Check
		reported  string
	}{
		{"match ignores a leading v", "v2.0.0\n", false, nil, CheckMatched, "v2.0.0"},
		{"first non-empty line", "\n  2.0.0  \nextra\n", false, nil, CheckMatched, "2.0.0"},
		{"mismatch is an error", "1.9.0\n", false, ErrVersionMismatch, "", ""},
		{"mismatch accepted with no-verify", "1.9.0\n", true, nil, CheckMismatch, "1.9.0"},
		{"empty output is unknown", "\n", false, nil, CheckUnknown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, script(tt.prints))
			c := Cache{Dir: t.TempDir()}
			p := chainProfile("2.0.0", profile.URLSource{URL: profile.Template(srv.URL + "/chaind")})
			b, err := c.Resolve(context.Background(), p, "2.0.0", Options{NoVerify: tt.noVerify})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			listed, _ := c.List()
			if tt.wantErr != nil {
				if len(listed) != 0 {
					t.Errorf("rejected binary was cached: %+v", listed)
				}
				return
			}
			if b.Check != tt.wantCheck || b.ReportedVersion != tt.reported || len(listed) != 1 || listed[0] != b {
				t.Errorf("resolved %+v, listed %+v; want check %s reported %q", b, listed, tt.wantCheck, tt.reported)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "chaind")
	if err := os.WriteFile(good, script("3.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	noExec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noExec, script("3.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: t.TempDir()}

	b, err := c.Resolve(context.Background(), chainProfile("3.0.0", profile.PathSource{Path: profile.Template(good)}), "3.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Path != good || b.Kind != KindPath || b.Check != CheckMatched {
		t.Errorf("resolved %+v, want the original path", b)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "chain", "3.0.0", "chaind")); err == nil {
		t.Error("path binary was copied into the cache")
	}
	if listed, _ := c.List(); len(listed) != 1 || listed[0].Path != good {
		t.Errorf("list = %+v", listed)
	}

	for _, bad := range []string{noExec, filepath.Join(dir, "missing")} {
		if _, err := c.Resolve(context.Background(), chainProfile("3.0.0", profile.PathSource{Path: profile.Template(bad)}), "3.0.0", Options{}); err == nil {
			t.Errorf("%s resolved, want an error", bad)
		}
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// buildScript writes a chaind printing $VER and appends a line to $COUNTER
// per build, so tests see both the env merge and how often it ran.
const buildScript = `mkdir -p out && printf '#!/bin/sh\necho %s\n' "$VER" > out/chaind && chmod +x out/chaind && echo built >> "$COUNTER"`

func builds(t *testing.T, counter string) int {
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "built")
}

// bareRepo creates root/repo.git with one commit tagged v1.0.0.
func bareRepo(t *testing.T, root string) string {
	work, bare := filepath.Join(root, "work"), filepath.Join(root, "repo.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "init", "-q")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("chain"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-q", "-m", "init")
	gitCmd(t, work, "tag", "v1.0.0")
	gitCmd(t, root, "clone", "-q", "--bare", work, bare)
	return bare
}

func TestResolveGit(t *testing.T) {
	root := t.TempDir()
	bare := bareRepo(t, root)
	counter := filepath.Join(root, "builds")
	src := profile.GitSource{
		Repo:  profile.Template("file://" + bare),
		Ref:   "v{version}",
		Build: buildScript,
		Out:   "out/chaind",
		Env:   map[string]string{"VER": "1.0.0", "COUNTER": counter},
	}
	c := Cache{Dir: filepath.Join(root, "cache")}
	p := chainProfile("1.0.0", src)

	b, err := c.Resolve(context.Background(), p, "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != KindGit || b.Source != "file://"+bare+"@v1.0.0" || b.Check != CheckMatched || versionOf(t, b.Path) != "1.0.0\n" {
		t.Errorf("resolved %+v", b)
	}
	if _, err := c.Resolve(context.Background(), p, "1.0.0", Options{}); err != nil || builds(t, counter) != 1 {
		t.Errorf("second resolve: err %v, builds %d, want the cached build", err, builds(t, counter))
	}
	if _, err := c.Resolve(context.Background(), p, "1.0.0", Options{Rebuild: true}); err != nil || builds(t, counter) != 2 {
		t.Errorf("rebuild: err %v, builds %d, want 2", err, builds(t, counter))
	}
	if leftovers := scratchDirs(t, filepath.Join(c.Dir, "chain")); len(leftovers) != 0 {
		t.Errorf("scratch dirs left behind: %v", leftovers)
	}

	src.Ref = "v9.9.9"
	if _, err := c.Resolve(context.Background(), chainProfile("1.0.0", src), "1.0.0", Options{}); err == nil {
		t.Error("missing ref resolved, want an error")
	}
}

func TestResolveSrcRecordsVersionAndRebuilds(t *testing.T) {
	checkout := t.TempDir()
	counter := filepath.Join(checkout, "builds")
	src := profile.SrcSource{
		Dir:   profile.Template(checkout),
		Build: buildScript,
		Out:   "out/chaind",
		Env:   map[string]string{"VER": "dev-build", "COUNTER": counter},
	}
	c := Cache{Dir: t.TempDir()}

	b, err := c.Resolve(context.Background(), chainProfile("1.0.0", src), "1.0.0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Check != CheckSkipped || b.ReportedVersion != "dev-build" {
		t.Errorf("src resolved %+v, want the reported version recorded and the check skipped", b)
	}

	src.Env["VER"] = "dev-build-2"
	b, err = c.Resolve(context.Background(), chainProfile("1.0.0", src), "1.0.0", Options{Rebuild: true})
	if err != nil || b.ReportedVersion != "dev-build-2" || builds(t, counter) != 2 {
		t.Errorf("rebuild: %+v, err %v, builds %d", b, err, builds(t, counter))
	}
}

func TestResolveFailedBuildKeepsPreviousBinary(t *testing.T) {
	checkout := t.TempDir()
	counter := filepath.Join(checkout, "builds")
	src := profile.SrcSource{Dir: profile.Template(checkout), Build: buildScript, Out: "out/chaind", Env: map[string]string{"VER": "a", "COUNTER": counter}}
	c := Cache{Dir: t.TempDir()}
	first, err := c.Resolve(context.Background(), chainProfile("1", src), "1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	src.Build = "echo compile error; exit 3"
	_, err = c.Resolve(context.Background(), chainProfile("1", src), "1", Options{Rebuild: true})
	if err == nil || !strings.Contains(err.Error(), "compile error") {
		t.Fatalf("err = %v, want the build output in the error", err)
	}
	if listed, _ := c.List(); len(listed) != 1 || listed[0] != first || versionOf(t, first.Path) != "a\n" {
		t.Errorf("after failed rebuild list = %+v", listed)
	}
}

func TestResolveUnknownVersion(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1", profile.PathSource{Path: "/bin/true"})
	if _, err := c.Resolve(context.Background(), p, "2", Options{}); err == nil || !strings.Contains(err.Error(), "no binary version 2") {
		t.Errorf("err = %v", err)
	}
}
