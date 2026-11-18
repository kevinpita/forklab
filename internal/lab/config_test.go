package lab

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
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
	applied, err := doc.setPort("rpc.pprof_laddr", "", 6160)
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

// A cosmovisor node's client.toml names its own RPC: cosmovisor asks the
// node for its height through it, and the default port is node0's.
func TestConfigurePointsClientTOMLAtTheNodesOwnRPCWhenAsked(t *testing.T) {
	home := homeFrom(t, "simd")
	client := filepath.Join(home, "config", "client.toml")
	if err := os.WriteFile(client, []byte("chain-id = \"\"\nnode = \"tcp://localhost:26657\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nodes, err := allocatePorts(2, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		own  bool
		want string
	}{{false, `"tcp://localhost:26657"`}, {true, `"tcp://localhost:26757"`}} {
		if _, err := configure(home, nodeSettings{ports: nodes[1], blockTime: time.Second, feeDenom: "stake", ownClient: c.own}); err != nil {
			t.Fatal(err)
		}
		if _, got, _ := readTOML(t, home, "client.toml").find("node"); got != c.want {
			t.Errorf("ownClient %t: node = %s, want %s", c.own, got, c.want)
		}
	}
}

// A node in a container listens on every interface, so the ports the
// engine publishes reach it.
func TestConfigureReplacesTheListenHostWhenAsked(t *testing.T) {
	home := homeFrom(t, "simd")
	nodes, err := allocatePorts(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configure(home, nodeSettings{ports: nodes[0], blockTime: time.Second, feeDenom: "stake", host: "0.0.0.0"}); err != nil {
		t.Fatal(err)
	}
	cfg, app := readTOML(t, home, "config.toml"), readTOML(t, home, "app.toml")
	for _, w := range []struct {
		doc       *tomlDoc
		key, want string
	}{
		{cfg, "rpc.laddr", `"tcp://0.0.0.0:26657"`},
		{app, "api.address", `"tcp://0.0.0.0:1317"`},
		{app, "grpc.address", `"0.0.0.0:9090"`},
	} {
		if _, got, _ := w.doc.find(w.key); got != w.want {
			t.Errorf("%s = %s, want %s", w.key, got, w.want)
		}
	}
}

// A docker lab's nodes dial each other at their fixed addresses. A
// container name would be looked up once per start, and a node whose
// container was recreated would be lost to its peers.
func TestDockerLabPeersAreTheNodesFixedAddresses(t *testing.T) {
	partial := t.TempDir()
	nodes := []Node{{Index: 0, Name: "node0", NodeID: "aa"}, {Index: 1, Name: "node1", NodeID: "bb"}, {Index: 2, Name: "node2", NodeID: "cc"}}
	for _, n := range nodes {
		home := filepath.Join(partial, n.Name)
		if err := os.Rename(homeFrom(t, "simd"), home); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "config", "client.toml"), []byte("chain-id = \"\"\nkeyring-backend = \"os\"\nnode = \"tcp://localhost:26657\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A profile may name the same key in two config files.
		for _, file := range []string{"app.toml", "config.toml"} {
			f, err := os.OpenFile(filepath.Join(home, "config", file), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("\n[extra]\naddress = \"0.0.0.0:1\"\n"); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
		}
	}
	ports, err := allocatePorts(len(nodes), map[string]map[string]int{"app.toml": {"extra.address": 7000}, "config.toml": {"extra.address": 8000}})
	if err != nil {
		t.Fatal(err)
	}
	b := builder{
		in: CreateInput{Name: "dk", ChainID: "c-1"}, p: profile.Profile{BlockTime: time.Second, FeeDenom: "stake"},
		dir: partial, partial: partial, ports: ports, docker: true, subnet: "10.89.3.0/24",
		cli: chainCLI{ctx: t.Context(), bin: "/bin/true", log: io.Discard},
	}
	if err := b.configure(nodes); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{
		`"bb@10.89.3.11:26656,cc@10.89.3.12:26656"`,
		`"aa@10.89.3.10:26656,cc@10.89.3.12:26656"`,
		`"aa@10.89.3.10:26656,bb@10.89.3.11:26656"`,
	} {
		if _, got, _ := readTOML(t, b.home(i), "config.toml").find("p2p.persistent_peers"); got != want {
			t.Errorf("node%d: persistent_peers = %s, want %s", i, got, want)
		}
	}
	if want := []string{"10.89.3.10", "10.89.3.11", "10.89.3.12"}; !slices.Equal(b.ips, want) {
		t.Errorf("node addresses = %v, want %v", b.ips, want)
	}
	// The lab records the host port of each listen address, by file and key.
	for _, want := range []Port{{File: "app.toml", Key: "extra.address", Port: 7100}, {File: "config.toml", Key: "extra.address", Port: 8100}} {
		if !slices.ContainsFunc(nodes[1].Ports, func(p Port) bool { return p.File == want.File && p.Key == want.Key && p.Port == want.Port }) {
			t.Errorf("node1 ports = %+v, want %+v among them", nodes[1].Ports, want)
		}
	}
	// Each node's host port is published to the port the node listens on
	// in its container, which is node0's: with the host port on both sides
	// every node but node0 would be out of reach from the host.
	specs := b.nodeSpecs(nodes)
	for i, want := range [][]string{
		{"127.0.0.1:26657:26657", "127.0.0.1:26656:26656", "127.0.0.1:7000:7000", "127.0.0.1:8000:8000"},
		{"127.0.0.1:26757:26657", "127.0.0.1:26756:26656", "127.0.0.1:7100:7000", "127.0.0.1:8100:8000"},
		{"127.0.0.1:26857:26657", "127.0.0.1:26856:26656", "127.0.0.1:7200:7000", "127.0.0.1:8200:8000"},
	} {
		c := specs[i].Container
		if c == nil || c.IP != b.ips[i] || c.Subnet != "10.89.3.0/24" || len(c.Publish) != len(nodes[i].Ports) {
			t.Fatalf("node%d container = %+v, want its address and one mapping per port of %+v", i, c, nodes[i].Ports)
		}
		for _, w := range want {
			if !slices.Contains(c.Publish, w) {
				t.Errorf("node%d publishes %q, want %q among them", i, c.Publish, w)
			}
		}
	}
}
