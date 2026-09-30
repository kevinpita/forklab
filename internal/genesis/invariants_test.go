package genesis_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
)

// exportFixtures are real exports: simd v0.53.8 (1e6 reduction,
// .consensus.validators), exrpd 11.1.1 (1e6 reduction, 18-decimal denom), and
// the simd export scaled to a 1e18 reduction with the older top-level
// .validators layout.
var exportFixtures = []string{"simd_export.json", "exrpd_export.json", "evm1e18_export.json"}

// nodeKeys are ed25519 public keys of throwaway lab nodes.
var nodeKeys = []genesis.NodeKey{
	{PubKey: "BEfKP1A2X+A4MGqJyoIkKnBHMMEwp2jeTfThuT+fDPk="},
	{PubKey: "XpdqdGK4Lz0Y5GhK+Sizli43/cmH4bSk6p9u9Cf3zbA="},
	{PubKey: "Cg8q3tgHYuGOZ5U8qRW442vgmUvc1t2xJo5ygtdDYhg="},
	{PubKey: "WCbxAomRaMsaloIPzMd35sBLL+KC6tYf/EYAwuWOWUY="},
}

// powerReduction reads tokens/power from the first last_validator_powers entry
// of an untouched export, where tokens are exact multiples.
func powerReduction(t *testing.T, d doc) *big.Int {
	t.Helper()
	tokens := map[string]string{}
	for _, v := range d.list("app_state.staking.validators") {
		tokens[fieldStr(v, "operator_address")] = fieldStr(v, "tokens")
	}
	for _, lp := range d.list("app_state.staking.last_validator_powers") {
		return new(big.Int).Quo(bigStr(t, tokens[fieldStr(lp, "address")]), bigStr(t, fieldStr(lp, "power")))
	}
	t.Fatal("fixture has no last_validator_powers")
	return nil
}

func consAddrHex(pubKeyB64 string) string {
	raw, _ := base64.StdEncoding.DecodeString(pubKeyB64)
	sum := sha256.Sum256(raw)
	return strings.ToUpper(hex.EncodeToString(sum[:20]))
}

// consensusSet returns the CometBFT validator set wherever the layout puts it.
func consensusSet(d doc) []map[string]any {
	if set := d.list("consensus.validators"); len(set) > 0 {
		return set
	}
	return d.list("validators")
}

// bondedValidators returns the staking validators with BOND_STATUS_BONDED.
func bondedValidators(d doc) []map[string]any {
	var out []map[string]any
	for _, v := range d.list("app_state.staking.validators") {
		if fieldStr(v, "status") == "BOND_STATUS_BONDED" {
			out = append(out, v)
		}
	}
	return out
}

