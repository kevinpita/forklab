package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionJSON(t *testing.T) {
	code, stdout, stderr := run("version", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	var env struct {
		OK   bool        `json:"ok"`
		Data versionInfo `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v: %q", err, stdout)
	}
	want := versionInfo{Version: "dev", Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if !env.OK || env.Data != want {
		t.Errorf("envelope = %+v, want ok with %+v", env, want)
	}
}

func TestVersionHuman(t *testing.T) {
	code, stdout, _ := run("version")
	if code != 0 || !strings.HasPrefix(stdout, "forklab dev (") {
		t.Errorf("code = %d, stdout = %q", code, stdout)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown command", []string{"nope"}},
		{"unknown flag", []string{"version", "--nope"}},
		{"extra arg", []string{"version", "extra"}},
		{"unsupported completion shell", []string{"completion", "nope"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(tt.args...)
			if code != 2 {
				t.Errorf("code = %d, want 2", code)
			}
			if stdout != "" || !strings.HasPrefix(stderr, "Error: ") {
				t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
			}
		})
	}
}

func TestExitCodeFromCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"runtime failure", []string{"fail"}, 1},
		{"lab not running", []string{"fail", "--down"}, 3},
		{"missing required flag", []string{"needs-flag"}, 2},
		{"missing required flag with pre-run hook", []string{"pre-run-needs-flag"}, 2},
		{"failing pre-run hook", []string{"pre-run-fails"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{}
			root := newRootCmd(a)
			var down bool
			fail := &cobra.Command{Use: "fail", RunE: func(*cobra.Command, []string) error {
				if down {
					return fmt.Errorf("lab %q: %w", "dev", output.ErrLabNotRunning)
				}
				return errors.New("boom")
			}}
			fail.Flags().BoolVar(&down, "down", false, "")
			needsFlag := &cobra.Command{Use: "needs-flag", RunE: func(*cobra.Command, []string) error { return nil }}
			needsFlag.Flags().String("profile", "", "")
			_ = needsFlag.MarkFlagRequired("profile")
			preRunNeedsFlag := &cobra.Command{
				Use:     "pre-run-needs-flag",
				PreRunE: func(*cobra.Command, []string) error { return nil },
				RunE:    func(*cobra.Command, []string) error { return nil },
			}
			preRunNeedsFlag.Flags().String("profile", "", "")
			_ = preRunNeedsFlag.MarkFlagRequired("profile")
			preRunFails := &cobra.Command{
				Use:     "pre-run-fails",
				PreRunE: func(*cobra.Command, []string) error { return errors.New("supervisor unreachable") },
				RunE:    func(*cobra.Command, []string) error { return nil },
			}
			root.AddCommand(fail, needsFlag, preRunNeedsFlag, preRunFails)

			if got := execute(a, root, tt.args, io.Discard, io.Discard); got != tt.want {
				t.Errorf("exit code = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFailingPersistentPreRunIsRuntimeError(t *testing.T) {
	a := &app{}
	root := newRootCmd(a)
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return errors.New("no home dir") }
	if got := execute(a, root, []string{"version"}, io.Discard, io.Discard); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}

func TestJSONRequested(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"version", "--json"}, true},
		{[]string{"version", "--json=true"}, true},
		{[]string{"version", "--json=1"}, true},
		{[]string{"version", "--json=false"}, false},
		{[]string{"version", "--json=nope"}, false},
		{[]string{"exec", "--", "exrpd", "--json"}, false},
		{[]string{"version"}, false},
	}
	for _, tt := range tests {
		if got := jsonRequested(tt.args); got != tt.want {
			t.Errorf("jsonRequested(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestUsageErrorJSONWhenParsingStopsBeforeJSONFlag(t *testing.T) {
	code, stdout, stderr := run("version", "--nope", "--json")
	if code != 2 || stderr != "" {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v: %q", err, stdout)
	}
	if env.OK || env.Error.Code != "usage" {
		t.Errorf("envelope = %+v, want usage error", env)
	}
}
