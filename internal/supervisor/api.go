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
	// OpConfigure changes the arguments of a stopped node, persisted for adoption.
	OpConfigure Op = "configure"
	// OpDown stops every node (killing any that ignore SIGTERM), then the
	// supervisor exits.
	OpDown Op = "down"
	// OpExit makes the supervisor exit and leaves the nodes running; the next
	// supervisor adopts them.
	OpExit Op = "exit"
	// OpUpgrade sets the pending upgrade the supervisor swaps halted nodes
	// to, or clears it when Request.Upgrade is nil.
	OpUpgrade Op = "upgrade"
	// OpComplete ends the pending upgrade Request.Upgrade names once every
	// node is swapped, keeping it as the completed one.
	OpComplete  Op = "complete"
	OpRecover   Op = "recover"
	OpRecovered Op = "recovered"
)

const All = "all"

// Request is one API call. Nodes is a node index or All for the node ops.
// Timeout bounds how long stop, restart, and down wait for SIGTERM to work;
// zero means DefaultStopTimeout. Binary is restart's optional new path and
// Version the profile version it is, recorded once the node runs on it.
type Request struct {
	Op       Op            `json:"op"`
	Nodes    string        `json:"nodes,omitempty"`
	Binary   string        `json:"binary,omitempty"`
	Version  string        `json:"version,omitempty"`
	Timeout  time.Duration `json:"timeout_ns,omitempty"`
	Upgrade  *Upgrade      `json:"upgrade,omitempty"`
	Expected *Upgrade      `json:"expected,omitempty"`
	Args     []string      `json:"args,omitempty"`
}

// Response carries every node's status, the pending upgrade, and the last
// completed one after the operation. Error is set when the operation failed
// for at least one node.
type Response struct {
	RecoveryProtocol int          `json:"recovery_protocol,omitempty"`
	Error            string       `json:"error,omitempty"`
	Nodes            []NodeStatus `json:"nodes"`
	Upgrade          *Upgrade     `json:"upgrade,omitempty"`
	Completed        *Upgrade     `json:"completed,omitempty"`
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
