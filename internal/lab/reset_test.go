package lab

import (
	"context"
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

func TestResetRunsTheChainsCometReset(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chaind")
	if err := os.WriteFile(bin, []byte(fakeChain), 0o755); err != nil {
		t.Fatal(err)
	}
	var specs []supervisor.NodeSpec
	for i, name := range []string{"node0", "node1"} {
		home := filepath.Join(dir, name)
		specs = append(specs, supervisor.NodeSpec{
			Index: i, Name: name, Binary: bin, Args: []string{"start", "--home", home}, Home: home,
			LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid"),
		})
	}
	if err := supervisor.SaveNodes(dir, specs); err != nil {
		t.Fatal(err)
	}

	methods, err := Reset(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(methods, []ResetMethod{ResetComet, ResetComet}) {
		t.Errorf("methods = %v", methods)
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
	_, err := Reset(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "node0") || !strings.Contains(err.Error(), "db locked") {
		t.Errorf("err = %v, want it to name node0 and the chain's error", err)
	}
}
