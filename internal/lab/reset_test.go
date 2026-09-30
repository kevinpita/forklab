package lab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/supervisor"
)

// fakeChain answers `comet unsafe-reset-all --help` like a Cosmos SDK binary
// and records every other invocation in calls.log.
const fakeChain = `#!/bin/sh
case "$*" in
*--help*) echo "  --keep-addr-book   keep the address book intact" ;;
*) echo "$*" >> "$(dirname "$0")/calls.log" ;;
esac
`

// TestResetRunsTheChainsCometReset resets a lab whose nodes were upgraded
// to chaind-v2: the reset runs on, and restores, the creation binary.
func TestResetRunsTheChainsCometReset(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chaind")
	if err := os.WriteFile(bin, []byte(fakeChain), 0o755); err != nil {
		t.Fatal(err)
	}
	upgraded := filepath.Join(t.TempDir(), "chaind-v2")
	if err := os.WriteFile(upgraded, []byte(fakeChain), 0o755); err != nil {
		t.Fatal(err)
	}
	var specs []supervisor.NodeSpec
	c := Config{Name: "lab", Mode: ModeFresh, Version: "1.0.0", Validators: 2}
	for i, name := range []string{"node0", "node1"} {
		home := filepath.Join(dir, name)
		specs = append(specs, supervisor.NodeSpec{
			Index: i, Name: name, Binary: upgraded, Args: []string{"start", "--home", home}, Home: home,
			LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid"),
		})
		c.Nodes = append(c.Nodes, Node{Index: i, Name: name, Version: "2.0.0"})
	}
	if err := supervisor.SaveNodes(dir, specs); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	pending := supervisor.Paths{Dir: dir}.Upgrade()
	if err := os.WriteFile(pending, []byte(`{"plan":{"name":"v2","height":50}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	methods, err := Reset(context.Background(), dir, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(methods, []ResetMethod{ResetComet, ResetComet}) {
		t.Errorf("methods = %v", methods)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("upgrade.json survived the reset: %v", err)
	}
	if after, err := supervisor.LoadNodes(dir); err != nil || after[0].Binary != bin || after[1].Binary != bin {
		t.Errorf("nodes.json = %+v, %v; want both on %s", after, err, bin)
	}
	if after, err := Load(dir); err != nil || after.Nodes[0].Version != "1.0.0" || after.Nodes[1].Version != "1.0.0" || after.Version != "1.0.0" {
		t.Errorf("lab.yaml = %+v, %v; want every node back on 1.0.0", after, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(upgraded), "calls.log")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the reset ran the upgraded binary: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	want := "comet unsafe-reset-all --keep-addr-book --home " + specs[0].Home + "\n" +
		"comet unsafe-reset-all --keep-addr-book --home " + specs[1].Home + "\n"
	if string(calls) != want {
		t.Errorf("calls:\n%s\nwant:\n%s", calls, want)
	}
}

func TestResetNamesTheFailingNode(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chaind")
	script := strings.Replace(fakeChain, `*) echo "$*" >> "$(dirname "$0")/calls.log" ;;`, `*) echo "db locked" >&2; exit 1 ;;`, 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "node0")
	spec := supervisor.NodeSpec{Name: "node0", Binary: bin, Args: []string{"--home", home}, Home: home, LogPath: "l", PidPath: "p"}
	if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, Config{Name: "lab", Validators: 1, Nodes: []Node{{Name: "node0"}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Reset(context.Background(), dir, bin)
	if err == nil || !strings.Contains(err.Error(), "node0") || !strings.Contains(err.Error(), "db locked") {
		t.Errorf("err = %v, want it to name node0 and the chain's error", err)
	}
}
