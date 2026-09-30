package profile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
)

func TestPatchFileResolutionAndLabSnapshot(t *testing.T) {
	dir := t.TempDir()
	patchFile := filepath.Join(dir, "corrections.yaml")
	if err := os.WriteFile(patchFile, []byte("fresh_patches: ['.fresh = 1']\nfork_patches: ['.fork = 1']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := validDoc()
	d.PatchesFile = "corrections.yaml"
	d.ForkPatches = []string{".fork = 2"}
	s := profile.Store{Dir: dir}
	e, err := s.Save(d)
	if err != nil {
		t.Fatal(err)
	}
	if e.Doc.PatchesFile != d.PatchesFile || !reflect.DeepEqual(e.Doc.ForkPatches, d.ForkPatches) {
		t.Fatal("saving changed the patch file reference or inline filters")
	}
	loaded, err := s.Get(d.Name)
	if err != nil {
		t.Fatal(err)
	}
	wantFork := []string{".fork = 1", ".fork = 2"}
	if !reflect.DeepEqual(loaded.Profile.ForkPatches, wantFork) {
		t.Fatalf("fork filters = %v, want %v", loaded.Profile.ForkPatches, wantFork)
	}
	_, validated, err := profile.LoadFile(e.Path)
	if err != nil || !reflect.DeepEqual(validated.ForkPatches, wantFork) {
		t.Fatalf("validating a file from a different directory: %v, %v", validated.ForkPatches, err)
	}
	snapshot := loaded.Snapshot()
	if snapshot.PatchesFile != "" || len(snapshot.FreshPatches) != 1 || !reflect.DeepEqual(snapshot.ForkPatches, wantFork) {
		t.Fatalf("lab profile = %+v", snapshot)
	}
	if err := os.Remove(patchFile); err != nil {
		t.Fatal(err)
	}
	p, err := snapshot.Profile()
	if err != nil || !reflect.DeepEqual(p.ForkPatches, wantFork) {
		t.Fatalf("lab depends on the removed patch file: %v", err)
	}
	if _, err := s.Get(d.Name); err == nil {
		t.Fatal("profile with missing patch file was accepted")
	}
}

func TestPatchFilesAreStrictlyValidated(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"unknown field", "fork_patch: ['.x = 1']", "field fork_patch not found"},
		{"invalid jq", "fork_patches: ['.x = (']", "fork_patches[0]"},
		{"invalid inline jq", "fork_patches: ['.x = 1']", "fork_patches[0]"},
		{"multiple documents", "fork_patches: []\n---\nfork_patches: []", "single YAML document"},
		{"wrong type", "fork_patches: hello", "cannot unmarshal"},
		{"empty file", "", "EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corrections.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			d := validDoc()
			d.PatchesFile = path
			if tc.name == "invalid inline jq" {
				d.ForkPatches = []string{".bad = ("}
			}
			if _, err := d.Profile(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Profile error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPatchFileOptionalAndBuiltinReferencePortable(t *testing.T) {
	d := validDoc()
	p, err := d.Profile()
	if err != nil || len(p.FreshPatches) != 0 || len(p.ForkPatches) != 0 {
		t.Fatalf("plain profile unexpectedly requires corrections: %v", err)
	}
	d.ForkPatches = []string{".x = 1"}
	p, err = d.Profile()
	if err != nil || !reflect.DeepEqual(p.ForkPatches, d.ForkPatches) {
		t.Fatalf("legacy inline filters = %v, %v", p.ForkPatches, err)
	}
	e, err := (profile.Store{Dir: t.TempDir()}).Get("xrplevm")
	if err != nil {
		t.Fatal(err)
	}
	if e.Doc.PatchesFile != "builtin:xrplevm.yaml" || len(e.Doc.ForkPatches) != 0 {
		t.Fatal("XRPL corrections were not separated into their own file")
	}
	e.Doc.Name = "xrp-copy"
	copied, err := (profile.Store{Dir: t.TempDir()}).Save(e.Doc)
	if err != nil || !reflect.DeepEqual(copied.Profile.ForkPatches, e.Profile.ForkPatches) {
		t.Fatalf("copy lost bundled corrections: %v", err)
	}
	e.Doc.PatchesFile = ""
	p, err = e.Doc.Profile()
	if err != nil || len(p.FreshPatches) != 0 || len(p.ForkPatches) != 0 {
		t.Fatalf("removing the reference did not disable corrections: %v", err)
	}
}
