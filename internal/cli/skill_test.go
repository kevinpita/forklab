package cli

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSkill(t *testing.T) {
	want, err := os.ReadFile("../../.agents/skills/forklab/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	code, stdout, stderr := run("skill")
	if code != 0 || stderr != "" {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if stdout != string(want) {
		t.Fatal("skill output differs from the canonical SKILL.md")
	}
	code, stdout, stderr = run("skill", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	var env struct {
		OK   bool      `json:"ok"`
		Data skillInfo `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v", err)
	}
	if !env.OK || env.Data.Name != "forklab" || env.Data.Content != string(want) {
		t.Fatal("JSON output does not contain the forklab skill")
	}
}
