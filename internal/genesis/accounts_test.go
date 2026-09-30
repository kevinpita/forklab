package genesis_test

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
)

func TestAddAccounts(t *testing.T) {
	for _, fixture := range []string{"simd_fresh.json", "simd_export.json", "exrpd_export.json"} {
		t.Run(fixture, func(t *testing.T) {
			input := fixtureBytes(t, fixture)
			before := decodeBytes(t, input)
			g := load(t, fixture)
			bond := before.str("app_state.staking.params.bond_denom")
			prefix := accountPrefix(before)
			accounts := []genesis.Account{
				{Address: testAddr(prefix, 0xa1), Balances: []genesis.Coin{{Denom: bond, Amount: big.NewInt(100)}, {Denom: "newdenom", Amount: big.NewInt(3)}}},
				{Address: testAddr(prefix, 0xa2), Balances: []genesis.Coin{{Denom: bond, Amount: big.NewInt(5)}}},
				{Address: testAddr(prefix, 0xa3)},
			}
			if err := genesis.AddAccounts(g, accounts); err != nil {
				t.Fatal(err)
			}
			after := decode(t, g)
			supplyBefore := map[string]string{}
			for _, c := range before.list("app_state.bank.supply") {
				supplyBefore[fieldStr(c, "denom")] = fieldStr(c, "amount")
			}
			sums := map[string]*big.Int{}
			for _, b := range after.list("app_state.bank.balances") {
				for _, c := range doc(b).list("coins") {
					n, ok := sums[fieldStr(c, "denom")]
					if !ok {
						n = new(big.Int)
					}
					sums[fieldStr(c, "denom")] = n.Add(n, bigStr(t, fieldStr(c, "amount")))
				}
			}
			for _, c := range after.list("app_state.bank.supply") {
				if got := fieldStr(c, "amount"); got != sums[fieldStr(c, "denom")].String() {
					t.Errorf("supply of %s is %s, balances sum to %s", fieldStr(c, "denom"), got, sums[fieldStr(c, "denom")])
				}
			}
			if len(after.list("app_state.bank.supply")) != len(sums) {
				t.Errorf("supply denoms %d, balance denoms %d", len(after.list("app_state.bank.supply")), len(sums))
			}
			bondBefore := bigStr(t, supplyBefore[bond])
			if got := sums[bond]; new(big.Int).Sub(got, bondBefore).String() != "105" {
				t.Errorf("%s supply went %s -> %s, want +105", bond, bondBefore, got)
			}
			if got := sums["newdenom"]; got == nil || got.String() != "3" {
				t.Errorf("newdenom supply %v, want 3", got)
			}

			numbers := map[string]bool{}
			var maxBefore int64 = -1
			for _, a := range before.list("app_state.auth.accounts") {
				_, num := accountIdentity(a)
				if n := bigStr(t, num).Int64(); n > maxBefore {
					maxBefore = n
				}
			}
			found := map[string]string{}
			for _, a := range after.list("app_state.auth.accounts") {
				addr, num := accountIdentity(a)
				if numbers[num] {
					t.Errorf("duplicate account number %s", num)
				}
				numbers[num] = true
				found[addr] = num
			}
			for i, acct := range accounts {
				want := big.NewInt(maxBefore + 1 + int64(i)).String()
				if found[acct.Address] != want {
					t.Errorf("account %s has number %q, want %s", acct.Address, found[acct.Address], want)
				}
			}
			var emptyBalance bool
			for _, b := range after.list("app_state.bank.balances") {
				emptyBalance = emptyBalance || fieldStr(b, "address") == accounts[2].Address
			}
			if emptyBalance {
				t.Error("an account without balances got a bank entry")
			}
			output, _ := g.Bytes()
			modulesUnchanged(t, input, output, "auth", "bank")
		})
	}
}

func TestAddAccountsRejectsExistingAddress(t *testing.T) {
	g := load(t, "simd_export.json")
	existing := decode(t, g).list("app_state.auth.accounts")[0]
	addr, _ := accountIdentity(existing)
	errContains(t, genesis.AddAccounts(g, []genesis.Account{{Address: addr}}), "already exists")
	errContains(t, genesis.AddAccounts(g, []genesis.Account{{Address: testAddr("cosmos", 1), Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(0)}}}}), "invalid coin")
	errContains(t, genesis.AddAccounts(g, []genesis.Account{{Address: testAddr("cosmos", 1), Balances: []genesis.Coin{{Denom: "a", Amount: big.NewInt(1)}, {Denom: "a", Amount: big.NewInt(1)}}}}), "duplicate denom")
}

func TestAddAccountsRejectsForeignPrefix(t *testing.T) {
	g := load(t, "simd_export.json")
	errContains(t, genesis.AddAccounts(g, []genesis.Account{{Address: testAddr("ethm", 1)}}), `chain uses "cosmos"`)
	errContains(t, genesis.AddAccounts(g, []genesis.Account{{Address: "cosmos1notbech32"}}), "bech32")
}

func TestAddAccountsKeepsEmptySupplyEmpty(t *testing.T) {
	// The bank module computes an empty supply from the balances and panics on
	// a non-empty one that does not match them.
	g := patched(t, "simd_fresh.json", `.app_state.bank.supply = []`)
	if err := genesis.AddAccounts(g, []genesis.Account{{Address: testAddr("cosmos", 7), Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(9)}}}}); err != nil {
		t.Fatal(err)
	}
	after := decode(t, g)
	if supply, _ := after.at("app_state.bank.supply").([]any); len(supply) != 0 {
		t.Errorf("supply became %v, want it left empty", supply)
	}
	if len(after.list("app_state.bank.balances")) != 2 {
		t.Errorf("%d balances, want 2", len(after.list("app_state.bank.balances")))
	}
}

func TestAddAccountsRejectsBalanceWithoutAccount(t *testing.T) {
	// An address can hold coins before it has an auth account; injecting it
	// would give the bank two balance entries, which the SDK rejects.
	addr := testAddr("cosmos", 0xb7)
	g := patched(t, "simd_export.json",
		fmt.Sprintf(`.app_state.bank.balances += [{"address":%q,"coins":[{"denom":"stake","amount":"5"}]}]`, addr),
		`.app_state.bank.supply[0].amount = "240000333"`,
	)
	before, _ := g.Bytes()
	err := genesis.AddAccounts(g, []genesis.Account{{Address: addr, Balances: []genesis.Coin{{Denom: "stake", Amount: big.NewInt(1)}}}})
	errContains(t, err, "already has a bank balance")
	if after, _ := g.Bytes(); string(after) != string(before) {
		t.Error("a rejected injection changed the genesis")
	}
}
