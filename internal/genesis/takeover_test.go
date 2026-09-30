package genesis_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
)

// touchedByTakeover lists the modules Takeover may rewrite; every other
// module must come out byte-identical.
var touchedByTakeover = []string{"staking", "slashing", "distribution", "auth", "bank"}

func TestTakeoverChosenValidatorsHoldThePower(t *testing.T) {
	for _, fixture := range exportFixtures {
		input := fixtureBytes(t, fixture)
		before := decodeBytes(t, input)
		reduction := powerReduction(t, before)
		prefix := bech32Prefix(fieldStr(before.list("app_state.staking.validators")[0], "operator_address"))
		bonded := len(bondedValidators(before))
		for _, n := range []int{1, 2, 4} {
			t.Run(fmt.Sprintf("%s/N=%d", fixture, n), func(t *testing.T) {
				g := load(t, fixture)
				keys := nodeKeys[:n]
				in := forkInput(t, fixture, n)
				in.Accounts = append(in.Accounts, testAccounts(prefix)...)
				report := mustTakeover(t, g, in)
				after := decode(t, g)
				checkInvariants(t, after, reduction)

				share := powerShare(t, after, keys)
				if n == bonded && share.Cmp(big.NewRat(1, 1)) != 0 {
					t.Errorf("all validators chosen but they hold %s of the power", share.FloatString(4))
				}
				if share.Cmp(big.NewRat(9, 10)) < 0 {
					t.Errorf("chosen validators hold %s of the consensus power, want >= 0.9", share.FloatString(4))
				}
				lastPowers := map[string]*big.Int{}
				for _, lp := range after.list("app_state.staking.last_validator_powers") {
					lastPowers[fieldStr(lp, "address")] = bigStr(t, fieldStr(lp, "power"))
				}
				held := new(big.Int)
				for _, cv := range report.Validators {
					held.Add(held, lastPowers[cv.Operator])
				}
				if stakingShare := new(big.Rat).SetFrac(held, bigStr(t, after.str("app_state.staking.last_total_power"))); stakingShare.Cmp(share) != 0 {
					t.Errorf("staking power share %s differs from consensus share %s", stakingShare.FloatString(4), share.FloatString(4))
				}

				var largest []string
				sorted := slices.Clone(bondedValidators(before))
				slices.SortStableFunc(sorted, func(a, b map[string]any) int {
					return bigStr(t, fieldStr(b, "tokens")).Cmp(bigStr(t, fieldStr(a, "tokens")))
				})
				for _, v := range sorted[:n] {
					largest = append(largest, fieldStr(v, "operator_address"))
				}
				var chosen []string
				for _, cv := range report.Validators {
					chosen = append(chosen, cv.Operator)
				}
				if !slices.Equal(chosen, largest) {
					t.Errorf("chosen %v, want the %d largest bonded %v", chosen, n, largest)
				}

				tokensBefore := map[string]*big.Int{}
				for _, v := range before.list("app_state.staking.validators") {
					tokensBefore[fieldStr(v, "operator_address")] = bigStr(t, fieldStr(v, "tokens"))
				}
				// The gov account owns every boost, so its vote alone clears
				// the mainnet default quorum of 33.4% on every chosen validator.
				govValues := govStake(t, after, in.GovDelegator.Address)
				govTotal := new(big.Rat)
				for _, cv := range report.Validators {
					value, ok := govValues[cv.Operator]
					if !ok {
						if cv.Tokens.Cmp(tokensBefore[cv.Operator]) != 0 {
							t.Errorf("boosted validator %s has no delegation from the gov account", cv.Operator)
						}
						continue
					}
					govTotal.Add(govTotal, value)
				}
				if govShare := new(big.Rat).Quo(govTotal, new(big.Rat).SetInt(report.BondedTokens)); govShare.Cmp(big.NewRat(334, 1000)) < 0 {
					t.Errorf("gov account holds %s of the bonded stake, below the 33.4%% default quorum", govShare.FloatString(3))
				}
				var powers []*big.Int
				for i, cv := range report.Validators {
					powers = append(powers, cv.Power)
					if cv.Tokens.Cmp(tokensBefore[cv.Operator]) < 0 {
						t.Errorf("validator %s lost tokens: %s -> %s", cv.Operator, tokensBefore[cv.Operator], cv.Tokens)
					}
					if want := genesis.Valcons(prefix+"valcons", keys[i].PubKey); cv.ConsAddr != want {
						t.Errorf("node %d cons address %s, want %s", i, cv.ConsAddr, want)
					}
				}
				// Even split, with the explicit gov amount on node0 only.
				for i, cv := range report.Validators[1:] {
					if cv.Tokens.Cmp(report.Validators[1].Tokens) != 0 {
						t.Errorf("node%d holds %s tokens, node1 %s: not an even split", i+1, cv.Tokens, report.Validators[1].Tokens)
					}
				}
				if n > 1 {
					if diff := new(big.Int).Sub(report.Validators[0].Tokens, report.Validators[1].Tokens); diff.Cmp(in.GovDelegator.Amount) != 0 {
						t.Errorf("node0 holds %s more than node1, want the gov amount %s (powers %v)", diff, in.GovDelegator.Amount, powers)
					}
				}
				if report.PowerReduction.Cmp(reduction) != 0 {
					t.Errorf("inferred power reduction %s, fixture uses %s", report.PowerReduction, reduction)
				}

				output, err := g.Bytes()
				if err != nil {
					t.Fatal(err)
				}
				modulesUnchanged(t, input, output, touchedByTakeover...)
				if after.str("chain_id") != before.str("chain_id") {
					t.Errorf("chain_id changed to %s", after.str("chain_id"))
				}
				if got, want := after.str("initial_height"), fmt.Sprint(before.at("initial_height")); got != want {
					t.Errorf("initial_height %q, want %q", got, want)
				}
				sameLayout := (len(after.list("consensus.validators")) > 0) == (len(before.list("consensus.validators")) > 0)
				if !sameLayout {
					t.Errorf("validator set moved between .consensus.validators and .validators")
				}
			})
		}
	}
}

