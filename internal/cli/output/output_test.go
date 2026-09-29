package output_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/kevinpita/forklab/internal/cli/output"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want output.Code
	}{
		{"nil", nil, output.CodeOK},
		{"plain", errors.New("boom"), output.CodeError},
		{"usage", output.Usagef("missing --profile"), output.CodeUsage},
		{"wrapped usage", fmt.Errorf("lab create: %w", output.Usagef("missing --profile")), output.CodeUsage},
		{"lab not running", output.ErrLabNotRunning, output.CodeLabNotRunning},
		{"wrapped lab not running", fmt.Errorf("lab %q: %w", "dev", output.ErrLabNotRunning), output.CodeLabNotRunning},
		{"usage of nil", output.Usage(nil), output.CodeOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := output.ExitCode(tt.err); got != tt.want {
				t.Errorf("ExitCode = %d, want %d", got, tt.want)
			}
		})
	}
}

type fakeResult struct {
	Name string `json:"name"`
}

func (f fakeResult) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "name is %s\n", f.Name)
	return err
}

func TestPrint(t *testing.T) {
	tests := []struct {
		name   string
		asJSON bool
		result output.Result
		want   string
	}{
		{"json envelope", true, fakeResult{Name: "node0"}, `{"ok":true,"data":{"name":"node0"}}` + "\n"},
		{"human", false, fakeResult{Name: "node0"}, "name is node0\n"},
		{"json nil result", true, nil, `{"ok":true,"data":{}}` + "\n"},
		{"human nil result", false, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := output.Print(&buf, tt.asJSON, tt.result); err != nil {
				t.Fatal(err)
			}
			if buf.String() != tt.want {
				t.Errorf("got %q, want %q", buf.String(), tt.want)
			}
		})
	}
}

func TestPrintError(t *testing.T) {
	tests := []struct {
		name       string
		asJSON     bool
		err        error
		wantCode   output.Code
		wantStdout string
		wantStderr string
	}{
		{
			name:       "json lab not running",
			asJSON:     true,
			err:        fmt.Errorf("lab %q: %w", "dev", output.ErrLabNotRunning),
			wantCode:   output.CodeLabNotRunning,
			wantStdout: `{"ok":false,"error":{"code":"lab_not_running","message":"lab \"dev\": lab not running"}}` + "\n",
		},
		{
			name:       "json generic",
			asJSON:     true,
			err:        errors.New("boom"),
			wantCode:   output.CodeError,
			wantStdout: `{"ok":false,"error":{"code":"error","message":"boom"}}` + "\n",
		},
		{
			name:       "human usage",
			err:        output.Usagef("missing --profile"),
			wantCode:   output.CodeUsage,
			wantStderr: "Error: missing --profile\nRun with --help for usage.\n",
		},
		{
			name:       "human generic",
			err:        errors.New("boom"),
			wantCode:   output.CodeError,
			wantStderr: "Error: boom\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := output.PrintError(&stdout, &stderr, tt.asJSON, tt.err)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}
