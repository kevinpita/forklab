package chain

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// SDK 0.47 prints query responses with gogoproto jsonpb: Any values carry
// @type and their fields inline, and single-object queries print the object
// without a wrapper. These inputs follow those proto shapes.

func TestDecodersReadSDK047Shapes(t *testing.T) {
	addr, err := parseModuleAccount([]byte(`{"account":{"@type":"/cosmos.auth.v1beta1.ModuleAccount","base_account":{"address":"cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn","pub_key":null,"account_number":"7","sequence":"0"},"name":"gov","permissions":["burner"]}}`))
	if err != nil || addr != "cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn" {
		t.Errorf("module account = %q, %v", addr, err)
	}

	vals, err := parseStakingValidators([]byte(`{"validators":[{"operator_address":"cosmosvaloper1x","consensus_pubkey":{"@type":"/cosmos.crypto.ed25519.PubKey","key":"fWiDqtQ0Gs00ga3YjpIXEIMxIqzTqAh29ZK6RISp2QM="},"jailed":true,"status":"BOND_STATUS_UNBONDING","tokens":"1000","delegator_shares":"1000.000000000000000000","description":{"moniker":"node0","identity":"","website":"","security_contact":"","details":""}}],"pagination":{"next_key":null,"total":"0"}}`))
	want := []StakingValidator{{Operator: "cosmosvaloper1x", Moniker: "node0", ConsensusPubKey: "fWiDqtQ0Gs00ga3YjpIXEIMxIqzTqAh29ZK6RISp2QM=", Status: "UNBONDING", Jailed: true, Tokens: "1000"}}
	if err != nil || !reflect.DeepEqual(vals, want) {
		t.Errorf("validators = %+v, %v", vals, err)
	}

	plan, err := parseUpgradePlan([]byte(`{"name":"v2","time":"0001-01-01T00:00:00Z","height":"120","info":"","upgraded_client_state":null}`))
	if err != nil || plan == nil || *plan != (Plan{Name: "v2", Height: 120}) {
		t.Errorf("plan = %+v, %v", plan, err)
	}

	tally, err := parseTally([]byte(`{"yes_count":"5","abstain_count":"0","no_count":"1","no_with_veto_count":"0"}`))
	if err != nil || tally != (Tally{Yes: "5", No: "1", Abstain: "0", NoWithVeto: "0"}) {
		t.Errorf("tally = %+v, %v", tally, err)
	}

	p, err := parseProposal([]byte(`{"id":"3","messages":[{"@type":"/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade","authority":"cosmos1gov","plan":{"name":"v2","height":"120"}}],"status":"PROPOSAL_STATUS_VOTING_PERIOD","final_tally_result":{"yes_count":"0","abstain_count":"0","no_count":"0","no_with_veto_count":"0"},"submit_time":"2026-09-30T04:03:09Z","total_deposit":[{"denom":"stake","amount":"10"}],"voting_end_time":"2026-09-30T04:03:39Z","metadata":"","title":"up","summary":"s","proposer":"cosmos1p"}`))
	if err != nil || p.ID != 3 || p.Status != "VOTING_PERIOD" || !reflect.DeepEqual(p.Messages, []string{"/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade"}) {
		t.Errorf("proposal = %+v, %v", p, err)
	}

	params, err := parseParams("staking", []byte(`{"unbonding_time":"1814400s","max_validators":100,"max_entries":7,"historical_entries":10000,"bond_denom":"stake","min_commission_rate":"0.000000000000000000"}`))
	if err != nil || params["bond_denom"] != "stake" || params["unbonding_time"] != "1814400s" {
		t.Errorf("params = %v, %v", params, err)
	}
	wrapped, err := parseParams("staking", cliFixture(t, "simd", "staking_params.json"))
	if err != nil || wrapped["unbonding_time"] != "504h0m0s" {
		t.Errorf("0.50 params = %v, %v", wrapped, err)
	}
}

func TestNoUpgradeScheduledIsNoPlan(t *testing.T) {
	bin, _ := fakeBinaryStderr(t, nil, "Error: no upgrade scheduled", 1)
	plan, err := testCLI(bin).UpgradePlan(context.Background())
	if err != nil || plan != nil {
		t.Errorf("plan = %+v, %v; want none", plan, err)
	}
	bin, _ = fakeBinaryStderr(t, nil, "Error: post failed: connection refused", 1)
	if _, err := testCLI(bin).UpgradePlan(context.Background()); err == nil {
		t.Error("another failure read as no plan")
	}
}

func TestLimitFlagFollowsTheBinaryHelp(t *testing.T) {
	for _, tt := range []struct{ help, want string }{
		{"      --page-limit uint   pagination limit", "--page-limit"},
		{"      --limit uint        pagination limit of all validators to query for", "--limit"},
	} {
		bin, args := fakeBinary(t, []byte(tt.help), 0)
		if got := testCLI(bin).limitFlag(context.Background()); got != tt.want {
			t.Errorf("help %q: flag %s, want %s", tt.help, got, tt.want)
		}
		if a := recordedArgs(t, args); !reflect.DeepEqual(a, []string{"q", "staking", "validators", "--help"}) {
			t.Errorf("probe args = %q", a)
		}
	}
}

func TestExecNeedsNode(t *testing.T) {
	for args, want := range map[string]bool{
		"q bank balances x":    true,
		"query gov params":     true,
		"tx bank send a b 1":   true,
		"status":               true,
		"q":                    false,
		"q --help":             false,
		"tx":                   false,
		"keys list":            false,
		"comet show-validator": false,
		"":                     false,
	} {
		if got := ExecNeedsNode(strings.Fields(args)); got != want {
			t.Errorf("ExecNeedsNode(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestCheckUpgradeHeight(t *testing.T) {
	// 30s of voting at 1s blocks from height 100 ends near height 130.
	if err := CheckUpgradeHeight(131, 100, time.Second, 30*time.Second); err != nil {
		t.Errorf("31 blocks out: %v", err)
	}
	err := CheckUpgradeHeight(120, 100, time.Second, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "height 120 is 20 blocks away") || !strings.Contains(err.Error(), "at least 130") {
		t.Errorf("20 blocks out: %v", err)
	}
	// Slow blocks make a close height fine.
	if err := CheckUpgradeHeight(120, 100, 2*time.Second, 30*time.Second); err != nil {
		t.Errorf("20 blocks of 2s: %v", err)
	}
}
