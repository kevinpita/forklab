//go:build e2e

package genesis_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/genesis"
)

// e2eChain describes one stock binary to fork the matching fixture with.
type e2eChain struct {
	env       string
	fixture   string
	keyType   string
	feeDenom  string
	gasPrices string
	patches   []string
	appPorts  []int
}

var e2eChains = map[string]e2eChain{
	"simd": {
		env: "FORKLAB_SIMD", fixture: "simd_export.json", feeDenom: "stake",
		appPorts: []int{1317, 9090},
	},
	"exrpd": {
		env: "FORKLAB_EXRPD", fixture: "exrpd_export.json", keyType: "eth_secp256k1",
		feeDenom: "axrp", gasPrices: "800000000000axrp",
		patches: []string{
			`.app_state.ratelimit.hour_epoch.epoch_start_height = (.initial_height|tonumber)`,
			`.app_state.bank.denom_metadata |= map(if .denom_units[0].denom != .base then (.denom_units[0].denom = .base | .display = .base) else . end)`,
		},
		appPorts: []int{1317, 9090, 8545, 8546, 6065, 8100},
	},
}

// Ports sit far from the defaults so the test can run next to other local chains.
const e2ePortOffset = 5000

func rpcPort(i int) int { return 26657 + e2ePortOffset + i*100 }
func p2pPort(i int) int { return 26656 + e2ePortOffset + i*100 }

// TestE2EFork rewrites a real export for two lab nodes, runs the stock
// binary on it, and checks that blocks are produced and that the gov account
// can withdraw rewards from its injected delegation.
func TestE2EFork(t *testing.T) {
	for name, chain := range e2eChains {
		t.Run(name, func(t *testing.T) {
			bin := os.Getenv(chain.env)
			if bin == "" {
				t.Skipf("%s not set", chain.env)
			}
			runFork(t, bin, chain)
		})
	}
}

