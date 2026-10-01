package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskprogress "github.com/kevinpita/forklab/internal/progress"
)

func TestLabProgressOptInKeepsFinalJSONSeparate(t *testing.T) {
	home, err := os.MkdirTemp("", "forklab-progress-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("FORKLAB_HOME", home)
	t.Setenv("FORKLAB_CONFIG_DIR", t.TempDir())
	bin := filepath.Join(home, "simd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 0.53.8; exit 0; fi\necho 'fixture init failed' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"lab", "create", "demo", "--profile", "simd", "--version", "0.53.8", "--validators", "1", "--binary", "0.53.8=path:" + bin, "--json"}
	code, stdout, stderr := run(args...)
	if code == 0 || stderr != "" {
		t.Fatalf("ordinary JSON: code %d stderr %q", code, stderr)
	}
	assertSingleJSON(t, stdout)
	code, stdout, stderr = run(append(args, "--progress=json")...)
	if code == 0 {
		t.Fatal("fixture must fail")
	}
	assertSingleJSON(t, stdout)
	humanArgs := append(append([]string{}, args[:len(args)-1]...), "--progress=json")
	if code, _, stderr := run(humanArgs...); code == 0 || strings.Contains(stderr, "\r") || strings.Contains(stderr, "forking ") {
		t.Fatalf("structured stderr mixed human output: %d %q", code, stderr)
	}
	var events []taskprogress.Event
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		e, ok := taskprogress.Decode([]byte(line))
		if !ok {
			t.Fatalf("non-progress stderr %q", line)
		}
		events = append(events, e)
	}
	if len(events) == 0 || events[len(events)-1].Phase != "lab.nodes" || events[len(events)-1].State != taskprogress.Started {
		t.Fatalf("failed stage: %+v", events)
	}
	if code, stdout, _ := run(append(args, "--progress=pretty")...); code != 2 || !strings.Contains(stdout, "--progress must be json") {
		t.Fatalf("invalid progress: %d %s", code, stdout)
	}
}

func assertSingleJSON(t *testing.T, s string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&v); err != io.EOF {
		t.Fatalf("extra stdout record: %v", err)
	}
}
