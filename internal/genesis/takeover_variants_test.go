package genesis_test

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
)

// Variants of the simd export that the small fixtures do not cover, built
// with ApplyPatches.

func TestTakeoverSkipsUnbondingValidator(t *testing.T) {
	// Move the largest simd validator (43M) to UNBONDING as the SDK would:
	// out of last_validator_powers and the CometBFT set, tokens in the
	// not-bonded pool. It must not be chosen and its pool must stay put.
	before := decodeBytes(t, fixtureBytes(t, "simd_export.json"))
	largest := "cosmosvaloper1dqn6eejcn9nkezhyhjazk3k82jsqvcn738t8sl"
	bondedPool, notBondedPool := moduleAccount(t, before, "bonded_tokens_pool"), moduleAccount(t, before, "not_bonded_tokens_pool")
	g := patched(t, "simd_export.json",
		fmt.Sprintf(`(.app_state.staking.validators[] | select(.operator_address == %q) | .status) = "BOND_STATUS_UNBONDING"`, largest),
		fmt.Sprintf(`.app_state.staking.last_validator_powers |= map(select(.address != %q)) | .app_state.staking.last_total_power = "62"`, largest),
		`.consensus.validators |= map(select(.pub_key.value != "FbibXW+2jhCEVoloJyiQWwyczwjkxYMZv6LoFfgrPVs="))`,
		fmt.Sprintf(`(.app_state.bank.balances[] | select(.address == %q) | .coins[0].amount) = "62000000"`, bondedPool),
		fmt.Sprintf(`.app_state.bank.balances += [{address: %q, coins: [{denom: "stake", amount: "43000000"}]}]`, notBondedPool),
	)
	report := mustTakeover(t, g, forkInput(t, "simd_export.json", 2))
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
	for _, cv := range report.Validators {
		if cv.Operator == largest {
			t.Errorf("unbonding validator %s was chosen", largest)
		}
	}
	for _, b := range after.list("app_state.bank.balances") {
		if fieldStr(b, "address") == notBondedPool && fieldStr(doc(b).list("coins")[0], "amount") != "43000000" {
			t.Errorf("not-bonded pool changed to %v", b["coins"])
		}
	}
	for _, v := range after.list("app_state.staking.validators") {
		if fieldStr(v, "operator_address") == largest && fieldStr(v, "tokens") != "43000000" {
			t.Errorf("unbonding validator tokens changed to %s", fieldStr(v, "tokens"))
		}
	}
}

func TestTakeoverKeepsExchangeRateOfSlashedValidator(t *testing.T) {
	// A slashed validator has fewer tokens than shares. The boost must keep
	// that rate, so existing delegations neither gain nor lose value.
	before := decodeBytes(t, fixtureBytes(t, "simd_export.json"))
	largest := "cosmosvaloper1dqn6eejcn9nkezhyhjazk3k82jsqvcn738t8sl"
	bondedPool := moduleAccount(t, before, "bonded_tokens_pool")
	g := patched(t, "simd_export.json",
		fmt.Sprintf(`(.app_state.staking.validators[] | select(.operator_address == %q) | .tokens) = "40000000"`, largest),
		fmt.Sprintf(`(.app_state.staking.last_validator_powers[] | select(.address == %q) | .power) = "40" | .app_state.staking.last_total_power = "102"`, largest),
		`(.consensus.validators[] | select(.pub_key.value == "FbibXW+2jhCEVoloJyiQWwyczwjkxYMZv6LoFfgrPVs=") | .power) = "40"`,
		fmt.Sprintf(`(.app_state.bank.balances[] | select(.address == %q) | .coins[0].amount) = "102000000"`, bondedPool),
		`.app_state.bank.supply[0].amount = "237000328"`,
	)
	report := mustTakeover(t, g, forkInput(t, "simd_export.json", 1))
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
	if report.Validators[0].Operator != largest {
		t.Fatalf("chose %s", report.Validators[0].Operator)
	}
	for _, v := range after.list("app_state.staking.validators") {
		if fieldStr(v, "operator_address") != largest {
			continue
		}
		tokens := bigStr(t, fieldStr(v, "tokens"))
		shares, _ := new(big.Rat).SetString(fieldStr(v, "delegator_shares"))
		rate := new(big.Rat).Quo(new(big.Rat).SetInt(tokens), shares)
		want := big.NewRat(40, 43)
		diff := new(big.Rat).Sub(rate, want)
		diff.Abs(diff)
		if diff.Cmp(big.NewRat(1, 1_000_000_000)) > 0 {
			t.Errorf("exchange rate moved from %s to %s", want.FloatString(9), rate.FloatString(9))
		}
	}
}

