package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposalWriteOfflineNoClobberAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.json")
	document := `{"messages":[{"@type":"/custom.Msg","amount":9007199254740993}],"title":"Title","summary":"Summary","metadata":"ipfs://CID","deposit":"10stake"}`
	code, out, stderr := run("gov", "write", path, "--document", document, "--json")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	before, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(before), "9007199254740993") {
		t.Fatalf("%s %v", before, err)
	}
	if code, _, _ := run("gov", "write", path, "--document", document); code == 0 {
		t.Fatal("overwrote file")
	}
	for _, bad := range []string{`{}`, document + ` {}`, strings.Replace(document, `"@type":"/custom.Msg"`, `"oops":true`, 1)} {
		if code, _, _ := run("gov", "write", path, "--document", bad, "--force"); code != 2 {
			t.Fatalf("invalid doc exit=%d", code)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatal("invalid draft changed file")
		}
	}
	replacement := strings.Replace(document, "Title", "Changed", 1)
	if code, _, _ := run("gov", "write", path, "--document", replacement, "--force"); code != 0 {
		t.Fatal("force failed")
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "Changed") {
		t.Fatal(string(after))
	}
}

func TestProposalExportCloneAndTemplateSaveDoNotBroadcast(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, true)
	script := `#!/bin/sh
case "$1 $2 $3" in
"q gov proposal") echo '{"proposal":{"id":"21","title":"Title","summary":"Summary","metadata":"ipfs://CID","status":"PROPOSAL_STATUS_VOTING_PERIOD","messages":[{"@type":"/custom.Msg","amount":9007199254740993}],"total_deposit":[{"denom":"stake","amount":"900"}]}}' ;;
"q gov params") echo '{"params":{"min_deposit":[{"denom":"stake","amount":"10"}]}}' ;;
"q gov tally") echo '{"tally":{"yes_count":"42","no_count":"0","abstain_count":"0","no_with_veto_count":"0"}}' ;;
*) echo unexpected-broadcast >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "chaind"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("gov", "draft", "21", "--lab", dir, "--json")
	if code != 0 || !strings.Contains(out, `"deposit":"10stake"`) || !strings.Contains(out, "9007199254740993") {
		t.Fatalf("draft %d %s %s", code, out, stderr)
	}
	path := filepath.Join(t.TempDir(), "record.json")
	if code, out, stderr := run("gov", "export", "21", path, "--lab", dir); code != 0 {
		t.Fatalf("export %d %s %s", code, out, stderr)
	}
	record, _ := os.ReadFile(path)
	if !strings.Contains(string(record), `"yes": "42"`) || !strings.Contains(string(record), "9007199254740993") {
		t.Fatal(string(record))
	}
	if code, _, _ := run("gov", "export", "21", path, "--lab", dir); code == 0 {
		t.Fatal("export clobbered file")
	}
	path = filepath.Join(t.TempDir(), "new.json")
	if code, out, stderr := run("gov", "submit", "--template", "text", "--output", path, "--auto-vote", "--lab", dir); code != 0 {
		t.Fatalf("save %d %s %s", code, out, stderr)
	}
	if code, out, _ := run("gov", "submit", "--template", "text", "--output", "", "--lab", dir); code != 2 {
		t.Fatalf("empty output %d %s", code, out)
	}
}
