package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
)

func completionFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("FORKLAB_HOME", home)
	t.Setenv("FORKLAB_CONFIG_DIR", filepath.Join(home, "config"))
	store, err := profile.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get("simd")
	if err != nil {
		t.Fatal(err)
	}
	entry.Doc.Name = "custom"
	entry.Doc.Binaries = map[string]profile.BinaryDocument{"1.0": {Path: "/nonexistent/chain"}, "2.0": {Path: "/nonexistent/chain2"}}
	if _, err := store.Save(entry.Doc); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "labs", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := lab.Config{Name: "demo", Validators: 2, Profile: entry.Doc, Nodes: []lab.Node{{Index: 0, Name: "node0"}, {Index: 1, Name: "node1"}}}
	if err := lab.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

func completionSnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[path] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestLocalCompletions(t *testing.T) {
	home := completionFixture(t)
	before := completionSnapshot(t, home)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"lab", "show", "d"}, "demo\n:4\n"},
		{[]string{"profile", "show", "cu"}, "custom\n:4\n"},
		{[]string{"profile", "create", "new", "--from", "cu"}, "custom\n:4\n"},
		{[]string{"lab", "create", "new", "--profile", "cu"}, "custom\n:4\n"},
		{[]string{"lab", "create", "new", "--profile", "custom", "--version", ""}, "1.0\n2.0\n:4\n"},
		{[]string{"binary", "fetch", "--profile", "custom", ""}, "1.0\n2.0\n:4\n"},
		{[]string{"node", "start", "--lab", "demo", ""}, "0\n1\nall\n:4\n"},
		{[]string{"node", "logs", "--lab", "demo", ""}, "0\n1\n:4\n"},
		{[]string{"node", "logs", ""}, "0\n1\n:4\n"},
		{[]string{"node", "logs", "--lab", filepath.Join(home, "labs", "demo"), ""}, "0\n1\n:4\n"},
		{[]string{"node", "logs", "--lab", "missing", ""}, ":4\n"},
		{[]string{"node", "logs", "0", ""}, ":4\n"},
		{[]string{"upgrade", "schedule", "--lab", "demo", "--height", "20", ""}, "1.0\n2.0\n:4\n"},
		{[]string{"upgrade", "recover", "--lab", "demo", "--version", ""}, "1.0\n2.0\n:4\n"},
		{[]string{"node", "restart", "0", "--lab", "demo", "--binary", ""}, "1.0\n2.0\n:4\n"},
		{[]string{"node", "restart", "0", "--binary", "./"}, ":0\n"},
		{[]string{"status", "--lab", "./"}, ":16\n"},
		{[]string{"profile", "validate", "./"}, ":0\n"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, out, errOut := run(append([]string{"__complete"}, tt.args...)...)
			if code != 0 || out != tt.want {
				t.Errorf("exit %d stdout %q stderr %q; want %q", code, out, errOut, tt.want)
			}
		})
	}
	if after := completionSnapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Fatal("completion changed local files")
	}
	second := filepath.Join(home, "labs", "other")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"node", "start", ""}, {"upgrade", "schedule", "--height", "20", ""}} {
		code, out, errOut := run(append([]string{"__complete"}, args...)...)
		if code != 0 || out != ":4\n" {
			t.Errorf("ambiguous lab: exit %d stdout %q stderr %q", code, out, errOut)
		}
	}
}

func TestCompletionEmptyStoreDoesNotCreateDirectories(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("FORKLAB_HOME", home)
	t.Setenv("FORKLAB_CONFIG_DIR", filepath.Join(home, "config"))
	code, out, stderr := run("__complete", "node", "logs", "")
	if code != 0 || out != ":4\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, stderr)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("completion created home: %v", err)
	}
}