func TestTakeoverWithoutSlashingOrDistribution(t *testing.T) {
	g := patched(t, "simd_export.json", `del(.app_state.slashing, .app_state.distribution)`)
	gov := testAddr("cosmos", 0xa1)
	mustTakeover(t, g, genesis.TakeoverInput{
		Validators:   nodeKeys[:2],
		Accounts:     []genesis.Account{{Address: gov, Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(1)}}}},
		GovDelegator: &genesis.GovDelegator{Address: gov, Amount: big.NewInt(1_000_000)},
	})
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
	if after.at("app_state.slashing") != nil || after.at("app_state.distribution") != nil {
		t.Error("modules were created")
	}
}

func TestTakeoverErrorLeavesGenesisUnchanged(t *testing.T) {
	g := load(t, "simd_export.json")
	before, _ := g.Bytes()
	_, err := genesis.Takeover(g, genesis.TakeoverInput{
		Validators:   nodeKeys[:1],
		Accounts:     testAccounts("cosmos"),
		GovDelegator: &genesis.GovDelegator{Address: testAddr("cosmos", 0xee), Amount: big.NewInt(1)},
	})
	errContains(t, err, "no account")
	after, _ := g.Bytes()
	if string(after) != string(before) {
		t.Error("a failed takeover changed the genesis")
	}
}

func TestTakeoverRejectsGovDelegationBelowQuorum(t *testing.T) {
	// Four equal validators all chosen: no boost, so only the explicit gov
	// amount counts, and 1 token of 100M bonded is below the 1e-6 quorum.
	before := decodeBytes(t, fixtureBytes(t, "simd_export.json"))
	g := patched(t, "simd_export.json",
		`.app_state.staking.validators[] |= (.tokens = "25000000" | .delegator_shares = "25000000.000000000000000000")`,
		`.app_state.staking.last_validator_powers[].power = "25" | .app_state.staking.last_total_power = "100"`,
		`.consensus.validators[].power = "25"`,
		fmt.Sprintf(`(.app_state.bank.balances[] | select(.address == %q) | .coins[0].amount) = "100000000"`, moduleAccount(t, before, "bonded_tokens_pool")),
		`.app_state.bank.supply[0].amount = "235000328"`,
	)
	in := forkInput(t, "simd_export.json", 4)
	in.GovDelegator.Amount = big.NewInt(1)
	_, err := genesis.Takeover(g, in)
	errContains(t, err, "quorum")
	in.GovDelegator.Amount = big.NewInt(1000)
	if _, err := genesis.Takeover(g, in); err != nil {
		t.Fatalf("1000 of 100001000 bonded meets the quorum: %v", err)
	}
}

func TestTakeoverOnForklabOutput(t *testing.T) {
	// A genesis forklab wrote carries the top-level mirror; a second takeover
	// (a new lab from a cached fork) must keep both copies in step.
	g := load(t, "simd_export.json")
	mustTakeover(t, g, forkInput(t, "simd_export.json", 2))
	written, err := g.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	g, err = genesis.Parse(written)
	if err != nil {
		t.Fatal(err)
	}
	in := forkInput(t, "simd_export.json", 1)
	in.Validators = nodeKeys[2:3]
	gov := testAddr("cosmos", 0xc1)
	in.Accounts[0].Address, in.GovDelegator.Address = gov, gov
	mustTakeover(t, g, in)
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
}

func TestBytesRejectsDivergentConsensusCopies(t *testing.T) {
	// A patch that edits the top-level copy instead of .consensus.params
	// must not be silently overwritten by the mirror.
	g := patched(t, "simd_export.json", `.consensus_params = .consensus.params | .consensus_params.block.max_gas = "1"`)
	_, err := g.Bytes()
	errContains(t, err, "consensus_params and consensus.params differ")
	g = patched(t, "simd_export.json", `.consensus_params = .consensus.params | .validators = .consensus.validators`)
	if _, err := g.Bytes(); err != nil {
		t.Fatalf("equal copies rejected: %v", err)
	}
}

func TestBytesMirrorsConsensusForCometBFT(t *testing.T) {
	// SDK nodes that fall back to the CometBFT parser, and chains that use it
	// directly, read consensus_params and validators at the top level.
	g := patched(t, "simd_export.json", `.consensus.params.block.max_gas = "4242"`)
	after := decode(t, g)
	if got := after.str("consensus_params.block.max_gas"); got != "4242" {
		t.Errorf("consensus_params.block.max_gas %q, want the mirrored 4242", got)
	}
	if got, want := len(after.list("validators")), len(after.list("consensus.validators")); got != want || got == 0 {
		t.Errorf("top-level validators has %d entries, consensus.validators %d", got, want)
	}
	if got := after.str("initial_height"); got != "83" {
		t.Errorf("initial_height %q", got)
	}
}
