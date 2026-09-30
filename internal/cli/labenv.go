package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

// labEnv is what the chain commands need to talk to a lab's nodes.
type labEnv struct {
	dir     string
	cfg     lab.Config
	profile profile.Profile
	// bin is node0's current binary, from nodes.json, so it follows upgrades.
	bin     string
	clients []*chain.Client
	// nodeByAddr and nodeByKey name the lab node behind a consensus address
	// or base64 public key, read from disk so stopped nodes still count.
	nodeByAddr map[string]string
	nodeByKey  map[string]string
}

func labRefFlag(cmd *cobra.Command, ref *string) {
	cmd.PersistentFlags().StringVar(ref, "lab", "", "lab name or directory (default: the running lab, else the only lab)")
}

// openLab loads the lab --lab resolves to, with a client for every node.
func openLab(ref string) (labEnv, error) {
	var e labEnv
	var err error
	if e.dir, err = resolveLab(ref); err != nil {
		return labEnv{}, err
	}
	if e.cfg, err = lab.Load(e.dir); err != nil {
		return labEnv{}, err
	}
	if len(e.cfg.Nodes) == 0 {
		return labEnv{}, fmt.Errorf("lab %s has no nodes", e.cfg.Name)
	}
	if e.profile, err = e.cfg.Profile.Profile(); err != nil {
		return labEnv{}, fmt.Errorf("lab %s: profile: %w", e.cfg.Name, err)
	}
	specs, err := supervisor.LoadNodes(e.dir)
	if err != nil {
		return labEnv{}, err
	}
	e.bin = specs[0].Binary
	e.nodeByAddr, e.nodeByKey = map[string]string{}, map[string]string{}
	for _, n := range e.cfg.Nodes {
		key, err := lab.ReadConsensusKey(lab.NodeHome(e.dir, n))
		if err != nil {
			return labEnv{}, err
		}
		e.nodeByAddr[key.Address], e.nodeByKey[key.PubKey] = n.Name, n.Name
		c, err := chain.New("http://127.0.0.1:"+strconv.Itoa(n.RPCPort()), 2*time.Second)
		if err != nil {
			return labEnv{}, err
		}
		e.clients = append(e.clients, c)
	}
	return e, nil
}

// live returns the index of the first node that answers with a block, so
// commands keep working while some nodes are stopped. No answer at all is
// exit code 3.
func (e labEnv) live(ctx context.Context) (int, error) {
	var errs []error
	for i, c := range e.clients {
		_, err := c.Status(ctx)
		if err == nil {
			return i, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", e.cfg.Nodes[i].Name, err))
	}
	return 0, e.notRunning(errs)
}

func (e labEnv) notRunning(errs []error) error {
	return fmt.Errorf("lab %s: %w: no node answers: %w", e.cfg.Name, output.ErrLabNotRunning, errors.Join(errs...))
}

// cli points the chain binary at node i, with node0's home for client
// config and the lab keyring for signing.
func (e labEnv) cli(i int) chain.CLI {
	return chain.CLI{
		Bin:        e.bin,
		Home:       lab.NodeHome(e.dir, e.cfg.Nodes[0]),
		Node:       "tcp://127.0.0.1:" + strconv.Itoa(e.cfg.Nodes[i].RPCPort()),
		ChainID:    e.cfg.ChainID,
		KeyringDir: lab.KeysDir(e.dir),
		GasPrices:  e.profile.GasPrices.String(),
	}
}

// liveCLI is cli for the first live node.
func (e labEnv) liveCLI(ctx context.Context) (chain.CLI, *chain.Client, error) {
	i, err := e.live(ctx)
	if err != nil {
		return chain.CLI{}, nil, err
	}
	return e.cli(i), e.clients[i], nil
}

// account finds a lab key by name.
func (e labEnv) account(name string) (lab.Account, bool) {
	for _, a := range e.cfg.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return lab.Account{}, false
}

func (e labEnv) accountNames() string {
	names := make([]string, len(e.cfg.Accounts))
	for i, a := range e.cfg.Accounts {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

// txTimeout bounds waiting for a broadcast transaction to land in a block.
const txTimeout = 60 * time.Second

// broadcast sends a tx from the lab key from and waits for it in a block.
func broadcast(ctx context.Context, c chain.CLI, rpc *chain.Client, from string, args ...string) (chain.TxResult, error) {
	hash, err := c.Broadcast(ctx, from, args...)
	if err != nil {
		return chain.TxResult{}, err
	}
	return waitTx(ctx, rpc, hash)
}

func waitTx(ctx context.Context, rpc *chain.Client, hash string) (chain.TxResult, error) {
	ctx, cancel := context.WithTimeout(ctx, txTimeout)
	defer cancel()
	return rpc.WaitTx(ctx, hash)
}
