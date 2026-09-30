package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeForkChain stands in for a chain binary: init writes the node files a fork
// needs (FAKE_TESTDATA points at the simd config samples), keys add prints
// a key, help output advertises the modern flags, and everything else
// succeeds.
const fakeForkChain = `#!/bin/sh
cmd=$1 name=$2 key=$3
case " $* " in *" --help "*)
  case "$cmd" in keys) echo "--key-type";; genesis) echo " validate ";; esac
  exit 0;;
esac
while [ $# -gt 0 ]; do case "$1" in --home) home=$2;; esac; shift; done
case "$cmd" in
init)
  mkdir -p "$home/config"
  cp "$FAKE_TESTDATA/config.toml" "$FAKE_TESTDATA/app.toml" "$FAKE_TESTDATA/node_key.json" "$home/config/"
  echo "{\"address\":\"0A8C9CFF520E\",\"pub_key\":{\"type\":\"tendermint/PubKeyEd25519\",\"value\":\"$(eval echo "\$PUBKEY_$name")\"}}" > "$home/config/priv_validator_key.json"
  echo '{"chain_id":"unused","app_state":{}}' > "$home/config/genesis.json";;
keys)
  case "$key" in
  gov) addr=cosmos1gzmusu4l83ndww92l6lqrngvnjlmrnlxlc033f;;
  test0) addr=cosmos1mmnpa2hxpmygt0nclqezlyh0qf28f00yjrnexx;;
  test1) addr=cosmos1an0untkswckdmd0sru5wcxrnlr9uywk0aa3fjv;;
  *) echo "unexpected key $key" >&2; exit 1;;
  esac
  echo "{\"name\":\"$key\",\"address\":\"$addr\",\"mnemonic\":\"words for $key\"}";;
esac
`

