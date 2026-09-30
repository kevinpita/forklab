//go:build e2e

package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/pierrec/lz4/v4"
)

// TestE2ELabFork packs a local "mainnet" node home into a .tar.lz4 snapshot,
// serves it over HTTP, forks it into a two-validator lab, and checks that
// the lab continues past the exported height with both validators signing
// and that the gov account alone passes a proposal. A second create of the
// same snapshot must reuse the cached export. The mainnet homes come from
// the prototypes: FORKLAB_SIMD_MAINNET and FORKLAB_EXRPD_MAINNET name a
// stopped node home with data/ and config/genesis.json.
func TestE2ELabFork(t *testing.T) {
	for _, c := range []struct {
		env, mainnetEnv, profile, version string
		// keepChainID passes the mainnet chain ID with --chain-id; chains
		// whose export needs one (exrpd) would be relabeled otherwise.
		keepChainID bool
		// delegator is a pre-fork delegator key in the mainnet home's test keyring.
		delegator string
	}{
		{"FORKLAB_SIMD", "FORKLAB_SIMD_MAINNET", "simd", "0.53.8", false, "d0"},
		{"FORKLAB_EXRPD", "FORKLAB_EXRPD_MAINNET", "xrplevm", "11.1.1", true, "op0"},
	} {
		t.Run(c.profile, func(t *testing.T) {
			bin, mainnet := os.Getenv(c.env), os.Getenv(c.mainnetEnv)
			if bin == "" || mainnet == "" {
				t.Skipf("%s or %s not set", c.env, c.mainnetEnv)
			}
			config := t.TempDir()
			t.Setenv("FORKLAB_CONFIG_DIR", config)
			t.Setenv("FORKLAB_HOME", t.TempDir())
			t.Setenv("FORKLAB_TEST_CHILD", "1")
			e, err := profile.Store{Dir: t.TempDir()}.Get(c.profile)
			if err != nil {
				t.Fatal(err)
			}
			e.Doc.Binaries = map[string]profile.BinaryDocument{c.version: {Path: bin}}
			if _, err := (profile.Store{Dir: filepath.Join(config, "profiles")}).Save(e.Doc); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "mainnet.tar.lz4")
			packData(t, mainnet, archive)
			srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(archive))))
			defer srv.Close()
			url := srv.URL + "/mainnet.tar.lz4"

			// Without --chain-id the lab takes the profile's chain ID, which
			// relabels this fake mainnet; with it the mainnet ID is kept.
			chainID := e.Doc.ChainID
			flags := []string{"--profile", c.profile, "--version", c.version, "--validators", "2", "--fork", url}
			if c.keepChainID {
				chainID = decodeFile(t, filepath.Join(mainnet, "config", "genesis.json"))["chain_id"].(string)
				flags = append(flags, "--chain-id", chainID)
			}
			// Two creates of the same snapshot at once: the work dir lock
			// serializes them, one exports, the other waits and reuses it.
			stderrs := make([]string, 2)
			var wg sync.WaitGroup
			for i, name := range []string{"e2e", "e2e-again"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					stderrs[i] = forklabStderr(t, append([]string{"lab", "create", name}, flags...)...)
				}()
			}
			wg.Wait()
			all := strings.Join(stderrs, "")
			if strings.Count(all, "export with") != 1 || strings.Count(all, "reusing export") != 1 || !strings.Contains(all, "waiting for another forklab process") {
				t.Fatalf("concurrent creates did not share one export:\n%s", all)
			}
			t.Logf("concurrent creates: one export, one reuse after waiting")
			runLabFork(t, bin, mainnet, c.delegator, chainID)
		})
	}
}

// forklabStderr runs forklab and returns its stderr, where progress and
// export step lines go.
func forklabStderr(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("forklab %v: exit %d\n%s%s", args, code, stdout.String(), stderr.String())
	}
	return stderr.String()
}

