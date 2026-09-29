package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		Path:            filepath.Join(home, "bin", "chain", "1.2.3", "simd"),
		ReportedVersion: "v1.2.3", Check: binary.CheckMatched,
		Size: fetched.Data.Size, ModTime: fetched.Data.ModTime,
	}
	if !fetched.OK || fetched.Data != want || !strings.HasPrefix(want.Source, srv.URL+"/simd_1.2.3_") {
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
