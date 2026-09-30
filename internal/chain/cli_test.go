package chain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// The fixtures in testdata/<chain>/cli are real output of stock simd v0.53.8
// and exrpd 11.1.1 on a two-validator fresh lab.

func cliFixture(t *testing.T, chain, name string) []byte {
	t.Helper()
	return []byte(readFixture(t, filepath.Join(chain, "cli", name)))
}

var chains = []string{"simd", "exrpd"}

func TestParseBalances(t *testing.T) {
	want := map[string]Coins{
		"simd":  {{Denom: "stake", Amount: "10000000000000000000000"}},
		"exrpd": {{Denom: "apoa", Amount: "10000000000000000000000"}, {Denom: "axrp", Amount: "1000000000000000000000000000"}},
	}
	for _, c := range chains {
		got, err := parseBalances(cliFixture(t, c, "bank_balances.json"))
		if err != nil || !reflect.DeepEqual(got, want[c]) {
			t.Errorf("%s: balances = %v, %v; want %v", c, got, err, want[c])
		}
	}
	if got, err := parseBalances([]byte(`{"balances":[],"pagination":{}}`)); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty balances = %#v, %v; want an empty non-nil list", got, err)
	}
}

func TestCoinsString(t *testing.T) {
	cs := Coins{{Denom: "apoa", Amount: "1"}, {Denom: "axrp", Amount: "20"}}
	if got := cs.String(); got != "1apoa,20axrp" {
		t.Errorf("String = %q", got)
	}
}

func TestParseUpgradePlan(t *testing.T) {
	for _, c := range chains {
		got, err := parseUpgradePlan(cliFixture(t, c, "upgrade_plan_none.json"))
		if err != nil || got != nil {
			t.Errorf("%s: no plan = %+v, %v; want nil", c, got, err)
		}
	}
	got, err := parseUpgradePlan(cliFixture(t, "exrpd", "upgrade_plan.json"))
	want := &Plan{Name: "v-next", Height: 99, Info: "forklab"}
	if err != nil || got == nil || got.Name != want.Name || got.Info != want.Info || got.Height <= 0 {
		t.Errorf("plan = %+v, %v; want %+v with a height", got, err, want)
	}
}

func TestParseModuleAccount(t *testing.T) {
	want := map[string]string{
		"simd":  "cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn",
		"exrpd": "ethm10d07y265gmmuvt4z0w9aw880jnsr700jpva843",
	}
	for _, c := range chains {
		got, err := parseModuleAccount(cliFixture(t, c, "module_account_gov.json"))
		if err != nil || got != want[c] {
			t.Errorf("%s: gov address = %q, %v; want %q", c, got, err, want[c])
		}
	}
	if _, err := parseModuleAccount([]byte(`{}`)); err == nil {
		t.Error("an account without an address parsed")
	}
}

func TestParseGovParams(t *testing.T) {
	denom := map[string]string{"simd": "stake", "exrpd": "axrp"}
	for _, c := range chains {
		got, err := parseGovParams(cliFixture(t, c, "gov_params.json"))
		want := GovParams{
			MinDeposit:          Coins{{Denom: denom[c], Amount: "1"}},
			ExpeditedMinDeposit: Coins{{Denom: denom[c], Amount: "2"}},
			VotingPeriod:        "30s", ExpeditedVotingPeriod: "20s",
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: gov params = %+v, %v; want %+v", c, got, err, want)
		}
	}
}

