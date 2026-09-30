package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
)

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestStoppedLabNamesNodesAndCauseOnce(t *testing.T) {
	e := labEnv{cfg: lab.Config{Name: "demo"}}
	var ports []int
	for i := range 2 {
		port := closedPort(t)
		ports = append(ports, port)
		e.cfg.Nodes = append(e.cfg.Nodes, lab.Node{Name: "node" + strconv.Itoa(i), Ports: []lab.Port{{Key: "rpc.laddr", Port: port}}})
		c, err := chain.New("http://127.0.0.1:"+strconv.Itoa(port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		e.clients = append(e.clients, c)
	}
	_, err := e.live(context.Background())
	want := fmt.Sprintf("lab demo: lab not running: no node answers (node0 :%d, node1 :%d: connection refused)", ports[0], ports[1])
	if err == nil || err.Error() != want || !errors.Is(err, output.ErrLabNotRunning) {
		t.Fatalf("live = %v\nwant %s", err, want)
	}

	err = e.notRunning([]error{errors.New("rpc status: 502 Bad Gateway"), fmt.Errorf("rpc status: %w", context.DeadlineExceeded)})
	want = fmt.Sprintf("lab demo: lab not running: no node answers (node0 :%d: rpc status: 502 Bad Gateway, node1 :%d: timeout)", ports[0], ports[1])
	if err.Error() != want {
		t.Errorf("mixed causes = %s\nwant %s", err, want)
	}
}
