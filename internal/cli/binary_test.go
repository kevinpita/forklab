package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/binary"
)

func chainTarGz(t *testing.T, versionOutput string) []byte {
	body := []byte("#!/bin/sh\necho " + versionOutput + "\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "bin/simd", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownloadProgressPicksUnitsBySize(t *testing.T) {
	for _, c := range []struct {
		done, total int64
		want        string
	}{
		{300, 1000, "\rdownloading 30% (300/1000 B)"},
		{300 << 10, 700 << 10, "\rdownloading 42% (300.0/700.0 KiB)"},
		{35 << 20, 70 << 20, "\rdownloading 50% (35.0/70.0 MiB)"},
		{1 << 30, 3 << 30, "\rdownloading 33% (1.0/3.0 GiB)"},
		{5 << 20, 0, "\rdownloading 5.0 MiB"},
		{512, 0, "\rdownloading 512 B"},
	} {
		var buf bytes.Buffer
		(&progress{w: &buf, last: -1}).update(c.done, c.total)
		if buf.String() != c.want {
			t.Errorf("update(%d, %d) = %q, want %q", c.done, c.total, buf.String(), c.want)
		}
	}
}

func TestUpgradeInfoCarriesTheDownloadLinkOnlyWhenCosmovisorDownloads(t *testing.T) {
	at := func(source, entry string) binary.Binary {
		return binary.Binary{Profile: "chain", Version: "2.0.0", Kind: binary.KindURL, Source: source, Path: "/cache/chaind", ArtifactSHA256: "abc", ArtifactEntry: entry}
	}
	url := at("https://x/chain.tar.gz", "bin/chaind")
	link := `{"binaries":{"plan9/` + runtime.GOARCH + `":"`
	for _, c := range []struct {
		name     string
		bin      binary.Binary
		download bool
		want     string
		wantErr  string
	}{
		{name: "placed by forklab", bin: url, want: "forklab upgrade to 2.0.0"},
		{name: "download", bin: url, download: true, want: link + `https://x/chain.tar.gz?checksum=sha256:abc"}}`},
		{name: "binary at the archive root", bin: at("https://x/chain.zip", "chaind"), download: true, want: link + `https://x/chain.zip?checksum=sha256:abc"}}`},
		{name: "url with a query", bin: at("https://x/get?file=chain", "chaind"), download: true, want: link + `https://x/get?file=chain\u0026checksum=sha256:abc"}}`},
		{name: "url with a fragment", bin: at("https://x/chaind#v2", "chaind"), download: true, want: link + `https://x/chaind?checksum=sha256:abc#v2"}}`},
		{name: "path source", bin: binary.Binary{Profile: "chain", Version: "2.0.0", Kind: binary.KindPath}, download: true, wantErr: "needs a url source: version 2.0.0 of profile chain is a path source"},
		{name: "binary deeper in the archive", bin: at("https://x/chain.tar.gz", "dist/linux/chaind"), download: true, wantErr: "cosmovisor looks for chaind or bin/chaind at the top of the archive, and https://x/chain.tar.gz has it at dist/linux/chaind"},
		{name: "archive cosmovisor cannot unpack", bin: at("https://x/chain.tar.lz4", "bin/chaind"), download: true, wantErr: "cosmovisor cannot unpack a .tar.lz4 archive"},
		{name: "placed by forklab from any archive", bin: at("https://x/chain.tar.lz4", "dist/linux/chaind"), want: "forklab upgrade to 2.0.0"},
	} {
		got, err := upgradeInfo(c.bin, c.download, "plan9")
		if got != c.want || (err == nil) != (c.wantErr == "") || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: upgradeInfo = %q, %v; want %q, error %q", c.name, got, err, c.want, c.wantErr)
		}
	}
}

type binaryEnvelope[T any] struct {
	OK    bool `json:"ok"`
	Data  T    `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func runBinary[T any](t *testing.T, wantCode int, args ...string) binaryEnvelope[T] {
	t.Helper()
	code, stdout, stderr := run(append(args, "--json")...)
	if code != wantCode {
		t.Fatalf("%v: code = %d, want %d\nstdout: %s\nstderr: %s", args, code, wantCode, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("%v: --json wrote to stderr: %q", args, stderr)
	}
	var env binaryEnvelope[T]
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("%v: stdout is not one envelope: %v: %q", args, err, stdout)
	}
	return env
}

func TestBinaryFetchListJSON(t *testing.T) {
	configDir(t)
	home := t.TempDir()
	t.Setenv("FORKLAB_HOME", home)
	archive := chainTarGz(t, "v1.2.3")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(srv.Close)

	runJSON(t, 0, "profile", "create", "chain", "--from", "simd",
		"--remove-binary", "0.53.8",
		"--binary", "1.2.3=url:"+srv.URL+"/simd_{version}_{os}_{arch}.tar.gz",
		"--binary", "9.9.9=url:"+srv.URL+"/other.tar.gz",
	)

	fetched := runBinary[binary.Binary](t, 0, "binary", "fetch", "1.2.3", "--profile", "chain")
	want := binary.Binary{
		Profile: "chain", Version: "1.2.3", Kind: binary.KindURL,
		Source:          fetched.Data.Source,
		Path:            fetched.Data.Path,
		ReportedVersion: "v1.2.3", Check: binary.CheckMatched,
		ArtifactSHA256: fmt.Sprintf("%x", sha256.Sum256(archive)), ArtifactEntry: "bin/simd",
		Size: fetched.Data.Size, ModTime: fetched.Data.ModTime,
	}
	if !strings.HasPrefix(fetched.Data.Path, filepath.Join(home, "bin", "chain")+string(filepath.Separator)) || filepath.Base(fetched.Data.Path) != "simd" || !fetched.OK || fetched.Data != want || !strings.HasPrefix(want.Source, srv.URL+"/simd_1.2.3_") {
		t.Errorf("fetch = %+v, want %+v", fetched.Data, want)
	}

	mismatch := runBinary[struct{}](t, 1, "binary", "fetch", "9.9.9", "--profile", "chain")
	if !strings.Contains(mismatch.Error.Message, "version mismatch: simd reports v1.2.3, want 9.9.9 (pass --no-verify") {
		t.Errorf("mismatch error = %+v", mismatch.Error)
	}

	listed := runBinary[[]binary.Binary](t, 0, "binary", "list")
	if len(listed.Data) != 1 || listed.Data[0] != want {
		t.Errorf("list = %+v", listed.Data)
	}
	if other := runBinary[[]binary.Binary](t, 0, "binary", "list", "--profile", "simd"); len(other.Data) != 0 {
		t.Errorf("list --profile simd = %+v", other.Data)
	}

	usage := []struct {
		args    []string
		wantMsg string
	}{
		{[]string{"binary", "build", "1.2.3", "--profile", "chain"}, "binary build applies to git and src"},
		{[]string{"binary", "fetch", "4.0.0", "--profile", "chain"}, "has no binary version 4.0.0"},
		{[]string{"binary", "fetch", "1.2.3"}, `"profile" not set`},
	}
	for _, u := range usage {
		env := runBinary[struct{}](t, 2, u.args...)
		if !strings.Contains(env.Error.Message, u.wantMsg) {
			t.Errorf("%v: error = %+v, want %q", u.args, env.Error, u.wantMsg)
		}
	}
}