func TestTakeoverConsensusSetUsesNodeKeys(t *testing.T) {
	g := load(t, "simd_export.json")
	mustTakeover(t, g, forkInput(t, "simd_export.json", 2))
	after := decode(t, g)
	for _, k := range nodeKeys[:2] {
		found := false
		for _, cv := range consensusSet(after) {
			if fieldStr(cv, "pub_key.value") == k.PubKey {
				found = true
				if fieldStr(cv, "address") != consAddrHex(k.PubKey) {
					t.Errorf("address %s for key %s, want %s", fieldStr(cv, "address"), k.PubKey, consAddrHex(k.PubKey))
				}
				if fieldStr(cv, "pub_key.type") != "tendermint/PubKeyEd25519" {
					t.Errorf("key type %s", fieldStr(cv, "pub_key.type"))
				}
			}
		}
		if !found {
			t.Errorf("node key %s missing from the consensus set", k.PubKey)
		}
	}
	for _, v := range after.list("app_state.staking.validators") {
		if key := fieldStr(v, "consensus_pubkey.key"); slices.ContainsFunc(nodeKeys[:2], func(k genesis.NodeKey) bool { return k.PubKey == key }) {
			if fieldStr(v, "consensus_pubkey.@type") != "/cosmos.crypto.ed25519.PubKey" {
				t.Errorf("staking key type %s", fieldStr(v, "consensus_pubkey.@type"))
			}
			if jailed, _ := v["jailed"].(bool); jailed {
				t.Errorf("chosen validator %s is jailed", fieldStr(v, "operator_address"))
			}
		}
	}
}

func TestTakeoverKeepsALargerValidatorWhole(t *testing.T) {
	// exrpd: 800M, 120M, 50M, 30M. With N=2 the 90% target (360M) is below
	// the largest validator, so both chosen end at 800M and the largest one
	// is not boosted.
	g := load(t, "exrpd_export.json")
	in := forkInput(t, "exrpd_export.json", 2)
	report := mustTakeover(t, g, in)
	// node0 is not boosted; it only carries the explicit gov amount.
	want := new(big.Int).Add(big.NewInt(800_000_000), in.GovDelegator.Amount)
	if got := report.Validators[0].Tokens; got.Cmp(want) != 0 {
		t.Errorf("largest validator now has %s tokens, want %s (unchanged plus the gov amount)", got, want)
	}
	if got := report.Validators[1].Tokens.String(); got != "800000000" {
		t.Errorf("second validator now has %s tokens, want 800000000", got)
	}
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
	for _, v := range after.list("app_state.staking.validators") {
		if fieldStr(v, "operator_address") == report.Validators[0].Operator && fieldStr(v, "delegator_shares") != want.String()+".000000000000000000" {
			t.Errorf("delegator_shares of the unboosted validator is %s, want %s", fieldStr(v, "delegator_shares"), want)
		}
	}
}

