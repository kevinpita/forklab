package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/runbook"
)

func TestRunbookWriteShowRoundTripAndValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "case.yaml")
	document := `{"version":1,"name":"a test","vars":{"amount":"10"},"steps":[{"id":"first","query":["bank","balances","{{ .accounts.val1.address }}"]},{"assert":".steps.first != null"}]}`
	code, out, stderr := run("runbook", "write", file, "--document", document, "--json")
	if code != 0 {
		t.Fatalf("write %d out=%s stderr=%s", code, out, stderr)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), "version: 1") {
		t.Fatalf("YAML=%s err=%v", data, err)
	}
	code, out, stderr = run("runbook", "show", file, "--json")
	var env struct {
		OK   bool             `json:"ok"`
		Data runbook.Document `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || code != 0 || !env.OK || len(env.Data.Steps) != 2 {
		t.Fatalf("show=%s stderr=%s err=%v", out, stderr, err)
	}
	if code, _, _ := run("runbook", "write", file, "--document", document); code == 0 {
		t.Fatal("overwrote existing YAML without force")
	}
	if code, _, _ := run("runbook", "write", file, "--document", `{"version":1,"steps":[{"assert":"unknown_function"}]}`, "--force"); code != 2 {
		t.Fatalf("bad document code=%d", code)
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(data) {
		t.Fatal("invalid recipe changed the file")
	}
	if code, _, _ := run("runbook", "validate", file); code != 0 {
		t.Fatal("saved recipe not valid")
	}
}

func TestRunbookFailureWritesOneFailedEnvelopeAndReport(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, false)
	file := filepath.Join(t.TempDir(), "case.yaml")
	if err := os.WriteFile(file, []byte("version: 1\nsteps:\n - id: failed\n   assert: 'false'\n - query: [bank, total]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	code, out, _ := run("runbook", "run", file, "--lab", dir, "--report", reportPath, "--json")
	var env struct {
		OK   bool           `json:"ok"`
		Data runbook.Report `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || code != 1 || env.OK || len(env.Data.Steps) != 1 || env.Data.Error == "" {
		t.Fatalf("code=%d envelope=%s err=%v", code, out, err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil || !strings.Contains(string(data), "failed") {
		t.Fatalf("report=%s err=%v", data, err)
	}
}

func TestReportDirectoryRejectedBeforeScriptRuns(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, false)
	recipe := filepath.Join(t.TempDir(), "case.yaml")
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	doc := runbook.Document{Version: 1, Steps: []runbook.Step{{Script: []string{"touch", marker}}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run("runbook", "write", recipe, "--document", string(data)); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := run("runbook", "run", recipe, "--lab", dir, "--report", t.TempDir()); code == 0 || !strings.Contains(stderr, "regular file") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("script ran before report preflight: %v", err)
	}
}

func TestRunbookWritePreservesIntegerVariableTypeAndValue(t *testing.T) {
	file := filepath.Join(t.TempDir(), "numbers.yaml")
	document := `{"version":1,"vars":{"large":9007199254740993,"nested":{"n":1000000}},"steps":[{"pause":1000000}]}`
	if code, _, stderr := run("runbook", "write", file, "--document", document); code != 0 {
		t.Fatal(stderr)
	}
	code, out, stderr := run("runbook", "show", file, "--json")
	if code != 0 || !strings.Contains(out, `"large":9007199254740993`) || !strings.Contains(out, `"n":1000000`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
}
