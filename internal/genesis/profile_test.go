package genesis_test

import (
	"maps"
	"reflect"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
	"github.com/kevinpita/forklab/internal/profile"
)

func TestXRPLEVMForkPatchesPreserveAllowances(t *testing.T) {
	p, err := (profile.Store{Dir: t.TempDir()}).Get("xrplevm")
	if err != nil {
		t.Fatal(err)
	}
	const lower = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const checksum = "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE"
	const ibcPair = "0x55A7Fc91A3Bf505b0136d84A21A875ABD1987D0e"
	for _, missing := range []bool{false, true} {
		name := "mainnet allowances"
		if missing {
			name = "no allowances field"
		}
		t.Run(name, func(t *testing.T) {
			g := patched(t, "exrpd_export.json", `.app_state.erc20 = {
  "native_precompiles": ["`+checksum+`"],
  "token_pairs": [{"erc20_address":"`+lower+`"}, {"erc20_address":"`+ibcPair+`"}],
  "allowances": [
    {"erc20_address":"`+checksum+`", "owner":"0xOwner", "spender":"0xSpender", "value":"115792089237316195423570985008687907853269984665640564039457584007913129639935"},
    {"erc20_address":"0x55a7fc91a3bf505b0136d84a21a875abd1987d0e", "owner":"0xOwner", "spender":"0xSpender", "value":"999"},
    {"erc20_address":"0xUnknown", "owner":"0xOwner", "spender":"0xSpender", "value":"1"}
  ]
}`)
			if missing {
				if err := genesis.ApplyPatches(g, []string{`del(.app_state.erc20.allowances)`}); err != nil {
					t.Fatal(err)
				}
			}
			before := decode(t, g)
			if err := genesis.ApplyPatches(g, p.Profile.ForkPatches); err != nil {
				t.Fatal(err)
			}
			after := decode(t, g)
			if !reflect.DeepEqual(before.at("app_state.erc20.token_pairs"), after.at("app_state.erc20.token_pairs")) {
				t.Fatal("token pairs changed")
			}
			if missing {
				if after.at("app_state.erc20.allowances") != nil {
					t.Fatal("absent allowances were created")
				}
				return
			}
			allowances := after.list("app_state.erc20.allowances")
			wantAddresses := []string{lower, ibcPair, "0xUnknown"}
			if len(allowances) != len(wantAddresses) {
				t.Fatalf("got %d allowances, want %d", len(allowances), len(wantAddresses))
			}
			for i, raw := range allowances {
				if got := fieldStr(raw, "erc20_address"); got != wantAddresses[i] {
					t.Errorf("allowance %d address = %s, want %s", i, got, wantAddresses[i])
				}
				old := maps.Clone(before.list("app_state.erc20.allowances")[i])
				updated := maps.Clone(raw)
				delete(old, "erc20_address")
				delete(updated, "erc20_address")
				if !reflect.DeepEqual(old, updated) {
					t.Errorf("allowance %d data changed", i)
				}
			}
		})
	}
}