// checkInvariants asserts what the SDK checks at InitChain or in its module
// invariants: supply equals the sum of balances, the bonded pool holds the
// bonded tokens, delegations sum to delegator_shares, the CometBFT set equals
// the bonded staking set at tokens/reduction and is mirrored where CometBFT
// reads it, last_validator_powers match, account numbers are unique, every
// bonded ed25519 validator has a signing info, initial_height is a string.
func checkInvariants(t *testing.T, d doc, reduction *big.Int) {
	t.Helper()
	sums := map[string]*big.Int{}
	for _, b := range d.list("app_state.bank.balances") {
		for _, c := range doc(b).list("coins") {
			denom := fieldStr(c, "denom")
			cur, ok := sums[denom]
			if !ok {
				cur = new(big.Int)
			}
			sums[denom] = cur.Add(cur, bigStr(t, fieldStr(c, "amount")))
		}
	}
	supply := d.list("app_state.bank.supply")
	if len(supply) != len(sums) {
		t.Errorf("supply has %d denoms, balances have %d", len(supply), len(sums))
	}
	for _, c := range supply {
		denom := fieldStr(c, "denom")
		if got := fieldStr(c, "amount"); sums[denom] == nil || got != sums[denom].String() {
			t.Errorf("supply of %s is %s, balances sum to %s", denom, got, sums[denom])
		}
	}

	bondDenom := d.str("app_state.staking.params.bond_denom")
	bondedTokens := new(big.Int)
	bonded := bondedValidators(d)
	byPubKey := map[string]map[string]any{}
	for _, v := range bonded {
		bondedTokens.Add(bondedTokens, bigStr(t, fieldStr(v, "tokens")))
		byPubKey[fieldStr(v, "consensus_pubkey.key")] = v
	}
	poolAddr := moduleAccount(t, d, "bonded_tokens_pool")
	poolBalance := "0"
	for _, b := range d.list("app_state.bank.balances") {
		if fieldStr(b, "address") == poolAddr {
			for _, c := range doc(b).list("coins") {
				if fieldStr(c, "denom") == bondDenom {
					poolBalance = fieldStr(c, "amount")
				}
			}
		}
	}
	if poolBalance != bondedTokens.String() {
		t.Errorf("bonded pool holds %s%s, bonded validators hold %s", poolBalance, bondDenom, bondedTokens)
	}

	delegated := map[string]*big.Rat{}
	for _, del := range d.list("app_state.staking.delegations") {
		op := fieldStr(del, "validator_address")
		shares, ok := new(big.Rat).SetString(fieldStr(del, "shares"))
		if !ok {
			t.Fatalf("bad shares %q", fieldStr(del, "shares"))
		}
		if delegated[op] == nil {
			delegated[op] = new(big.Rat)
		}
		delegated[op].Add(delegated[op], shares)
	}
	for _, v := range d.list("app_state.staking.validators") {
		op := fieldStr(v, "operator_address")
		want, _ := new(big.Rat).SetString(fieldStr(v, "delegator_shares"))
		if got := delegated[op]; got == nil || got.Cmp(want) != 0 {
			t.Errorf("delegations to %s sum to %v, delegator_shares is %s", op, got, fieldStr(v, "delegator_shares"))
		}
	}

	set := consensusSet(d)
	if len(set) != len(bonded) {
		t.Errorf("consensus set has %d validators, staking has %d bonded", len(set), len(bonded))
	}
	total := new(big.Int)
	for _, cv := range set {
		pub := fieldStr(cv, "pub_key.value")
		v, ok := byPubKey[pub]
		if !ok {
			t.Errorf("consensus validator %s is not a bonded staking validator", pub)
			continue
		}
		want := new(big.Int).Quo(bigStr(t, fieldStr(v, "tokens")), reduction)
		if got := fieldStr(cv, "power"); got != want.String() {
			t.Errorf("consensus power of %s is %s, tokens/reduction is %s", fieldStr(v, "operator_address"), got, want)
		}
		total.Add(total, want)
		if fieldStr(cv, "pub_key.type") == "tendermint/PubKeyEd25519" && fieldStr(cv, "address") != consAddrHex(pub) {
			t.Errorf("consensus address of %s is %s, sha256(pubkey)[:20] is %s", pub, fieldStr(cv, "address"), consAddrHex(pub))
		}
	}
	if inner := d.list("consensus.validators"); len(inner) > 0 {
		if top := d.list("validators"); len(top) != len(inner) || fieldStr(top[0], "address") != fieldStr(inner[0], "address") {
			t.Errorf("top-level validators (%d) do not mirror consensus.validators (%d)", len(top), len(inner))
		}
		if d.at("consensus_params") == nil {
			t.Error("consensus.params not mirrored to consensus_params")
		}
	}
	lastPowers := d.list("app_state.staking.last_validator_powers")
	if len(lastPowers) != len(bonded) {
		t.Errorf("last_validator_powers has %d entries, %d validators are bonded", len(lastPowers), len(bonded))
	}
	tokensByOperator := map[string]string{}
	for _, v := range bonded {
		tokensByOperator[fieldStr(v, "operator_address")] = fieldStr(v, "tokens")
	}
	for _, lp := range lastPowers {
		want := new(big.Int).Quo(bigStr(t, tokensByOperator[fieldStr(lp, "address")]), reduction)
		if got := fieldStr(lp, "power"); got != want.String() {
			t.Errorf("last power of %s is %s, want %s", fieldStr(lp, "address"), got, want)
		}
	}
	if got := d.str("app_state.staking.last_total_power"); got != total.String() {
		t.Errorf("last_total_power is %s, powers sum to %s", got, total)
	}

	numbers := map[string]string{}
	for _, a := range d.list("app_state.auth.accounts") {
		addr, num := accountIdentity(a)
		if other, dup := numbers[num]; dup {
			t.Errorf("account number %s used by %s and %s", num, other, addr)
		}
		numbers[num] = addr
	}

	if d.at("app_state.slashing") != nil {
		signers := map[string]bool{}
		hrp := ""
		for _, si := range d.list("app_state.slashing.signing_infos") {
			signers[fieldStr(si, "address")] = true
			hrp, _, _ = strings.Cut(fieldStr(si, "address"), "1")
		}
		for _, v := range bonded {
			if fieldStr(v, "consensus_pubkey.@type") != "/cosmos.crypto.ed25519.PubKey" {
				continue
			}
			if addr := genesis.Valcons(hrp, fieldStr(v, "consensus_pubkey.key")); !signers[addr] {
				t.Errorf("bonded validator %s has no signing info at %s", fieldStr(v, "operator_address"), addr)
			}
		}
	}

	if _, ok := d.at("initial_height").(string); !ok {
		t.Errorf("initial_height is %T, want a JSON string", d.at("initial_height"))
	}
}

