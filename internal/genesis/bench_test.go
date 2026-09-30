//go:build bench

package genesis_test

import (
	"math/big"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/genesis"
)

// TestBenchBigExport times Parse, Takeover, and Bytes on the export named by
// FORKLAB_BIG_GENESIS and reports the Go allocation totals. Peak RSS is a
// process property; measure it with the OS (GNU time -v, /proc, or similar)
// around the test binary.
func TestBenchBigExport(t *testing.T) {
	path := os.Getenv("FORKLAB_BIG_GENESIS")
	if path == "" {
		t.Skip("FORKLAB_BIG_GENESIS not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	g, err := genesis.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parse %d MB: %s", len(data)>>20, time.Since(start).Round(time.Millisecond))
	gov := testAddr("cosmos", 0xa1)
	start = time.Now()
	if _, err := genesis.Takeover(g, genesis.TakeoverInput{
		Validators:   nodeKeys[:2],
		Accounts:     []genesis.Account{{Address: gov, Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(1_000_000)}}}},
		GovDelegator: &genesis.GovDelegator{Address: gov, Amount: big.NewInt(1_000_000)},
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("takeover: %s", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	out, err := g.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bytes %d MB: %s", len(out)>>20, time.Since(start).Round(time.Millisecond))
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("cumulative heap allocation %d MB, memory obtained from the OS %d MB", m.TotalAlloc>>20, m.Sys>>20)
}
