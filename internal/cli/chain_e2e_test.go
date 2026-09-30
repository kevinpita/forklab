//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
)

// TestE2EChainCommands drives status, consensus, account, and gov against a
// running two-validator fresh lab of each stock binary.
func TestE2EChainCommands(t *testing.T) {
	for _, c := range []struct{ env, profile, version string }{
		{"FORKLAB_SIMD", "simd", "0.53.8"},
		{"FORKLAB_EXRPD", "xrplevm", "11.1.1"},
	} {
		t.Run(c.profile, func(t *testing.T) {
			bin := os.Getenv(c.env)
			if bin == "" {
				t.Skipf("%s not set", c.env)
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
			runChainCommands(t, c.profile, c.version)
		})
	}
}

func forklabData[T any](t *testing.T, args ...string) T {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(append(args, "--json"), &stdout, &stderr); code != 0 {
		t.Fatalf("forklab %v: exit %d\n%s%s", args, code, stdout.String(), stderr.String())
	}
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("forklab %v: %v: %s", args, err, stdout.String())
	}
	return env.Data
}

func runChainCommands(t *testing.T, profileName, version string) {
	forklab(t, "lab", "create", "e2e", "--profile", profileName, "--version", version, "--validators", "2")
	l, err := labs()
	if err != nil {
		t.Fatal(err)
	}
	_, dir, err := l.Get("e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var out bytes.Buffer
		_ = Run([]string{"supervisor", "down", "--lab", dir}, &out, &out)
	})
	forklab(t, "node", "start", "all", "--lab", dir)

	var st statusView
	deadline := time.Now().Add(2 * time.Minute)
	for st.Height < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("lab never reached height 3: %+v", st)
		}
		time.Sleep(time.Second)
		var stdout, stderr bytes.Buffer
		if Run([]string{"status", "--lab", "e2e", "--json"}, &stdout, &stderr) == 0 {
			var env struct{ Data statusView }
			_ = json.Unmarshal(stdout.Bytes(), &env)
			st = env.Data
		}
	}
	if len(st.Nodes) != 2 || len(st.Validators) != 2 || st.UpgradePlan != nil || len(st.Warnings) != 0 {
		t.Fatalf("status = %+v", st)
	}
	for i, n := range st.Nodes {
		if !n.Up || n.Peers != 1 || n.CatchingUp {
			t.Errorf("node%d = %+v, want up with one peer", i, n)
		}
	}
	for _, v := range st.Validators {
		if v.Node == "" || v.PowerPercent != 50 || v.Status != "BONDED" || v.Jailed {
			t.Errorf("validator = %+v, want a bonded lab node with half the power", v)
		}
	}

	cs := forklabData[consensusView](t, "consensus", "--lab", "e2e")
	if cs.Height < st.Height || len(cs.Validators) != 2 || cs.ProposerNode == "" || cs.LastCommit.Bits != "xx" {
		t.Errorf("consensus = %+v", cs)
	}
	for _, v := range cs.Validators {
		if v.Node == "" || v.LastCommit.Kind != "block" {
			t.Errorf("consensus validator = %+v, want a lab node that signed the last block", v)
		}
	}

	e, err := openLab("e2e")
	if err != nil {
		t.Fatal(err)
	}
	denom := e.profile.FeeDenom
	balance := func(name string) *big.Int {
		for _, acc := range forklabData[accountList](t, "account", "list", "--lab", "e2e") {
			if acc.Name != name {
				continue
			}
			for _, c := range acc.Balances {
				if c.Denom == denom {
					n, _ := new(big.Int).SetString(c.Amount, 10)
					return n
				}
			}
		}
		t.Fatalf("no %s balance for %s", denom, name)
		return nil
	}
	before := balance("test1")
	sent := forklabData[txView](t, "account", "send", "test0", "test1", "4242", "--lab", "e2e")
	if sent.Code != 0 || sent.Height == 0 || sent.Hash == "" {
		t.Fatalf("send = %+v", sent)
	}
	if got := new(big.Int).Sub(balance("test1"), before); got.Int64() != 4242 {
		t.Errorf("test1 balance grew by %s, want 4242", got)
	}

	sub := forklabData[submitView](t, "gov", "submit", "--template", "text", "--title", "e2e", "--auto-vote", "--lab", "e2e")
	if sub.ProposalID == 0 || len(sub.Votes) != 3 {
		t.Fatalf("submit = %+v, want a proposal with votes from val0, val1, and gov", sub)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	for {
		p := forklabData[proposalView](t, "gov", "show", "1", "--lab", "e2e")
		if p.Status == "PASSED" {
			t.Logf("proposal passed with tally %+v", p.Tally)
			break
		}
		if p.Status != "VOTING_PERIOD" {
			t.Fatalf("proposal ended %s: %+v", p.Status, p)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("proposal still voting: %+v", p)
		case <-time.After(2 * time.Second):
		}
	}
}
