package supervisor

import (
	"fmt"
	"strconv"
	"time"
)

// Op is an API operation. Each request is one JSON Request on a fresh
// connection to supervisor.sock, answered by one JSON Response.
type Op string

const (
	OpStatus  Op = "status"
	OpStart   Op = "start"
	OpStop    Op = "stop"
	OpKill    Op = "kill"
	OpRestart Op = "restart"
	// OpDown stops every node (killing any that ignore SIGTERM), then the
	// supervisor exits.
	OpDown Op = "down"
	// OpExit makes the supervisor exit and leaves the nodes running; the next
	// supervisor adopts them.
	OpExit Op = "exit"
)

const All = "all"

// Request is one API call. Nodes is a node index or All for the node ops.
// Timeout bounds how long stop, restart, and down wait for SIGTERM to work;
// zero means DefaultStopTimeout. Binary is restart's optional new path.
type Request struct {
	Op      Op            `json:"op"`
	Nodes   string        `json:"nodes,omitempty"`
	Binary  string        `json:"binary,omitempty"`
	Timeout time.Duration `json:"timeout_ns,omitempty"`
}

// Response carries every node's status after the operation. Error is set
// when the operation failed for at least one node.
type Response struct {
	Error string       `json:"error,omitempty"`
	Nodes []NodeStatus `json:"nodes"`
}

const DefaultStopTimeout = 30 * time.Second

func selectNodes(sel string, count int) ([]int, error) {
	if sel == All {
		all := make([]int, count)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	i, err := strconv.Atoi(sel)
	if err != nil || i < 0 || i >= count {
		return nil, fmt.Errorf("node %q: want an index 0..%d or %q", sel, count-1, All)
	}
	return []int{i}, nil
}