func runLabFork(t *testing.T, bin, mainnet, delegator, chainID string) {
	l, err := labs()
	if err != nil {
		t.Fatal(err)
	}
	c, dir, err := l.Get("e2e")
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != lab.ModeFork || c.Fork == nil || c.Fork.Height < 1 || c.ChainID != chainID {
		t.Fatalf("lab.yaml mode %s fork %+v chain %s, want a fork running as %s", c.Mode, c.Fork, c.ChainID, chainID)
	}
	for _, n := range c.Nodes {
		if n.Validator != "" || n.Operator == "" {
			t.Fatalf("%s: validator %q operator %q, want a taken-over operator and no key", n.Name, n.Validator, n.Operator)
		}
	}
	t.Logf("lab forks %s at height %d from %s; node0 took over %s", c.ChainID, c.Fork.Height, c.Fork.Archive, c.Nodes[0].Operator)
	work, _ := os.ReadDir(filepath.Join(filepath.Dir(c.Fork.Archive), "work"))
	for _, w := range work {
		if _, err := os.Stat(filepath.Join(filepath.Dir(c.Fork.Archive), "work", w.Name(), "home", "data")); !os.IsNotExist(err) {
			t.Fatalf("extracted snapshot data kept after the export in %s: %v", w.Name(), err)
		}
	}
	t.Cleanup(func() {
		var out bytes.Buffer
		_ = Run([]string{"lab", "down", "e2e"}, &out, &out)
		if t.Failed() {
			for _, n := range c.Nodes {
				log, _ := os.ReadFile(filepath.Join(lab.NodeHome(dir, n), "node.log"))
				if len(log) > 3000 {
					log = log[len(log)-3000:]
				}
				t.Logf("%s log tail:\n%s", n.Name, log)
			}
		}
	})
	checkCreateLog(t, dir)
	forklab(t, "lab", "up", "e2e")

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	var clients []*chain.Client
	for _, n := range c.Nodes {
		client, err := chain.New(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort()), 0)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	for i, client := range clients {
		h, err := client.WaitHeight(ctx, c.Fork.Height+5)
		if err != nil {
			t.Fatalf("node%d: %v", i, err)
		}
		t.Logf("node%d reached height %d (fork at %d)", i, h, c.Fork.Height)
	}
	cs, err := clients[0].ConsensusState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The mainnet validators stay in the set but are offline; the two lab
	// nodes sign every block and hold more than two thirds of the power.
	if strings.Count(cs.LastCommit.Bits, "x") != 2 || cs.LastCommit.Power*3 <= cs.LastCommit.TotalPower*2 {
		t.Fatalf("last commit bits %q with %d/%d power, want the 2 lab validators signing with more than 2/3", cs.LastCommit.Bits, cs.LastCommit.Power, cs.LastCommit.TotalPower)
	}
	t.Logf("last commit before height %d: %s of %d validators (%d/%d power)", cs.Height, cs.LastCommit.Bits, len(cs.Validators), cs.LastCommit.Power, cs.LastCommit.TotalPower)

	// A pre-fork delegator keeps its key, balance, and delegation across the
	// takeover: it can still send and withdraw rewards.
	node := fmt.Sprintf("tcp://127.0.0.1:%d", c.Nodes[0].RPCPort())
	tx := func(args ...string) {
		t.Helper()
		args = append(args, "--keyring-backend", "test", "--keyring-dir", mainnet, "--home", lab.NodeHome(dir, c.Nodes[0]),
			"--chain-id", c.ChainID, "--node", node, "--gas", "400000", "--gas-prices", c.Profile.GasPrices, "-y", "--output", "json")
		res := chainJSON(t, bin, args...)
		if fmt.Sprint(res["code"]) != "0" {
			t.Fatalf("%s rejected: %v", strings.Join(args[:3], " "), res)
		}
		hash := res["txhash"].(string)
		deadline := time.Now().Add(60 * time.Second)
		for {
			out, err := exec.Command(bin, "query", "tx", hash, "--node", node, "--output", "json").Output()
			if err == nil {
				var res map[string]any
				if err := json.Unmarshal(out, &res); err != nil {
					t.Fatal(err)
				}
				if fmt.Sprint(res["code"]) != "0" {
					t.Fatalf("tx %s failed: %v", hash, res["raw_log"])
				}
				t.Logf("%s from pre-fork delegator %s included at height %v", strings.Join(args[1:3], " "), delegator, res["height"])
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("tx %s not found after 60s", hash)
			}
			time.Sleep(time.Second)
		}
	}
	tx("tx", "bank", "send", delegator, c.Accounts[len(c.Accounts)-1].Address, "1"+c.Profile.BondDenom)
	tx("tx", "distribution", "withdraw-all-rewards", "--from", delegator)

	// The gov account alone carries a text proposal: it owns the boost
	// delegations, so --auto-vote's single yes vote clears quorum and
	// threshold.
	submitted := forklabJSON(t, "gov", "submit", "--template", "text", "--auto-vote", "--lab", "e2e")
	id := fmt.Sprint(submitted["proposal_id"])
	votes, _ := submitted["votes"].([]any)
	if len(votes) != 1 || votes[0].(map[string]any)["voter"] != "gov" {
		t.Fatalf("auto-vote cast %v, want exactly the gov key's vote", votes)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		status := forklabJSON(t, "gov", "show", id, "--lab", "e2e")["status"]
		if status == "PASSED" {
			t.Logf("proposal %s PASSED on the gov account's vote alone", id)
			return
		}
		if status != "VOTING_PERIOD" || time.Now().After(deadline) {
			t.Fatalf("proposal %s ended as %v", id, status)
		}
		time.Sleep(2 * time.Second)
	}
}

// chainJSON runs the chain binary and decodes its JSON output.
func chainJSON(t *testing.T, bin string, args ...string) map[string]any {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("%s: %v: %s", strings.Join(args, " "), err, out)
	}
	return doc
}

// forklabJSON runs forklab --json and returns the envelope's data object.
func forklabJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args = append(args, "--json")
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("forklab %v: exit %d\n%s%s", args, code, stdout.String(), stderr.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("forklab %v: %v: %s", args, err, stdout.String())
	}
	return env.Data
}

// packData writes <home>/data as data/... into a .tar.lz4 archive.
func packData(t *testing.T, home, archive string) {
	t.Helper()
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := lz4.NewWriter(f)
	tw := tar.NewWriter(zw)
	root := filepath.Join(home, "data")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, path)
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tw, src)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []io.Closer{tw, zw, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := os.Stat(archive)
	t.Logf("packed %s/data into %s (%d bytes)", home, archive, info.Size())
}

func decodeFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return doc
}
