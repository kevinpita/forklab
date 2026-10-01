package cli

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
)

func TestLabBinaryOverrideValidation(t *testing.T) {
	t.Setenv("FORKLAB_CONFIG_DIR", t.TempDir())
	t.Setenv("FORKLAB_HOME", t.TempDir())
	tests := []struct {
		name  string
		flags []string
		want  string
	}{
		{"metadata only", []string{"--binary-build", "0.53.8=make build"}, "requires --binary"},
		{"wrong version", []string{"--binary", "other=path:/tmp/simd"}, "must target --version"},
		{"wrong metadata version", []string{"--binary", "0.53.8=git:https://g/x", "--binary-ref", "other=main"}, "must target --version"},
		{"Git required fields", []string{"--binary", "0.53.8=git:https://g/x"}, "set with --binary-ref"},
		{"Git build required", []string{"--binary", "0.53.8=git:https://g/x", "--binary-ref", "0.53.8=main"}, "set with --binary-build"},
		{"src output required", []string{"--binary", "0.53.8=src:.", "--binary-build", "0.53.8=make"}, "set with --binary-out"},
		{"src ref forbidden", []string{"--binary", "0.53.8=src:.", "--binary-ref", "0.53.8=main", "--binary-build", "0.53.8=make", "--binary-out", "0.53.8=out"}, "ref"},
		{"path build forbidden", []string{"--binary", "0.53.8=path:/tmp/simd", "--binary-build", "0.53.8=make"}, "build"},
		{"two replacements", []string{"--binary", "0.53.8=path:/tmp/one", "--binary", "0.53.8=path:/tmp/two"}, "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"lab", "create", "custom", "--profile", "simd", "--version", "0.53.8"}, tt.flags...)
			code, _, stderr := run(args...)
			if code != 2 || !strings.Contains(stderr, tt.want) {
				t.Fatalf("exit %d: %s, want %s", code, stderr, tt.want)
			}
		})
	}
	store, err := profile.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.HasUser("simd") {
		t.Fatal("lab override saved a global profile")
	}
}

func TestLabBinarySnapshotIsConsistentAndSelfContained(t *testing.T) {
	store := profile.Store{Dir: t.TempDir()}
	base, err := store.Get("simd")
	if err != nil {
		t.Fatal(err)
	}
	base.Doc.PatchesFile = "missing-relative-file.yaml"
	base.Profile.FreshPatches = []string{".app_state.test = true"}
	original := maps.Clone(base.Doc.Binaries)
	for _, tt := range []struct {
		kind, location string
		metadata       [][]string
	}{
		{"path", "my binaries/simd", nil},
		{"path", "~/my binaries/simd", nil},
		{"url", "https://example.test/simd-{version}", nil},
		{"git", "https://example.test/repo", [][]string{{"0.53.8=main"}, {"0.53.8=make build"}, {"0.53.8=build/simd"}, {"0.53.8=KEY=with spaces"}}},
		{"src", "my checkout", [][]string{nil, {"0.53.8=make build"}, {"0.53.8=build/simd"}}},
	} {
		t.Run(tt.kind+tt.location, func(t *testing.T) {
			flags := binaryFlags{binaries: []string{"0.53.8=" + tt.kind + ":" + tt.location}}
			copy(flags.binaryFields[:], tt.metadata)
			overridden, err := labProfile(base, flags, "0.53.8")
			if err != nil {
				t.Fatal(err)
			}
			d := overridden.Snapshot()
			if d.PatchesFile != "" || !reflect.DeepEqual(d.FreshPatches, base.Profile.FreshPatches) {
				t.Fatalf("patches lost: %+v", d)
			}
			if !reflect.DeepEqual(base.Doc.Binaries, original) {
				t.Fatal("base profile was modified")
			}
			p, err := d.Profile()
			if err != nil || !reflect.DeepEqual(p, overridden.Profile) {
				t.Fatalf("snapshot and validated profile differ: %v", err)
			}
			b := d.Binaries["0.53.8"]
			if (tt.kind == "path" && !filepath.IsAbs(b.Path)) || (tt.kind == "src" && !filepath.IsAbs(b.Src)) {
				t.Fatalf("relative source persisted: %+v", b)
			}
			if strings.HasPrefix(tt.location, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				if b.Path != filepath.Join(home, "my binaries/simd") {
					t.Fatalf("tilde not expanded: %s", b.Path)
				}
			}
		})
	}
}