func TestParseProposals(t *testing.T) {
	for _, c := range chains {
		got, err := parseProposals(cliFixture(t, c, "gov_proposals.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("%s: %d proposals, want 2", c, len(got))
		}
		text, upgrade := got[0], got[1]
		if text.ID != 1 || text.Status != "PASSED" || text.Title != "hello" || len(text.Messages) != 0 ||
			text.FinalTally != (Tally{Yes: "2000000000000000000000", No: "0", Abstain: "0", NoWithVeto: "0"}) || text.VotingEndTime == nil {
			t.Errorf("%s: text proposal = %+v", c, text)
		}
		if upgrade.ID != 2 || upgrade.Status != "REJECTED" || !reflect.DeepEqual(upgrade.Messages, []string{"/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade"}) {
			t.Errorf("%s: upgrade proposal = %+v", c, upgrade)
		}

		none, err := parseProposals(cliFixture(t, c, "gov_proposals_none.json"))
		if err != nil || none == nil || len(none) != 0 {
			t.Errorf("%s: no proposals = %#v, %v; want an empty non-nil list", c, none, err)
		}
	}
}

func TestParseProposalAndTally(t *testing.T) {
	for _, c := range chains {
		p, err := parseProposal(cliFixture(t, c, "gov_proposal.json"))
		if err != nil || p.ID != 2 || p.Status != "VOTING_PERIOD" || p.Title != "upgrade" ||
			!reflect.DeepEqual(p.TotalDeposit, Coins{{Denom: map[string]string{"simd": "stake", "exrpd": "axrp"}[c], Amount: "1"}}) {
			t.Errorf("%s: proposal = %+v, %v", c, p, err)
		}
		tally, err := parseTally(cliFixture(t, c, "gov_tally_final.json"))
		if err != nil || tally != (Tally{Yes: "2000000000000000000000", No: "0", Abstain: "0", NoWithVeto: "0"}) {
			t.Errorf("%s: tally = %+v, %v", c, tally, err)
		}
	}
}

func TestParseStakingValidators(t *testing.T) {
	got, err := parseStakingValidators(cliFixture(t, "simd", "staking_validators.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []StakingValidator{
		{Operator: "cosmosvaloper18a5vtvqx82nfed03rlk6kld7td54qntgq3yupu", Moniker: "node0", ConsensusPubKey: "fWiDqtQ0Gs00ga3YjpIXEIMxIqzTqAh29ZK6RISp2QM=", Status: "BONDED", Tokens: "1000000000000000000000"},
		{Operator: "cosmosvaloper1w7rucegt85yu8sc34m80t6hd5q764n8cn6tcdm", Moniker: "node1", ConsensusPubKey: "jw/FnsXWwoxzjfsCb+qWvb9LheFh6zdxnDWfEc1RONM=", Status: "BONDED", Tokens: "1000000000000000000000"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("validators = %+v\nwant %+v", got, want)
	}
	jailed, err := parseStakingValidators(cliFixture(t, "simd", "staking_validators_jailed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, v := range jailed {
		if v.Jailed {
			n++
			if v.Status == "BONDED" {
				t.Errorf("jailed validator %s is still bonded", v.Moniker)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d jailed validators, want 1: %+v", n, jailed)
	}
}

func TestParseBroadcast(t *testing.T) {
	ok := cliFixture(t, "simd", "broadcast.json")
	hash, err := parseBroadcast(ok)
	if err != nil || hash != "E1FE02F476282F138E18DDAFB5F84B74AA5E7A0124E13500EA42076DC111490D" {
		t.Errorf("hash = %q, %v", hash, err)
	}
	rejected := strings.Replace(strings.Replace(string(ok), `"code":0`, `"code":5`, 1), `"raw_log":""`, `"raw_log":"insufficient funds"`, 1)
	_, err = parseBroadcast([]byte(rejected))
	var txErr *TxError
	if !errors.As(err, &txErr) || txErr.Code != 5 || txErr.Log != "insufficient funds" {
		t.Errorf("rejected broadcast err = %v, want a TxError with code 5", err)
	}
	if _, err := parseBroadcast([]byte("gas estimate: 1234")); err == nil {
		t.Error("non-JSON broadcast output parsed")
	}
}

func TestCommandErrorNamesTheChainError(t *testing.T) {
	// simd prints usage then the error; exrpd prints "Error: " then usage.
	for _, c := range chains {
		e := &CommandError{Args: []string{"tx", "gov", "vote", "2", "yes", "--from", "val0"}, ExitCode: 1, Stderr: string(cliFixture(t, c, "sequence_mismatch.stderr"))}
		msg := e.Error()
		if !strings.HasPrefix(msg, "tx gov vote 2 yes: exit 1: rpc error:") || !strings.Contains(msg, "account sequence mismatch") || strings.Contains(msg, "Usage") {
			t.Errorf("%s: %q", c, msg)
		}
	}
}

func TestExecArgs(t *testing.T) {
	c := CLI{Bin: "simd", Home: "/lab/node0", Node: "tcp://127.0.0.1:26757", ChainID: "lab-1", KeyringDir: "/lab/keys"}
	keyring := []string{"--keyring-backend", "test", "--keyring-dir", "/lab/keys"}
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"query", []string{"q", "bank", "balances", "addr"}, []string{"q", "bank", "balances", "addr", "--home", "/lab/node0", "--node", "tcp://127.0.0.1:26757"}},
		{"query long name", []string{"query", "gov", "params"}, []string{"query", "gov", "params", "--home", "/lab/node0", "--node", "tcp://127.0.0.1:26757"}},
		{"tx", []string{"tx", "bank", "send", "a", "b", "1stake"}, append([]string{"tx", "bank", "send", "a", "b", "1stake", "--home", "/lab/node0", "--node", "tcp://127.0.0.1:26757", "--chain-id", "lab-1"}, keyring...)},
		{"keys", []string{"keys", "list"}, append([]string{"keys", "list", "--home", "/lab/node0"}, keyring...)},
		{"status", []string{"status"}, []string{"status", "--home", "/lab/node0", "--node", "tcp://127.0.0.1:26757"}},
		{"other command gets only home", []string{"comet", "show-validator"}, []string{"comet", "show-validator", "--home", "/lab/node0"}},
		{"user flags win", []string{"q", "bank", "balances", "x", "--node=tcp://remote:1", "--home", "/mine"}, []string{"q", "bank", "balances", "x", "--node=tcp://remote:1", "--home", "/mine"}},
		{"flags after -- are not user flags", []string{"keys", "list", "--", "--home"}, append([]string{"keys", "list", "--", "--home", "--home", "/lab/node0"}, keyring...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.ExecArgs(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExecArgs(%q)\n = %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// fakeBinary is a chain binary that records its args and prints fixture,
// with a gas estimate line on stderr as the SDK prints for --gas auto.
func fakeBinary(t *testing.T, fixture []byte, exit int) (bin, argsFile string) {
	t.Helper()
	return fakeBinaryStderr(t, fixture, "gas estimate: 1", exit)
}

func fakeBinaryStderr(t *testing.T, fixture []byte, stderr string, exit int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(out, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "chaind")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\ncat " + out + "\necho '" + stderr + "' >&2\nexit " + string(rune('0'+exit)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func recordedArgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func testCLI(bin string) CLI {
	return CLI{Bin: bin, Home: "/lab/node0", Node: "tcp://127.0.0.1:26657", ChainID: "lab-1", KeyringDir: "/lab/keys", GasPrices: "0.025stake"}
}

func TestQueryRunsAgainstTheLabNode(t *testing.T) {
	bin, args := fakeBinary(t, cliFixture(t, "simd", "bank_balances.json"), 0)
	got, err := testCLI(bin).Balances(context.Background(), "cosmos1abc")
	if err != nil || len(got) != 1 {
		t.Fatalf("balances = %v, %v", got, err)
	}
	want := []string{"q", "bank", "balances", "cosmos1abc", "--output", "json", "--node", "tcp://127.0.0.1:26657", "--home", "/lab/node0"}
	if a := recordedArgs(t, args); !reflect.DeepEqual(a, want) {
		t.Errorf("args = %q\nwant %q", a, want)
	}
}

func TestBroadcastSignsFromTheLabKeyring(t *testing.T) {
	bin, args := fakeBinary(t, cliFixture(t, "simd", "broadcast.json"), 0)
	hash, err := testCLI(bin).Broadcast(context.Background(), "test0", "bank", "send", "test0", "cosmos1to", "5stake")
	if err != nil || hash == "" {
		t.Fatalf("hash = %q, %v", hash, err)
	}
	want := []string{
		"tx", "bank", "send", "test0", "cosmos1to", "5stake",
		"--from", "test0", "--chain-id", "lab-1", "--node", "tcp://127.0.0.1:26657", "--home", "/lab/node0",
		"--keyring-backend", "test", "--keyring-dir", "/lab/keys",
		"--gas", "auto", "--gas-adjustment", "1.5", "--gas-prices", "0.025stake", "--yes", "--output", "json",
	}
	if a := recordedArgs(t, args); !reflect.DeepEqual(a, want) {
		t.Errorf("args = %q\nwant %q", a, want)
	}
}

func TestFailedRunIsCommandError(t *testing.T) {
	bin, _ := fakeBinary(t, nil, 1)
	_, err := testCLI(bin).GovParams(context.Background())
	var ce *CommandError
	if !errors.As(err, &ce) || ce.ExitCode != 1 || !strings.Contains(err.Error(), "gas estimate: 1") {
		t.Errorf("err = %v, want a CommandError with exit 1 and the stderr line", err)
	}
}

func TestProposalFileMatchesWhatTheChainAccepted(t *testing.T) {
	// The chain accepted this shape in the capture run.
	p := ProposalFile{
		Messages: []any{SoftwareUpgradeMsg("cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn", Plan{Name: "v-next", Height: 63, Info: "forklab"})},
		Deposit:  "1stake", Title: "upgrade", Summary: "upgrade proposal",
	}
	got, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"messages":[{"@type":"/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade","authority":"cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn","plan":{"height":"63","info":"forklab","name":"v-next"}}],"metadata":"","deposit":"1stake","title":"upgrade","summary":"upgrade proposal"}`
	if string(got) != want {
		t.Errorf("proposal JSON\n = %s\nwant %s", got, want)
	}
	p.Expedited = true
	if got, _ := json.Marshal(p); !strings.Contains(string(got), `"expedited":true`) {
		t.Errorf("expedited proposal JSON lacks the flag: %s", got)
	}
	cancel, _ := json.Marshal(CancelUpgradeMsg("cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn"))
	if want := `{"@type":"/cosmos.upgrade.v1beta1.MsgCancelUpgrade","authority":"cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn"}`; string(cancel) != want {
		t.Errorf("cancel JSON = %s, want %s", cancel, want)
	}
}

func TestUpdateParamsMsg(t *testing.T) {
	var q struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(cliFixture(t, "simd", "staking_params.json"), &q); err != nil {
		t.Fatal(err)
	}
	if err := SetParam(q.Params, "max_validators=50"); err != nil {
		t.Fatal(err)
	}
	if err := SetParam(q.Params, "bond_denom=ustake"); err != nil {
		t.Fatal(err)
	}
	msg := UpdateParamsMsg(UpdateParamsMsgType("staking"), "cosmos1gov", q.Params)
	got, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"@type":"/cosmos.staking.v1beta1.MsgUpdateParams","authority":"cosmos1gov","params":{"bond_denom":"ustake","historical_entries":10000,"max_entries":7,"max_validators":50,"min_commission_rate":"0.000000000000000000","unbonding_time":"1814400s"}}`
	if string(got) != want {
		t.Errorf("msg\n = %s\nwant %s", got, want)
	}
	for _, bad := range []string{"max_validator=5", "novalue"} {
		if err := SetParam(q.Params, bad); err == nil {
			t.Errorf("SetParam(%q) accepted", bad)
		}
	}
	if got := UpdateParamsMsgType("gov"); got != "/cosmos.gov.v1.MsgUpdateParams" {
		t.Errorf("gov type = %s", got)
	}
}

func TestWaitTxReadsTheProposalID(t *testing.T) {
	for _, c := range chains {
		t.Run(c, func(t *testing.T) {
			body := readFixture(t, filepath.Join(c, "cli", "rpc_tx_submit_proposal.json"))
			var polls atomic.Int32
			rpc := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/tx" || !strings.HasPrefix(r.URL.Query().Get("hash"), "0x") {
					http.NotFound(w, r)
					return
				}
				if polls.Add(1) < 3 {
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":-1,"error":{"code":-32603,"message":"Internal error","data":"tx (ABC) not found"}}`))
					return
				}
				_, _ = w.Write([]byte(body))
			})
			rpc.pollInterval = 0
			r, err := rpc.WaitTx(context.Background(), "ABC")
			if err != nil {
				t.Fatal(err)
			}
			id, err := ProposalID(r)
			if err != nil || id != 2 || r.Code != 0 || r.Height <= 0 {
				t.Errorf("tx = %+v, id %d, %v; want proposal 2 in a block", r, id, err)
			}
		})
	}
}

func TestWaitTxFailedInBlock(t *testing.T) {
	body := strings.Replace(readFixture(t, "simd/cli/rpc_tx_submit_proposal.json"), `"code":0`, `"code":11`, 1)
	rpc := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	_, err := rpc.WaitTx(context.Background(), "ABC")
	var txErr *TxError
	if !errors.As(err, &txErr) || txErr.Code != 11 {
		t.Errorf("err = %v, want a TxError with code 11", err)
	}
}

func TestBroadcastHonorsExplicitGasAndFees(t *testing.T) {
	for _, input := range [][]string{{"--gas", "200000", "--fees", "10stake"}, {"--gas=200000", "--gas-prices=1stake", "--gas-adjustment=1.2"}} {
		bin, file := fakeBinary(t, cliFixture(t, "simd", "broadcast.json"), 0)
		if _, err := testCLI(bin).Broadcast(context.Background(), "val0", append([]string{"bank", "send", "val0", "addr", "1stake"}, input...)...); err != nil {
			t.Fatal(err)
		}
		args := recordedArgs(t, file)
		for _, unwanted := range []string{"auto", "0.025stake"} {
			for _, arg := range args {
				if arg == unwanted {
					t.Fatalf("defaults override explicit gas or fees: %q", args)
				}
			}
		}
		if !strings.Contains(strings.Join(args, " "), "--yes --output json") {
			t.Fatalf("missing unary flag or output: %q", args)
		}
	}
}
