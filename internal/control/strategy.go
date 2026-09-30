// Package control pauses consensus using stock chain binary capabilities.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// HaltStrategy isolates the chain-specific meaning of its halt-height flag.
// A strategy must prevent committing H+1 and leave read-only RPC available.
type HaltStrategy interface {
	Arguments(context.Context, string, int64) ([]string, error)
	Marker(int64) string
}

// SDKFinalizeHalt supports SDK 0.50 and 0.53: halt-height is checked
// before FinalizeBlock, and CometBFT keeps serving queries after the failure.
// Older SDKs terminate the process at Commit and need a different strategy.
type SDKFinalizeHalt struct{}

var sdkVersion = regexp.MustCompile(`^v?0\.(50|53)\.`)

func (SDKFinalizeHalt) Arguments(ctx context.Context, bin string, height int64) ([]string, error) {
	if height < 1 || height == math.MaxInt64 {
		return nil, fmt.Errorf("pause height must be in 1..%d", int64(math.MaxInt64-1))
	}
	out, err := exec.CommandContext(ctx, bin, "version", "--long", "--output", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("exact pause unsupported: cannot read SDK version from %s: %w", bin, err)
	}
	var version struct {
		SDK string `json:"cosmos_sdk_version"`
	}
	if err := json.Unmarshal(out, &version); err != nil || !sdkVersion.MatchString(version.SDK) {
		return nil, fmt.Errorf("exact pause unsupported for SDK %q; supported halt strategy requires SDK 0.50 or 0.53", version.SDK)
	}
	help, err := exec.CommandContext(ctx, bin, "start", "--help").Output()
	if err != nil || !strings.Contains(string(help), "--halt-height ") {
		return nil, fmt.Errorf("exact pause unsupported: %s has no halt-height flag", bin)
	}
	return []string{"--halt-height", strconv.FormatInt(height+1, 10)}, nil
}

func (SDKFinalizeHalt) Marker(height int64) string {
	return "halt per configuration height " + strconv.FormatInt(height+1, 10) + " time"
}
