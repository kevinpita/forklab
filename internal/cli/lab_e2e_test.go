//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
)

// TestE2ELabFresh creates a two-validator fresh lab for each stock binary,
// starts it through the supervisor, and checks that both validators sign
// blocks. Each binary is registered as a user profile that copies the
// built-in one with a path source.
func TestE2ELabFresh(t *testing.T) {
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
			runLabFresh(t, c.profile, c.version)
		})
	}
}

func forklab(t *testing.T, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("forklab %v: exit %d\n%s%s", args, code, stdout.String(), stderr.String())
	}
}

func runLabFresh(t *testing.T, profileName, version string) {
	forklab(t, "lab", "create", "e2e", "--profile", profileName, "--version", version, "--validators", "2")
	l, err := labs()
	if err != nil {
		t.Fatal(err)
	}
	c, dir, err := l.Get("e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var out bytes.Buffer
		_ = Run([]string{"supervisor", "down", "--lab", dir}, &out, &out)
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
	forklab(t, "node", "start", "all", "--lab", dir)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var clients []*chain.Client
	for _, n := range c.Nodes {
		client, err := chain.New(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort()), 0)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	start, err := clients[0].WaitHeight(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, client := range clients {
		h, err := client.WaitHeight(ctx, start+5)
		if err != nil {
			t.Fatalf("node%d: %v", i, err)
		}
		t.Logf("node%d reached height %d (from %d)", i, h, start)
	}
	// Equal stakes mean every block needs both precommits; check it anyway.
	cs, err := clients[0].ConsensusState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Validators) != 2 || cs.LastCommit.Bits != "xx" {
		t.Fatalf("validators=%d last commit bits %q, want 2 validators both signing", len(cs.Validators), cs.LastCommit.Bits)
	}
	t.Logf("last commit before height %d: %s (%d/%d power)", cs.Height, cs.LastCommit.Bits, cs.LastCommit.Power, cs.LastCommit.TotalPower)
}

// checkCreateLog asserts that create.log is private and holds no mnemonic.
func checkCreateLog(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "create.log")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("create.log mode %o, want 600", fi.Mode().Perm())
	}
	log, _ := os.ReadFile(path)
	data, _ := os.ReadFile(lab.MnemonicsPath(dir))
	var keys []lab.Mnemonic
	if err := json.Unmarshal(data, &keys); err != nil || len(keys) == 0 {
		t.Fatalf("mnemonics.json: %v", err)
	}
	for _, k := range keys {
		words := strings.Fields(k.Mnemonic)
		if strings.Contains(string(log), strings.Join(words[:4], " ")) {
			t.Fatalf("create.log holds the %s mnemonic", k.Name)
		}
	}
	t.Logf("create.log is 0600 and holds none of %d mnemonics", len(keys))
}