func nodePubKey(name string) string {
	sum := sha256.Sum256([]byte(name))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// buildFork runs the builder pipeline in fork mode on the simd export
// fixture and returns the lab config with its partial directory.
func buildFork(t *testing.T, chainID string) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "chaind")
	if err := os.WriteFile(bin, []byte(fakeForkChain), 0o755); err != nil {
		t.Fatal(err)
	}
	testdata, _ := filepath.Abs("testdata/simd")
	t.Setenv("FAKE_TESTDATA", testdata)
	t.Setenv("PUBKEY_node0", nodePubKey("node0"))
	t.Setenv("PUBKEY_node1", nodePubKey("node1"))
	partial := filepath.Join(dir, "fork.partial")
	if err := os.Mkdir(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	p := builtinProfile(t, "simd")
	in := CreateInput{
		Name: "fork", Profile: p, Version: "0.53.8", Validators: 2, TestAccounts: 2, ChainID: chainID,
		Fork: &ForkInput{Source: "mainnet.tar.lz4", Export: func(context.Context, string) (string, string, error) {
			return "/snapshots/mainnet.tar.lz4", "../genesis/testdata/simd_export.json", nil
		}},
	}
	exported, err := loadExport(context.Background(), bin, *in.Fork)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := allocatePorts(2, nil)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	b := builder{in: in, p: p.Profile, dir: filepath.Join(dir, "fork"), partial: partial, ports: ports, exported: exported, cli: chainCLI{ctx: context.Background(), bin: bin, log: &log}}
	c, err := b.build()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	if err := os.WriteFile(filepath.Join(partial, "create.log"), log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return c, partial
}

func TestForkBuildTakesOverTheLargestValidatorsForTheNodes(t *testing.T) {
	c, dir := buildFork(t, "mainnet-1")
	if c.Mode != ModeFork || c.Fork == nil || c.Fork.Height != 83 || c.Fork.Source != "mainnet.tar.lz4" || c.Fork.Archive != "/snapshots/mainnet.tar.lz4" {
		t.Fatalf("mode %s fork %+v, want fork mode from the snapshot at height 83", c.Mode, c.Fork)
	}
	if c.ChainID != "mainnet-1" {
		t.Fatalf("chain id %s, want mainnet-1", c.ChainID)
	}
	var names []string
	for _, a := range c.Accounts {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "gov,test0,test1" {
		t.Fatalf("accounts %v, want gov and test accounts only: fork validators have no operator key", names)
	}
	// The fixture's bonded validators hold 43M, 32M, 20M, and 10M tokens.
	want := []string{"cosmosvaloper1dqn6eejcn9nkezhyhjazk3k82jsqvcn738t8sl", "cosmosvaloper12aupv3ulqapjf4nnav4t8603xeuxtq60nmvph0"}
	for i, n := range c.Nodes {
		if n.Validator != "" || n.Operator != want[i] {
			t.Fatalf("node%d validator %q operator %q, want no key name and operator %s", i, n.Validator, n.Operator, want[i])
		}
	}

	g0 := readGenesis(t, filepath.Join(dir, "node0", "config", "genesis.json"))
	g1 := readGenesis(t, filepath.Join(dir, "node1", "config", "genesis.json"))
	if !bytes.Equal(g0, g1) {
		t.Fatal("node0 and node1 got different genesis files")
	}
	var doc struct {
		ChainID   string `json:"chain_id"`
		Consensus struct {
			Validators []struct {
				PubKey struct{ Value string } `json:"pub_key"`
			} `json:"validators"`
		} `json:"consensus"`
		AppState struct {
			Gov struct {
				Params struct {
					VotingPeriod string `json:"voting_period"`
				} `json:"params"`
			} `json:"gov"`
			Bank struct {
				Balances []struct {
					Address string
					Coins   []struct{ Denom, Amount string }
				} `json:"balances"`
			} `json:"bank"`
			Staking struct {
				Delegations []struct {
					DelegatorAddress string `json:"delegator_address"`
					ValidatorAddress string `json:"validator_address"`
				} `json:"delegations"`
			} `json:"staking"`
		} `json:"app_state"`
	}
	if err := json.Unmarshal(g0, &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, v := range doc.Consensus.Validators {
		keys = append(keys, v.PubKey.Value)
	}
	for _, name := range []string{"node0", "node1"} {
		if !slices.Contains(keys, nodePubKey(name)) {
			t.Fatalf("consensus validators %v lack %s's key", keys, name)
		}
	}
	if doc.AppState.Gov.Params.VotingPeriod != "30s" {
		t.Fatalf("voting period %s, want the profile's 30s", doc.AppState.Gov.Params.VotingPeriod)
	}
	gov := c.Accounts[0].Address
	funded := false
	for _, b := range doc.AppState.Bank.Balances {
		// The fixture supply is 240000328 stake; stake also pays fees, so
		// each lab account gets one percent of it.
		if b.Address == gov && len(b.Coins) == 1 && b.Coins[0].Denom == "stake" && b.Coins[0].Amount == "2400003" {
			funded = true
		}
	}
	if !funded {
		t.Fatalf("gov account %s is not funded with 2400003 stake (1%% of the supply)", gov)
	}
	delegated := map[string]bool{}
	for _, d := range doc.AppState.Staking.Delegations {
		if d.DelegatorAddress == gov {
			delegated[d.ValidatorAddress] = true
		}
	}
	if !delegated[want[0]] || !delegated[want[1]] {
		t.Fatalf("gov delegations %v, want one to each taken-over validator", delegated)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "create.log"))
	for _, want := range []string{"init node0 --chain-id mainnet-1", "node0 takes over " + want[0], "validate --home"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("create.log lacks %q:\n%s", want, log)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes.json")); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mode != ModeFork || loaded.Fork.Height != 83 || loaded.Nodes[1].Operator != want[1] {
		t.Fatalf("lab.yaml round trip lost fork details: %+v", loaded)
	}
}

func TestForkBuildOverridesTheChainID(t *testing.T) {
	c, dir := buildFork(t, "lab-1")
	if c.ChainID != "lab-1" {
		t.Fatalf("chain id %s, want lab-1", c.ChainID)
	}
	var doc struct {
		ChainID string `json:"chain_id"`
	}
	if err := json.Unmarshal(readGenesis(t, filepath.Join(dir, "node0", "config", "genesis.json")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ChainID != "lab-1" {
		t.Fatalf("genesis chain id %s, want lab-1 (the export said mainnet-1)", doc.ChainID)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "create.log"))
	if !strings.Contains(string(log), "init node0 --chain-id lab-1") {
		t.Fatalf("nodes were not initialised with the overriding chain id:\n%s", log)
	}
}

func readGenesis(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
