package genesis_test

import (
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
)

func TestApplyPatches(t *testing.T) {
	input := fixtureBytes(t, "evm1e18_export.json")
	g := load(t, "evm1e18_export.json")
	patches := []string{
		`.app_state.slashing.params.signed_blocks_window = "7"`,
		`.app_state.mint.minter.inflation = "0.000000000000000000"`,
	}
	if err := genesis.ApplyPatches(g, patches); err != nil {
		t.Fatal(err)
	}
	after := decode(t, g)
	if got := after.str("app_state.slashing.params.signed_blocks_window"); got != "7" {
		t.Errorf("signed_blocks_window %s", got)
	}
	if got := after.str("app_state.mint.minter.inflation"); got != "0.000000000000000000" {
		t.Errorf("inflation %s", got)
	}
	if got := after.str("app_state.bank.supply"); got != "" {
		t.Errorf("supply is a string: %s", got)
	}
	if got := fieldStr(after.list("app_state.bank.supply")[0], "amount"); got != "240000328000000000000" {
		t.Errorf("1e18-scale supply lost precision: %s", got)
	}
	output, _ := g.Bytes()
	modulesUnchanged(t, input, output, "slashing", "mint")
}

func TestEmptyPatcherLeavesGenesisUnchanged(t *testing.T) {
	g := load(t, "simd_export.json")
	before, err := g.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var patcher genesis.Patcher = genesis.JQPatcher(nil)
	if err := patcher.Apply(g); err != nil {
		t.Fatal(err)
	}
	after, err := g.Bytes()
	if err != nil || string(before) != string(after) {
		t.Fatalf("empty patcher changed genesis: %v", err)
	}
}

func TestApplyPatchesInitialHeightStaysAString(t *testing.T) {
	g := load(t, "simd_export.json")
	if err := genesis.ApplyPatches(g, []string{`.initial_height = (.initial_height | tonumber) + 1`}); err != nil {
		t.Fatal(err)
	}
	if got := decode(t, g).at("initial_height"); got != "84" {
		t.Errorf("initial_height %v (%T), want the string \"84\"", got, got)
	}
}

func TestApplyPatchesErrorsNameThePatch(t *testing.T) {
	g := load(t, "simd_export.json")
	errContains(t, genesis.ApplyPatches(g, []string{`.chain_id = "ok"`, `.app_state.bank.supply[0].amount | tonumber |`}), "patch 1")
	errContains(t, genesis.ApplyPatches(g, []string{`.app_state.gov.params.quorum | tonumber | error("boom")`}), "patch 0")
	errContains(t, genesis.ApplyPatches(g, []string{`.app_state.bank.balances[]`}), "patch 0")
	errContains(t, genesis.ApplyPatches(g, []string{`empty`}), "patch 0")
	errContains(t, genesis.ApplyPatches(g, []string{`"not a document"`}), "patch")
	errContains(t, genesis.ApplyPatches(g, []string{`del(.app_state)`}), "app_state")
	if got := decode(t, g).str("chain_id"); got != "mainnet-1" {
		t.Errorf("a failed patch list changed the genesis: chain_id %s", got)
	}
}

func TestApplyPatchesKeepsNumberPrecision(t *testing.T) {
	g := patched(t, "simd_export.json",
		`.app_state.mint.minter.annual_provisions = 12345678901234567890123`,
		`.app_state.slashing.params.signed_blocks_window = 9007199254740993`,
	)
	out, err := g.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"annual_provisions":12345678901234567890123`, `"signed_blocks_window":9007199254740993`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %s", want)
		}
	}
}