func runFork(t *testing.T, bin string, chain e2eChain) {
	lab := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	before := decodeBytes(t, fixtureBytes(t, chain.fixture))
	chainID := before.str("chain_id")
	bondDenom := before.str("app_state.staking.params.bond_denom")

	homes := []string{filepath.Join(lab, "node0"), filepath.Join(lab, "node1")}
	var keys []genesis.NodeKey
	for i, home := range homes {
		run("init", fmt.Sprintf("node%d", i), "--chain-id", chainID, "--home", home)
		raw, err := os.ReadFile(filepath.Join(home, "config", "priv_validator_key.json"))
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, genesis.NodeKey{PubKey: decodeBytes(t, raw).str("pub_key.value")})
	}
	keyArgs := []string{"--keyring-backend", "test", "--home", homes[0]}
	addArgs := append([]string{"keys", "add", "gov"}, keyArgs...)
	if chain.keyType != "" {
		addArgs = append(addArgs, "--key-type", chain.keyType)
	}
	run(addArgs...)
	gov := run(append([]string{"keys", "show", "gov", "-a"}, keyArgs...)...)

	balances := map[string]*big.Int{bondDenom: big.NewInt(1_000_000_000)}
	fee, _ := new(big.Int).SetString("1000000000000000000000000", 10)
	if chain.feeDenom != bondDenom {
		balances[chain.feeDenom] = fee
	}
	var coins []genesis.Coin
	for denom, amount := range balances {
		coins = append(coins, genesis.Coin{Denom: denom, Amount: amount})
	}
	// A non-default block max_gas proves the node consumed the mirrored
	// consensus params instead of CometBFT defaults.
	g := patched(t, chain.fixture, `.consensus.params.block.max_gas = "31337000"`)
	report, err := genesis.Takeover(g, genesis.TakeoverInput{
		Validators:   keys,
		Accounts:     []genesis.Account{{Address: gov, Balances: coins}},
		GovDelegator: &genesis.GovDelegator{Address: gov, Amount: big.NewInt(200_000_000)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := genesis.GovPatch(g, genesis.GovParams{
		VotingPeriod:          30 * time.Second,
		ExpeditedVotingPeriod: 20 * time.Second,
		MinDeposit:            genesis.Coin{Denom: chain.feeDenom, Amount: big.NewInt(1)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := genesis.ApplyPatches(g, chain.patches); err != nil {
		t.Fatal(err)
	}
	data, err := g.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("takeover: reduction=%s bond=%s total_power=%s node0=%s(%s) node1=%s(%s)", report.PowerReduction, report.BondDenom, report.TotalPower,
		report.Validators[0].Operator, report.Validators[0].Power, report.Validators[1].Operator, report.Validators[1].Power)

	var ids []string
	for _, home := range homes {
		if err := os.WriteFile(filepath.Join(home, "config", "genesis.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run("comet", "show-node-id", "--home", home))
	}
	for i, home := range homes {
		peer := fmt.Sprintf("%s@127.0.0.1:%d", ids[1-i], p2pPort(1-i))
		configureNode(t, home, i, peer, chain)
	}
	for i, home := range homes {
		logFile, err := os.Create(filepath.Join(lab, fmt.Sprintf("node%d.log", i)))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "start", "--home", home)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			if t.Failed() {
				content, _ := os.ReadFile(logFile.Name())
				var lines []string
				for _, line := range strings.Split(string(content), "\n") {
					if strings.Contains(line, "panic") || strings.Contains(line, "ERR") {
						lines = append(lines, line)
					}
				}
				if len(lines) > 30 {
					lines = lines[:30]
				}
				t.Logf("%s:\n%s", logFile.Name(), strings.Join(lines, "\n"))
			}
		})
	}

	initial, _ := strconv.ParseInt(fmt.Sprint(before.at("initial_height")), 10, 64)
	height := waitForHeight(t, rpcPort(0), initial+3, 120*time.Second)
	t.Logf("height advanced from initial %d to %d", initial, height)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/consensus_params", rpcPort(0)))
	if err != nil {
		t.Fatal(err)
	}
	var params doc
	if err := json.NewDecoder(resp.Body).Decode(&params); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	maxGas := params.str("result.consensus_params.block.max_gas")
	if maxGas != "31337000" {
		t.Fatalf("node runs with block max_gas %q, genesis said 31337000: consensus params were not consumed", maxGas)
	}
	t.Logf("node consensus params: block.max_gas=%s (from genesis)", maxGas)

	node := fmt.Sprintf("tcp://127.0.0.1:%d", rpcPort(0))
	txArgs := append([]string{"tx", "distribution", "withdraw-all-rewards", "--from", "gov", "--chain-id", chainID, "--node", node, "--gas", "400000", "-y", "--output", "json"}, keyArgs...)
	if chain.gasPrices != "" {
		txArgs = append(txArgs, "--gas-prices", chain.gasPrices)
	}
	res := waitForTx(t, bin, node, submitted(t, run(txArgs...)))
	t.Logf("withdraw-all-rewards from %s included at height %s with code 0", gov, res.str("height"))
	t.Logf("height after withdraw: %d", waitForHeight(t, rpcPort(0), height+1, 60*time.Second))

	// The gov account alone must carry a proposal: its delegations hold the
	// boost, so its yes vote clears even the mainnet quorum.
	proposal := filepath.Join(lab, "proposal.json")
	if err := os.WriteFile(proposal, []byte(fmt.Sprintf(`{"messages":[],"metadata":"forklab","deposit":"1%s","title":"forklab e2e","summary":"gov account decides alone"}`, chain.feeDenom)), 0o644); err != nil {
		t.Fatal(err)
	}
	submit := append([]string{"tx", "gov", "submit-proposal", proposal, "--from", "gov", "--chain-id", chainID, "--node", node, "--gas", "400000", "-y", "--output", "json"}, keyArgs...)
	if chain.gasPrices != "" {
		submit = append(submit, "--gas-prices", chain.gasPrices)
	}
	waitForTx(t, bin, node, submitted(t, run(submit...)))
	proposals := decodeBytes(t, []byte(run("query", "gov", "proposals", "--node", node, "--output", "json"))).list("proposals")
	id := fmt.Sprint(proposals[len(proposals)-1]["id"])
	vote := append([]string{"tx", "gov", "vote", id, "yes", "--from", "gov", "--chain-id", chainID, "--node", node, "--gas", "400000", "-y", "--output", "json"}, keyArgs...)
	if chain.gasPrices != "" {
		vote = append(vote, "--gas-prices", chain.gasPrices)
	}
	waitForTx(t, bin, node, submitted(t, run(vote...)))
	tally := decodeBytes(t, []byte(run("query", "gov", "tally", id, "--node", node, "--output", "json")))
	pool := decodeBytes(t, []byte(run("query", "staking", "pool", "--node", node, "--output", "json")))
	yes, bonded := tally.str("tally.yes_count"), pool.str("pool.bonded_tokens")
	share := new(big.Rat).SetFrac(bigStr(t, yes), bigStr(t, bonded))
	if share.Cmp(big.NewRat(334, 1000)) < 0 {
		t.Fatalf("gov yes votes %s of %s bonded (%s) do not reach the default 33.4%% quorum", yes, bonded, share.FloatString(3))
	}
	t.Logf("proposal %s: gov yes votes %s of %s bonded = %s", id, yes, bonded, share.FloatString(3))
	deadline := time.Now().Add(120 * time.Second)
	for {
		status := decodeBytes(t, []byte(run("query", "gov", "proposal", id, "--node", node, "--output", "json"))).str("proposal.status")
		if status == "PROPOSAL_STATUS_PASSED" {
			t.Logf("proposal %s PASSED on the gov account's vote alone", id)
			break
		}
		if status != "PROPOSAL_STATUS_VOTING_PERIOD" || time.Now().After(deadline) {
			t.Fatalf("proposal %s ended as %s", id, status)
		}
		time.Sleep(2 * time.Second)
	}
}

// submitted checks the CheckTx result of a broadcast and returns the hash.
func submitted(t *testing.T, out string) string {
	t.Helper()
	res := decodeBytes(t, []byte(out))
	if code := fmt.Sprint(res.at("code")); code != "0" {
		t.Fatalf("tx rejected: %v", res)
	}
	return res.str("txhash")
}

// waitForTx polls until the tx is in a block and asserts it succeeded.
func waitForTx(t *testing.T, bin, node, hash string) doc {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		out, err := exec.Command(bin, "query", "tx", hash, "--node", node, "--output", "json").Output()
		if err == nil {
			res := decodeBytes(t, out)
			if code := fmt.Sprint(res.at("code")); code != "0" {
				t.Fatalf("tx %s failed with code %s: %s", hash, code, res.str("raw_log"))
			}
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("tx %s not found after 60s", hash)
		}
		time.Sleep(time.Second)
	}
}

// configureNode ports the prototype's config edits: per-node ports, fast
// blocks, local peering, zero minimum gas price.
func configureNode(t *testing.T, home string, i int, peer string, chain e2eChain) {
	t.Helper()
	offset := e2ePortOffset + i*100
	rewrite(t, filepath.Join(home, "config", "config.toml"), map[string]string{
		`^laddr = "tcp://127\.0\.0\.1:26657"`: fmt.Sprintf(`laddr = "tcp://127.0.0.1:%d"`, rpcPort(i)),
		`^laddr = "tcp://0\.0\.0\.0:26656"`:   fmt.Sprintf(`laddr = "tcp://0.0.0.0:%d"`, p2pPort(i)),
		`^pprof_laddr = .*`:                   fmt.Sprintf(`pprof_laddr = "localhost:%d"`, 6060+offset),
		`^timeout_commit = .*`:                `timeout_commit = "1s"`,
		`^allow_duplicate_ip = .*`:            `allow_duplicate_ip = true`,
		`^addr_book_strict = .*`:              `addr_book_strict = false`,
		`^persistent_peers = .*`:              fmt.Sprintf(`persistent_peers = "%s"`, peer),
		`^prometheus = true`:                  `prometheus = false`,
	})
	app := map[string]string{`^minimum-gas-prices = .*`: fmt.Sprintf(`minimum-gas-prices = "0%s"`, chain.feeDenom)}
	for _, port := range chain.appPorts {
		app[fmt.Sprintf(`:%d"`, port)] = fmt.Sprintf(`:%d"`, port+offset)
	}
	rewrite(t, filepath.Join(home, "config", "app.toml"), app)
}

func rewrite(t *testing.T, path string, rules map[string]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for pattern, replacement := range rules {
		data = regexp.MustCompile(`(?m)`+pattern).ReplaceAll(data, []byte(replacement))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForHeight(t *testing.T, port int, want int64, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", port))
		if err == nil {
			var status struct {
				Result struct {
					SyncInfo struct {
						LatestBlockHeight string `json:"latest_block_height"`
					} `json:"sync_info"`
				} `json:"result"`
			}
			err = json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
			if height, _ := strconv.ParseInt(status.Result.SyncInfo.LatestBlockHeight, 10, 64); err == nil && height >= want {
				return height
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("height %d not reached within %s", want, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
