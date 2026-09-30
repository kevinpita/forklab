package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// homeFrom copies a chain's init-generated config files into a new home.
func homeFrom(t *testing.T, chain string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"config.toml", "app.toml"} {
		data, err := os.ReadFile(filepath.Join("testdata", chain, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "config", f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func readTOML(t *testing.T, home, file string) *tomlDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "config", file))
	if err != nil {
		t.Fatal(err)
	}
	return parseTOML(data)
}

func TestConfigureWritesPortsPeersAndChainSettings(t *testing.T) {
	for _, chain := range []string{"simd", "exrpd"} {
		t.Run(chain, func(t *testing.T) {
			home := homeFrom(t, chain)
			extra := map[string]map[string]int{}
			if chain == "exrpd" {
				extra["app.toml"] = map[string]int{"json-rpc.address": 8545, "evm.geth-metrics-address": 8100}
			}
			nodes, err := allocatePorts(2, extra)
			if err != nil {
				t.Fatal(err)
			}
			set, err := configure(home, nodeSettings{
				ports:     nodes[1],
				peers:     []string{"aa@127.0.0.1:26656", "bb@127.0.0.1:26856"},
				blockTime: 500 * time.Millisecond,
				feeDenom:  "axrp",
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg, app := readTOML(t, home, "config.toml"), readTOML(t, home, "app.toml")
			type setting struct {
				doc       *tomlDoc
				key, want string
			}
			want := []setting{
				{cfg, "p2p.laddr", `"tcp://0.0.0.0:26756"`},
				{cfg, "rpc.laddr", `"tcp://127.0.0.1:26757"`},
				{cfg, "rpc.pprof_laddr", `"localhost:6160"`},
				{cfg, "p2p.persistent_peers", `"aa@127.0.0.1:26656,bb@127.0.0.1:26856"`},
				{cfg, "p2p.allow_duplicate_ip", "true"},
				{cfg, "p2p.addr_book_strict", "false"},
				{cfg, "consensus.timeout_commit", `"500ms"`},
				{app, "api.enable", "true"},
				{app, "api.address", `"tcp://localhost:1417"`},
				{app, "grpc.address", `"localhost:9190"`},
				{app, "minimum-gas-prices", `"0axrp"`},
			}
			if chain == "exrpd" {
				want = append(want,
					setting{app, "json-rpc.address", `"127.0.0.1:8645"`},
					setting{app, "evm.geth-metrics-address", `"127.0.0.1:8200"`},
				)
			}
			for _, w := range want {
				if _, got, _ := w.doc.find(w.key); got != w.want {
					t.Errorf("%s = %s, want %s", w.key, got, w.want)
				}
			}
			for _, p := range set {
				if p.Key == "grpc-web.address" {
					t.Errorf("recorded grpc-web.address, which this chain's app.toml lacks")
				}
			}
			if got := portOf(t, set, "config.toml", "rpc.laddr"); got != 26757 {
				t.Errorf("recorded rpc port %d, want 26757", got)
			}
			raw, _ := os.ReadFile(filepath.Join(home, "config", "config.toml"))
			if !strings.Contains(string(raw), "# TCP or UNIX socket address for the RPC server to listen on") {
				t.Error("config.toml lost its comments")
			}
		})
	}
}

func TestConfigureFailsOnAMissingRequiredKey(t *testing.T) {
	home := homeFrom(t, "simd")
	nodes, err := allocatePorts(1, map[string]map[string]int{"app.toml": {"json-rpc.address": 8545}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = configure(home, nodeSettings{ports: nodes[0], blockTime: time.Second, feeDenom: "stake"})
	if err == nil || !strings.Contains(err.Error(), "json-rpc.address") || !strings.Contains(err.Error(), "app.toml") {
		t.Fatalf("err = %v, want one naming app.toml json-rpc.address", err)
	}
}

func TestTOMLSetLeavesDisabledListenersAlone(t *testing.T) {
	doc := parseTOML([]byte("[rpc]\npprof_laddr = \"\"\n"))
	applied, err := doc.setPort("rpc.pprof_laddr", 6160)
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v, want a disabled listener left alone", applied, err)
	}
	if _, got, _ := doc.find("rpc.pprof_laddr"); got != `""` {
		t.Fatalf("pprof_laddr = %s, want empty", got)
	}
}

func TestNodeIDMatchesTheChainCLI(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/simd/node_key.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "node_key.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := nodeID(home)
	if err != nil {
		t.Fatal(err)
	}
	// From `simd comet show-node-id` on the same home.
	if want := "7fd4befd0aa6669059db3b22696fe49ff3c8e4f5"; id != want {
		t.Fatalf("node id %s, want %s", id, want)
	}
}