// powerShare returns the share of the CometBFT set's power held by the given keys.
func powerShare(t *testing.T, d doc, keys []genesis.NodeKey) *big.Rat {
	t.Helper()
	mine := map[string]bool{}
	for _, k := range keys {
		mine[k.PubKey] = true
	}
	held, total := new(big.Int), new(big.Int)
	for _, cv := range consensusSet(d) {
		p := bigStr(t, fieldStr(cv, "power"))
		total.Add(total, p)
		if mine[fieldStr(cv, "pub_key.value")] {
			held.Add(held, p)
		}
	}
	if total.Sign() == 0 {
		t.Fatal("consensus set has no power")
	}
	return new(big.Rat).SetFrac(held, total)
}

func mustTakeover(t *testing.T, g *genesis.Genesis, in genesis.TakeoverInput) *genesis.TakeoverReport {
	t.Helper()
	report, err := genesis.Takeover(g, in)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// forkInput is the input a lab uses on a fixture: n node keys and a gov
// account that delegates 12.345678 whole tokens to node0 on top of the boost.
func forkInput(t *testing.T, fixture string, n int) genesis.TakeoverInput {
	t.Helper()
	d := decodeBytes(t, fixtureBytes(t, fixture))
	gov := testAddr(accountPrefix(d), 0xa1)
	bond := d.str("app_state.staking.params.bond_denom")
	amount := new(big.Int).Quo(new(big.Int).Mul(big.NewInt(12_345_678), powerReduction(t, d)), big.NewInt(1_000_000))
	return genesis.TakeoverInput{
		Validators:   nodeKeys[:n],
		Accounts:     []genesis.Account{{Address: gov, Balances: []genesis.Coin{{Denom: bond, Amount: big.NewInt(1)}}}},
		GovDelegator: &genesis.GovDelegator{Address: gov, Amount: amount},
	}
}

// govStake returns the token value of every delegation from gov, by
// validator, at each validator's exchange rate.
func govStake(t *testing.T, d doc, gov string) map[string]*big.Rat {
	t.Helper()
	validators := map[string]map[string]any{}
	for _, v := range d.list("app_state.staking.validators") {
		validators[fieldStr(v, "operator_address")] = v
	}
	out := map[string]*big.Rat{}
	for _, del := range d.list("app_state.staking.delegations") {
		if fieldStr(del, "delegator_address") != gov {
			continue
		}
		v := validators[fieldStr(del, "validator_address")]
		shares, _ := new(big.Rat).SetString(fieldStr(del, "shares"))
		total, _ := new(big.Rat).SetString(fieldStr(v, "delegator_shares"))
		value := new(big.Rat).Mul(shares, new(big.Rat).SetInt(bigStr(t, fieldStr(v, "tokens"))))
		out[fieldStr(del, "validator_address")] = value.Quo(value, total)
	}
	return out
}

func testAccounts(prefix string) []genesis.Account {
	return []genesis.Account{
		{Address: testAddr(prefix, 0xb1), Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(100)}}},
		{Address: testAddr(prefix, 0xb2), Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(5)}, {Denom: "axrp", Amount: big.NewInt(7)}}},
	}
}
