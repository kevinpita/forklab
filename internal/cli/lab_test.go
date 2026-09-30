package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLabInvalidNameIsAUsageError(t *testing.T) {
	t.Setenv("FORKLAB_HOME", t.TempDir())
	for _, args := range [][]string{
		{"lab", "show", "../x"},
		{"lab", "delete", "Bad"},
		{"lab", "create", "a/b", "--profile", "simd", "--version", "0.53.8"},
	} {
		if code, _, stderr := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, stderr)
		}
	}
}

func TestLabListKeepsBrokenLabsAndDeleteRemovesThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORKLAB_HOME", home)
	broken := filepath.Join(home, "labs", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "lab.yaml"), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run("lab", "list", "--json")
	if code != 0 {
		t.Fatalf("lab list: exit %d: %s", code, stderr)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 || env.Data[0]["name"] != "broken" || env.Data[0]["error"] == nil || env.Data[0]["dir"] != broken {
		t.Fatalf("lab list rows = %v, want the broken lab with name, dir, and error", env.Data)
	}
	if code, _, stderr := run("lab", "delete", "broken"); code != 0 {
		t.Fatalf("lab delete broken: exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Fatalf("broken lab still exists: %v", err)
	}
}