func TestTakeoverRemapsSigningInfos(t *testing.T) {
	g := load(t, "simd_export.json")
	before := decode(t, g)
	oldSigners := map[string]bool{}
	for _, si := range before.list("app_state.slashing.signing_infos") {
		oldSigners[fieldStr(si, "address")] = true
	}
	report := mustTakeover(t, g, forkInput(t, "simd_export.json", 2))
	after := decode(t, g)
	infos := after.list("app_state.slashing.signing_infos")
	if len(infos) != len(oldSigners) {
		t.Fatalf("%d signing infos, want %d", len(infos), len(oldSigners))
	}
	chosen := map[string]bool{}
	for _, cv := range report.Validators {
		chosen[cv.ConsAddr] = true
	}
	moved := 0
	for _, si := range infos {
		addr := fieldStr(si, "address")
		if !chosen[addr] {
			if !oldSigners[addr] {
				t.Errorf("unexpected signing info %s", addr)
			}
			continue
		}
		moved++
		info := doc(si)
		if info.str("validator_signing_info.address") != addr {
			t.Errorf("inner address %s differs from %s", info.str("validator_signing_info.address"), addr)
		}
		if info.str("validator_signing_info.jailed_until") != "1970-01-01T00:00:00Z" || info.at("validator_signing_info.tombstoned") != false ||
			info.str("validator_signing_info.missed_blocks_counter") != "0" || info.str("validator_signing_info.start_height") != "0" {
			t.Errorf("signing info %s not reset: %v", addr, si)
		}
	}
	if moved != 2 {
		t.Errorf("%d signing infos moved to the node addresses, want 2", moved)
	}
	for _, mb := range after.list("app_state.slashing.missed_blocks") {
		if chosen[fieldStr(mb, "address")] && len(doc(mb).list("missed_blocks")) != 0 {
			t.Errorf("missed blocks of %s not cleared", fieldStr(mb, "address"))
		}
	}
}

func TestTakeoverCreatesMissingSigningInfo(t *testing.T) {
	g := load(t, "simd_export.json")
	// Drop the signing info of the largest validator, which node0 takes over.
	var largest map[string]any
	for _, v := range bondedValidators(decode(t, g)) {
		if largest == nil || bigStr(t, fieldStr(v, "tokens")).Cmp(bigStr(t, fieldStr(largest, "tokens"))) > 0 {
			largest = v
		}
	}
	drop := genesis.Valcons("cosmosvalcons", fieldStr(largest, "consensus_pubkey.key"))
	if err := genesis.ApplyPatches(g, []string{fmt.Sprintf(`.app_state.slashing.signing_infos |= map(select(.address != %q))`, drop)}); err != nil {
		t.Fatal(err)
	}
	if len(decode(t, g).list("app_state.slashing.signing_infos")) != 3 {
		t.Fatalf("fixture setup: signing info %s not removed", drop)
	}
	report := mustTakeover(t, g, forkInput(t, "simd_export.json", 1))
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
	found := false
	for _, si := range after.list("app_state.slashing.signing_infos") {
		found = found || fieldStr(si, "address") == report.Validators[0].ConsAddr
	}
	if !found {
		t.Errorf("no signing info created for %s", report.Validators[0].ConsAddr)
	}
}

