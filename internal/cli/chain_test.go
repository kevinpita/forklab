package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
	"go.yaml.in/yaml/v3"
)

func TestJoinValidators(t *testing.T) {
	set := []chain.Validator{
		{Address: "AA", PubKey: chain.PubKey{Value: "ka"}, VotingPower: 10},
		{Address: "BB", PubKey: chain.PubKey{Value: "kb"}, VotingPower: 30},
	}
	staking := []chain.StakingValidator{
		{Operator: "opb", Moniker: "b", ConsensusPubKey: "kb", Status: "BONDED", Tokens: "30"},
		{Operator: "opa", Moniker: "a", ConsensusPubKey: "ka", Status: "BONDED", Tokens: "10"},
		{Operator: "opj", Moniker: "jailed-other", ConsensusPubKey: "kj", Status: "UNBONDING", Jailed: true, Tokens: "5"},
		{Operator: "opn", Moniker: "our-unbonded", ConsensusPubKey: "kn", Status: "UNBONDED", Tokens: "1"},
		{Operator: "opu", Moniker: "someone-unbonded", ConsensusPubKey: "ku", Status: "UNBONDED", Tokens: "1"},
	}
	got := joinValidators(set, staking, map[string]string{"ka": "node0", "kn": "node1"})
	want := []validatorRow{
		{Address: "BB", Moniker: "b", Operator: "opb", VotingPower: 30, PowerPercent: 75, Status: "BONDED", Tokens: "30", pubKey: "kb"},
		{Address: "AA", Node: "node0", Moniker: "a", Operator: "opa", VotingPower: 10, PowerPercent: 25, Status: "BONDED", Tokens: "10", pubKey: "ka"},
		{Moniker: "jailed-other", Operator: "opj", Status: "UNBONDING", Jailed: true, Tokens: "5", pubKey: "kj"},
		{Node: "node1", Moniker: "our-unbonded", Operator: "opn", Status: "UNBONDED", Tokens: "1", pubKey: "kn"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows =\n%+v\nwant\n%+v", got, want)
	}
	// Without staking data the set alone still names lab nodes.
	if rows := joinValidators(set, nil, map[string]string{"kb": "node1"}); len(rows) != 2 || rows[0].Node != "node1" {
		t.Errorf("set-only rows = %+v", rows)
	}
}

func TestAutoVoters(t *testing.T) {
	c := lab.Config{Mode: lab.ModeFresh, Nodes: []lab.Node{{Validator: "val0"}, {Validator: "val1"}}}
	if got := autoVoters(c); !reflect.DeepEqual(got, []string{"val0", "val1", "gov"}) {
		t.Errorf("fresh voters = %v", got)
	}
	c.Mode = "fork"
	if got := autoVoters(c); !reflect.DeepEqual(got, []string{"gov"}) {
		t.Errorf("fork voters = %v", got)
	}
}

func TestSlotOfAnEmptyRoundIsMissing(t *testing.T) {
	vs := chain.VoteSet{Votes: []chain.Vote{{Kind: chain.VoteBlock, BlockHashPrefix: "AB12"}}}
	if got := slot(vs, 0); got != (voteView{Kind: "block", Block: "AB12"}) {
		t.Errorf("slot 0 = %+v", got)
	}
	if got := slot(chain.VoteSet{}, 1); got != (voteView{Kind: "missing"}) {
		t.Errorf("empty set slot = %+v", got)
	}
}

// fakeExecLab writes a one-node lab whose chain binary is a script that
// prints its args, one per line, and exits with code. When live, the node's
// RPC port answers /status like a node with blocks; otherwise nothing listens.
func fakeExecLab(t *testing.T, code int, live bool) (dir string, rpcPort int) {
	t.Helper()
	dir = t.TempDir()
	home := filepath.Join(dir, "node0")
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := `{"address":"AA","pub_key":{"type":"tendermint/PubKeyEd25519","value":"ka"}}`
	if err := os.WriteFile(filepath.Join(home, "config", "priv_validator_key.json"), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcPort = ln.Addr().(*net.TCPAddr).Port
	if live {
		status, err := os.ReadFile("../chain/testdata/simd/status.json")
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(status) }))
		srv.Listener = ln
		srv.Start()
		t.Cleanup(srv.Close)
	} else {
		_ = ln.Close()
	}

	bin := filepath.Join(dir, "chaind")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\"\necho oops >&2\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Store{Dir: t.TempDir()}.Get("simd")
	if err != nil {
		t.Fatal(err)
	}
	cfg := lab.Config{
		Name: "fake", Mode: lab.ModeFresh, ChainID: "fake-1", Version: "1", Validators: 1, Profile: p.Doc,
		Nodes: []lab.Node{{Name: "node0", Validator: "val0", Ports: []lab.Port{{File: "config.toml", Key: "rpc.laddr", Port: rpcPort}}}},
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := supervisor.NodeSpec{Name: "node0", Binary: bin, Args: []string{"start", "--home", home}, Home: home, LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid")}
	if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	return dir, rpcPort
}

func TestExecPassesThroughOutputAndExitCode(t *testing.T) {
	dir, port := fakeExecLab(t, 7, true)
	code, stdout, stderr := run("exec", "--lab", dir, "--", "q", "bank", "balances", "addr1")
	want := strings.Join([]string{"q", "bank", "balances", "addr1", "--home", filepath.Join(dir, "node0"), "--node", "tcp://127.0.0.1:" + strconv.Itoa(port)}, "\n") + "\n"
	if code != 7 || stdout != want || stderr != "oops\n" {
		t.Errorf("code %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
	}
}

func TestExecBareQueryGetsNoNode(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, false)
	code, stdout, _ := run("exec", "--lab", dir, "--", "q")
	if want := "q\n--home\n" + filepath.Join(dir, "node0") + "\n"; code != 0 || stdout != want {
		t.Errorf("code %d, stdout %q, want %q", code, stdout, want)
	}
}

func TestExecJSONFailureIsNotOK(t *testing.T) {
	dir, _ := fakeExecLab(t, 7, true)
	code, stdout, _ := run("exec", "--lab", dir, "--json", "--", "q", "gov", "proposal", "9")
	var env struct {
		OK   bool     `json:"ok"`
		Data execView `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	if code != 7 || env.OK || env.Data.ExitCode != 7 || env.Data.Stderr != "oops\n" {
		t.Errorf("code %d, envelope %+v", code, env)
	}
}

func TestExecJSONWrapsOutput(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, false)
	code, stdout, _ := run("exec", "--lab", dir, "--json", "--", "keys", "list")
	var env struct {
		OK   bool     `json:"ok"`
		Data execView `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	keyring := filepath.Join(dir, "keys")
	wantArgs := []string{"keys", "list", "--home", filepath.Join(dir, "node0"), "--keyring-backend", "test", "--keyring-dir", keyring}
	if code != 0 || !env.OK || env.Data.ExitCode != 0 || !reflect.DeepEqual(env.Data.Args, wantArgs) ||
		env.Data.Stdout != strings.Join(wantArgs, "\n")+"\n" || env.Data.Stderr != "oops\n" {
		t.Errorf("code %d, envelope %+v", code, env)
	}
}

func TestChainCommandsOnAStoppedLabExitThree(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, false)
	for _, args := range [][]string{
		{"exec", "--lab", dir, "--", "q", "bank", "balances", "addr1"},
		{"exec", "--lab", dir, "--", "query", "gov", "params"},
		{"exec", "--lab", dir, "--", "tx", "bank", "send", "a", "b", "1stake"},
		{"exec", "--lab", dir, "--", "status"},
		{"status", "--lab", dir},
		{"consensus", "--lab", dir},
		{"account", "list", "--lab", dir},
		{"gov", "list", "--lab", dir},
	} {
		code, stdout, _ := run(append([]string{"--json"}, args...)...)
		if code != 3 || !strings.Contains(stdout, `"lab_not_running"`) {
			t.Errorf("%v: code %d, stdout %s", args, code, stdout)
		}
	}
}

func TestChainCommandUsageErrors(t *testing.T) {
	dir, _ := fakeExecLab(t, 0, false)
	for _, args := range [][]string{
		{"gov", "vote", "x", "yes", "--lab", dir},
		{"gov", "vote", "1", "maybe", "--lab", dir},
		{"gov", "submit", "--template", "upgrade", "--name", "v2", "--lab", dir},
		{"gov", "vote", "1", "yes", "--from", "nobody", "--lab", dir},
		{"gov", "submit", "--lab", dir},
		{"account", "send", "nobody", "val0", "1", "--lab", dir},
	} {
		if code, _, stderr := run(args...); code != 2 {
			t.Errorf("%v: code %d, stderr %s", args, code, stderr)
		}
	}
}

func TestDedupeKeepsFirstOrder(t *testing.T) {
	if got := dedupe([]string{"gov", "val0", "gov", "val1", "val0"}); !reflect.DeepEqual(got, []string{"gov", "val0", "val1"}) {
		t.Errorf("dedupe = %v", got)
	}
}
