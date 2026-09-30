package lab

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func portOf(t *testing.T, ports []Port, file, key string) int {
	t.Helper()
	for _, p := range ports {
		if p.File == file && p.Key == key {
			return p.Port
		}
	}
	t.Fatalf("no port %s %s in %v", file, key, ports)
	return 0
}

func TestAllocatePortsOffsetsEachNode(t *testing.T) {
	extra := map[string]map[string]int{"app.toml": {"json-rpc.address": 8545}}
	nodes, err := allocatePorts(3, extra)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		node      int
		file, key string
		port      int
	}{
		{0, "config.toml", "p2p.laddr", 26656},
		{0, "config.toml", "rpc.laddr", 26657},
		{0, "config.toml", "rpc.pprof_laddr", 6060},
		{0, "app.toml", "api.address", 1317},
		{0, "app.toml", "grpc.address", 9090},
		{0, "app.toml", "json-rpc.address", 8545},
		{1, "config.toml", "rpc.laddr", 26757},
		{2, "config.toml", "p2p.laddr", 26856},
		{2, "app.toml", "json-rpc.address", 8745},
	}
	for _, w := range want {
		if got := portOf(t, nodes[w.node], w.file, w.key); got != w.port {
			t.Errorf("node%d %s %s = %d, want %d", w.node, w.file, w.key, got, w.port)
		}
	}
}

func TestAllocatePortsRejectsCollisions(t *testing.T) {
	// node1's p2p port is 26756.
	extra := map[string]map[string]int{"app.toml": {"evm.metrics": 26756}}
	_, err := allocatePorts(2, extra)
	if err == nil || !strings.Contains(err.Error(), "26756") || !strings.Contains(err.Error(), "p2p.laddr") {
		t.Fatalf("err = %v, want a collision naming 26756 and p2p.laddr", err)
	}
}

func TestAllocatePortsRejectsPortsPast65535(t *testing.T) {
	if _, err := allocatePorts(400, nil); err == nil || !strings.Contains(err.Error(), "65535") {
		t.Fatalf("err = %v, want a port range error", err)
	}
}

func TestCheckFreeNamesTheBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port
	err = checkFree([][]Port{{{File: "config.toml", Key: "rpc.laddr", Port: busy}}})
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(busy)) || !strings.Contains(err.Error(), "rpc.laddr") {
		t.Fatalf("err = %v, want one naming port %d and rpc.laddr", err, busy)
	}
}