func TestTakeoverGovDelegation(t *testing.T) {
	for _, fixture := range exportFixtures {
		t.Run(fixture, func(t *testing.T) {
			g := load(t, fixture)
			before := decode(t, g)
			reduction := powerReduction(t, before)
			bondDenom := before.str("app_state.staking.params.bond_denom")
			prefix := bech32Prefix(fieldStr(before.list("app_state.staking.validators")[0], "operator_address"))
			gov := testAddr(prefix, 0xa1)
			// 12.345678 whole tokens, whatever the chain's decimals
			amount := new(big.Int).Quo(new(big.Int).Mul(big.NewInt(12_345_678), reduction), big.NewInt(1_000_000))
			report := mustTakeover(t, g, genesis.TakeoverInput{
				Validators:   nodeKeys[:2],
				Accounts:     []genesis.Account{{Address: gov, Balances: []genesis.Coin{{Denom: bondDenom, Amount: big.NewInt(1)}}}},
				GovDelegator: &genesis.GovDelegator{Address: gov, Amount: amount},
			})
			after := decode(t, g)
			checkInvariants(t, after, reduction)
			if diff := new(big.Int).Sub(report.Validators[0].Tokens, report.Validators[1].Tokens); diff.Cmp(amount) != 0 {
				t.Errorf("node0 holds %s more tokens than node1, want the gov amount %s on top of the even boost", diff, amount)
			}
			tokensBefore := map[string]*big.Int{}
			for _, v := range before.list("app_state.staking.validators") {
				tokensBefore[fieldStr(v, "operator_address")] = bigStr(t, fieldStr(v, "tokens"))
			}
			values := govStake(t, after, gov)
			for i, cv := range report.Validators {
				// The gov account owns exactly what was added to the validator.
				want := new(big.Int).Sub(cv.Tokens, tokensBefore[cv.Operator])
				value, ok := values[cv.Operator]
				if !ok {
					t.Fatalf("no delegation from %s to node%d validator %s", gov, i, cv.Operator)
				}
				if diff := new(big.Rat).Sub(value, new(big.Rat).SetInt(want)); diff.Abs(diff).Cmp(big.NewRat(1, 1)) >= 0 {
					t.Errorf("gov delegation to node%d is worth %s tokens, want %s", i, value.FloatString(3), want)
				}

				var starting map[string]any
				for _, si := range after.list("app_state.distribution.delegator_starting_infos") {
					if fieldStr(si, "delegator_address") == gov && fieldStr(si, "validator_address") == cv.Operator {
						starting = si
					}
				}
				if starting == nil {
					t.Fatalf("no delegator starting info for the gov delegation to node%d", i)
				}
				var period string
				for _, cr := range after.list("app_state.distribution.validator_current_rewards") {
					if fieldStr(cr, "validator_address") == cv.Operator {
						period = fmt.Sprint(field(cr, "rewards.period"))
					}
				}
				wantPrevious := new(big.Int).Sub(bigStr(t, period), big.NewInt(1)).String()
				if got := fieldStr(starting, "starting_info.previous_period"); got != wantPrevious {
					t.Errorf("previous_period %s, want %s (current period %s minus one)", got, wantPrevious, period)
				}
				stake, _ := new(big.Rat).SetString(fieldStr(starting, "starting_info.stake"))
				if diff := new(big.Rat).Sub(stake, value); stake == nil || diff.Abs(diff).Cmp(big.NewRat(1, 1_000_000_000)) > 0 {
					t.Errorf("starting stake %s, want the delegation value %s", fieldStr(starting, "starting_info.stake"), value.FloatString(18))
				}
				if got := fieldStr(starting, "starting_info.height"); got != after.str("initial_height") {
					t.Errorf("starting height %s, want initial_height %s", got, after.str("initial_height"))
				}
				refBefore, refAfter := referenceCount(t, before, cv.Operator, wantPrevious), referenceCount(t, after, cv.Operator, wantPrevious)
				if refAfter != refBefore+1 {
					t.Errorf("reference_count of %s period %s went %d -> %d, want +1", cv.Operator, wantPrevious, refBefore, refAfter)
				}
			}
			if got := after.str("app_state.distribution.previous_proposer"); got != report.Validators[0].ConsAddr {
				t.Errorf("previous_proposer %s, want node0 %s", got, report.Validators[0].ConsAddr)
			}
		})
	}
}

func referenceCount(t *testing.T, d doc, operator, period string) int64 {
	t.Helper()
	for _, hr := range d.list("app_state.distribution.validator_historical_rewards") {
		if fieldStr(hr, "validator_address") == operator && fmt.Sprint(hr["period"]) == period {
			n, _ := field(hr, "rewards.reference_count").(json.Number).Int64()
			return n
		}
	}
	t.Fatalf("no historical rewards for %s period %s", operator, period)
	return 0
}

