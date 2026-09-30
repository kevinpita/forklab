package genesis_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/genesis"
)

var labGov = genesis.GovParams{
	VotingPeriod:          30 * time.Second,
	ExpeditedVotingPeriod: 20 * time.Second,
	MinDeposit:            genesis.Coin{Denom: "stake", Amount: big.NewInt(1000)},
}

func checkGovParams(t *testing.T, params doc, expedited bool) {
	t.Helper()
	if got := params.str("voting_period"); got != "30s" {
		t.Errorf("voting_period %s", got)
	}
	if got := params.str("max_deposit_period"); got != "30s" {
		t.Errorf("max_deposit_period %s", got)
	}
	if got := params.str("quorum"); got != "0.000001000000000000" {
		t.Errorf("quorum %s", got)
	}
	if dep := params.list("min_deposit"); len(dep) != 1 || fieldStr(dep[0], "denom") != "stake" || fieldStr(dep[0], "amount") != "1000" {
		t.Errorf("min_deposit %v", params.at("min_deposit"))
	}
	if !expedited {
		if params.at("expedited_voting_period") != nil || params.at("expedited_min_deposit") != nil {
			t.Errorf("expedited fields were added: %v", params)
		}
		return
	}
	if got := params.str("expedited_voting_period"); got != "20s" {
		t.Errorf("expedited_voting_period %s", got)
	}
	if dep := params.list("expedited_min_deposit"); len(dep) != 1 || fieldStr(dep[0], "denom") != "stake" || fieldStr(dep[0], "amount") != "2000" {
		t.Errorf("expedited_min_deposit %v, want twice the min deposit", params.at("expedited_min_deposit"))
	}
}

func TestGovPatch(t *testing.T) {
	for _, fixture := range []string{"simd_export.json", "simd_fresh.json", "exrpd_export.json"} {
		t.Run(fixture, func(t *testing.T) {
			input := fixtureBytes(t, fixture)
			g := load(t, fixture)
			if err := genesis.GovPatch(g, labGov); err != nil {
				t.Fatal(err)
			}
			after := decode(t, g)
			params, _ := after.at("app_state.gov.params").(map[string]any)
			checkGovParams(t, params, true)
			if got, want := after.str("app_state.gov.params.threshold"), decodeBytes(t, input).str("app_state.gov.params.threshold"); got != want {
				t.Errorf("threshold changed %s -> %s", want, got)
			}
			output, _ := g.Bytes()
			modulesUnchanged(t, input, output, "gov")
		})
	}
}

func TestGovPatchWithoutExpeditedFields(t *testing.T) {
	g := load(t, "simd_export.json")
	if err := genesis.ApplyPatches(g, []string{`del(.app_state.gov.params.expedited_voting_period, .app_state.gov.params.expedited_min_deposit, .app_state.gov.params.expedited_threshold)`}); err != nil {
		t.Fatal(err)
	}
	if err := genesis.GovPatch(g, labGov); err != nil {
		t.Fatal(err)
	}
	params, _ := decode(t, g).at("app_state.gov.params").(map[string]any)
	checkGovParams(t, params, false)
}

func TestGovPatchLegacyLayout(t *testing.T) {
	g := load(t, "simd_export.json")
	if err := genesis.ApplyPatches(g, []string{`.app_state.gov |= (
		.voting_params = {voting_period: .params.voting_period}
		| .deposit_params = {min_deposit: .params.min_deposit, max_deposit_period: .params.max_deposit_period}
		| .tally_params = {quorum: .params.quorum, threshold: .params.threshold, veto_threshold: .params.veto_threshold}
		| del(.params))`}); err != nil {
		t.Fatal(err)
	}
	if err := genesis.GovPatch(g, labGov); err != nil {
		t.Fatal(err)
	}
	after := decode(t, g)
	if got := after.str("app_state.gov.voting_params.voting_period"); got != "30s" {
		t.Errorf("voting_period %s", got)
	}
	if got := after.str("app_state.gov.deposit_params.max_deposit_period"); got != "30s" {
		t.Errorf("max_deposit_period %s", got)
	}
	if dep := after.list("app_state.gov.deposit_params.min_deposit"); len(dep) != 1 || fieldStr(dep[0], "amount") != "1000" {
		t.Errorf("min_deposit %v", after.at("app_state.gov.deposit_params.min_deposit"))
	}
	if got := after.str("app_state.gov.tally_params.quorum"); got != "0.000001000000000000" {
		t.Errorf("quorum %s", got)
	}
	if got := after.str("app_state.gov.tally_params.threshold"); got != "0.500000000000000000" {
		t.Errorf("threshold changed to %s", got)
	}
}

func TestGovPatchRejectsBadPeriods(t *testing.T) {
	g := load(t, "simd_export.json")
	bad := labGov
	bad.ExpeditedVotingPeriod = 30 * time.Second
	errContains(t, genesis.GovPatch(g, bad), "expedited")
	bad = labGov
	bad.VotingPeriod = 0
	errContains(t, genesis.GovPatch(g, bad), "voting period")
	bad = labGov
	bad.MinDeposit.Amount = big.NewInt(0)
	errContains(t, genesis.GovPatch(g, bad), "min deposit")
}

func TestGovPatchSubSecondPeriod(t *testing.T) {
	g := load(t, "simd_export.json")
	p := labGov
	p.VotingPeriod = 1500 * time.Millisecond
	p.ExpeditedVotingPeriod = 500 * time.Millisecond
	if err := genesis.GovPatch(g, p); err != nil {
		t.Fatal(err)
	}
	after := decode(t, g)
	if got := after.str("app_state.gov.params.voting_period"); got != "1.5s" {
		t.Errorf("voting_period %s, want 1.5s", got)
	}
	if got := after.str("app_state.gov.params.expedited_voting_period"); got != "0.5s" {
		t.Errorf("expedited_voting_period %s, want 0.5s", got)
	}
}