func TestTakeoverOlderValidatorLayout(t *testing.T) {
	g := load(t, "simd_export.json")
	if err := genesis.ApplyPatches(g, []string{`.validators = .consensus.validators | del(.consensus.validators)`}); err != nil {
		t.Fatal(err)
	}
	mustTakeover(t, g, forkInput(t, "simd_export.json", 2))
	after := decode(t, g)
	checkInvariants(t, after, big.NewInt(1_000_000))
	if after.at("consensus.validators") != nil {
		t.Error("consensus.validators was created")
	}
	if len(after.list("validators")) != 4 {
		t.Errorf("top-level validators has %d entries, want 4", len(after.list("validators")))
	}
}

func TestTakeoverChainID(t *testing.T) {
	g := load(t, "simd_export.json")
	in := forkInput(t, "simd_export.json", 1)
	in.ChainID = "lab-1"
	mustTakeover(t, g, in)
	if got := decode(t, g).str("chain_id"); got != "lab-1" {
		t.Errorf("chain_id %s, want lab-1", got)
	}
}

func TestTakeoverErrors(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		input   genesis.TakeoverInput
		want    string
	}{
		{"more nodes than bonded validators", "simd_export.json", genesis.TakeoverInput{Validators: append(slices.Clone(nodeKeys), genesis.NodeKey{PubKey: "pF9UXWMZDDKG5QlLhLMYVZ25OyNrYXlOJdXZLmuO9oo="})}, "only 4 bonded"},
		{"no validators", "simd_export.json", genesis.TakeoverInput{}, "at least one"},
		{"bad key", "simd_export.json", genesis.TakeoverInput{Validators: []genesis.NodeKey{{PubKey: "nope"}}}, "validator 0"},
		{"duplicate key", "simd_export.json", genesis.TakeoverInput{Validators: []genesis.NodeKey{nodeKeys[0], nodeKeys[0]}}, "duplicate"},
		{"fresh genesis has no last powers", "simd_fresh.json", genesis.TakeoverInput{Validators: nodeKeys[:1]}, "last_validator_powers"},
		{"gov delegator without account", "simd_export.json", genesis.TakeoverInput{Validators: nodeKeys[:1], GovDelegator: &genesis.GovDelegator{Address: testAddr("cosmos", 0xee), Amount: big.NewInt(1)}}, "no account"},
		{"no gov delegator", "simd_export.json", genesis.TakeoverInput{Validators: nodeKeys[:1], GovDelegator: nil}, "gov delegator is required"},
		{"zero gov amount", "simd_export.json", genesis.TakeoverInput{Validators: nodeKeys[:1], GovDelegator: &genesis.GovDelegator{Address: testAddr("cosmos", 0xa1), Amount: big.NewInt(0)}}, "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.input
			if in.GovDelegator == nil && tc.name != "no gov delegator" {
				in.GovDelegator = forkInput(t, "simd_export.json", 1).GovDelegator
			}
			_, err := genesis.Takeover(load(t, tc.fixture), in)
			errContains(t, err, tc.want)
		})
	}
}

func TestBech32MatchesFixtureAddresses(t *testing.T) {
	// The signing info of each simd validator sits at the bech32 encoding of
	// sha256(consensus pubkey)[:20]; both directions must agree with the fixture.
	d := decodeBytes(t, fixtureBytes(t, "simd_export.json"))
	signers := map[string]bool{}
	for _, si := range d.list("app_state.slashing.signing_infos") {
		addr := fieldStr(si, "address")
		signers[addr] = true
		hrp, data, err := genesis.Bech32Decode(addr)
		if err != nil || hrp != "cosmosvalcons" || len(data) != 20 {
			t.Errorf("decode %s: hrp %q, %d bytes, err %v", addr, hrp, len(data), err)
		}
	}
	for _, v := range d.list("app_state.staking.validators") {
		if addr := genesis.Valcons("cosmosvalcons", fieldStr(v, "consensus_pubkey.key")); !signers[addr] {
			t.Errorf("computed %s for %s, not in the fixture", addr, fieldStr(v, "operator_address"))
		}
	}
	if _, _, err := genesis.Bech32Decode("cosmosvalcons1yfuqpdz4q9950wq2l8mxeth4q8498spkekqm35"); err == nil {
		t.Error("corrupted checksum accepted")
	}
}
