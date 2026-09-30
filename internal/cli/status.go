package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

type nodeState struct {
	Name       string     `json:"name"`
	RPCPort    int        `json:"rpc_port"`
	Up         bool       `json:"up"`
	Error      string     `json:"error,omitempty"`
	Height     int64      `json:"height,omitempty"`
	BlockTime  *time.Time `json:"block_time,omitempty"`
	CatchingUp bool       `json:"catching_up"`
	Peers      int        `json:"peers"`
}

type validatorRow struct {
	Address      string  `json:"address,omitempty"`
	Node         string  `json:"node,omitempty"`
	Moniker      string  `json:"moniker,omitempty"`
	Operator     string  `json:"operator,omitempty"`
	VotingPower  int64   `json:"voting_power"`
	PowerPercent float64 `json:"power_percent"`
	Status       string  `json:"status,omitempty"`
	Jailed       bool    `json:"jailed"`
	Tokens       string  `json:"tokens,omitempty"`
	pubKey       string
}

type statusView struct {
	Lab     string `json:"lab"`
	ChainID string `json:"chain_id"`
	// Height is the highest height any node reports.
	Height int64 `json:"height"`
	// AvgBlockTime is over the last 10 blocks, empty when too few exist.
	AvgBlockTime string         `json:"avg_block_time,omitempty"`
	Nodes        []nodeState    `json:"nodes"`
	Validators   []validatorRow `json:"validators"`
	UpgradePlan  *chain.Plan    `json:"upgrade_plan"`
	Warnings     []string       `json:"warnings,omitempty"`
}

func (v statusView) WriteHuman(w io.Writer) error {
	_, _ = fmt.Fprintf(w, "lab %s (%s) height %d", v.Lab, v.ChainID, v.Height)
	if v.AvgBlockTime != "" {
		_, _ = fmt.Fprintf(w, ", avg block time %s", v.AvgBlockTime)
	}
	_, _ = fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NODE\tRPC\tSTATE\tHEIGHT\tBLOCK TIME\tCATCHING UP\tPEERS")
	for _, n := range v.Nodes {
		if !n.Up {
			_, _ = fmt.Fprintf(tw, "%s\t%d\tdown\t-\t-\t-\t-\t(%s)\n", n.Name, n.RPCPort, n.Error)
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\tup\t%d\t%s\t%v\t%d\n", n.Name, n.RPCPort, n.Height, n.BlockTime.Local().Format(time.TimeOnly), n.CatchingUp, n.Peers)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(tw, "VALIDATOR\tNODE\tPOWER\tSTATUS\tJAILED\tMONIKER")
	for _, r := range v.Validators {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%.2f%%\t%s\t%v\t%s\n", dash(r.Address), dash(r.Node), r.PowerPercent, dash(r.Status), r.Jailed, r.Moniker)
	}
	_ = tw.Flush()
	if v.UpgradePlan == nil {
		_, _ = fmt.Fprintln(w, "\nupgrade plan: none")
	} else {
		_, _ = fmt.Fprintf(w, "\nupgrade plan: %s at height %d\n", v.UpgradePlan.Name, v.UpgradePlan.Height)
	}
	for _, warn := range v.Warnings {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warn)
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newStatusCmd(a *app) *cobra.Command {
	var ref string
	var w watchFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show node heights, the validator set, and any pending upgrade",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := openLab(ref)
			if err != nil {
				return err
			}
			if !w.watch {
				v, err := e.status(cmd.Context())
				if err != nil {
					return err
				}
				return a.print(cmd, v)
			}
			return watch(cmd, a, w.interval, func(ctx context.Context) (statusView, string, error) {
				v, err := e.status(ctx)
				heights := make([]string, len(v.Nodes))
				for i, n := range v.Nodes {
					heights[i] = strconv.FormatInt(n.Height, 10)
				}
				return v, strings.Join(heights, ","), err
			})
		},
	}
	labRefFlag(cmd, &ref)
	w.register(cmd, time.Second)
	return cmd
}

// status asks every node for its view, then the first live node for the
// validator set and the chain CLI for staking and upgrade state. A down node
// is part of the view, and failed CLI queries become warnings; only a lab
// with no node up is an error.
func (e labEnv) status(ctx context.Context) (statusView, error) {
	v := statusView{Lab: e.cfg.Name, ChainID: e.cfg.ChainID, Nodes: []nodeState{}, Validators: []validatorRow{}}
	live := -1
	var errs []error
	for i, n := range e.cfg.Nodes {
		ns := nodeState{Name: n.Name, RPCPort: n.RPCPort()}
		st, err := e.clients[i].Status(ctx)
		if err != nil {
			errs = append(errs, err)
			ns.Error = err.Error()
			v.Nodes = append(v.Nodes, ns)
			continue
		}
		ns.Up, ns.Height, ns.CatchingUp = true, st.LatestHeight, st.CatchingUp
		ns.BlockTime = &st.LatestBlockTime
		if ni, err := e.clients[i].NetInfo(ctx); err == nil {
			ns.Peers = len(ni.Peers)
		}
		if live < 0 {
			live = i
		}
		v.Height = max(v.Height, st.LatestHeight)
		v.Nodes = append(v.Nodes, ns)
	}
	if live < 0 {
		return v, e.notRunning(errs)
	}
	rpc := e.clients[live]
	if d, err := rpc.AvgBlockTime(ctx, 10); err == nil {
		v.AvgBlockTime = d.Round(time.Millisecond).String()
	}
	set, err := rpc.Validators(ctx, 0)
	if err != nil {
		return v, err
	}
	c := e.cli(live)
	staking, err := c.StakingValidators(ctx)
	if err != nil {
		v.Warnings = append(v.Warnings, "staking validators: "+err.Error())
	}
	v.Validators = joinValidators(set, staking, e.nodeByKey)
	if v.UpgradePlan, err = c.UpgradePlan(ctx); err != nil {
		v.Warnings = append(v.Warnings, "upgrade plan: "+err.Error())
	}
	return v, nil
}

// joinValidators lists the CometBFT set with its staking record, plus any
// staking validator that is jailed or run by a lab node but out of the set.
// Consensus public keys join the set, staking, and lab nodes.
func joinValidators(set []chain.Validator, staking []chain.StakingValidator, nodeByKey map[string]string) []validatorRow {
	var total int64
	for _, s := range set {
		total += s.VotingPower
	}
	rows := []validatorRow{}
	inSet := map[string]bool{}
	for _, s := range set {
		r := validatorRow{Address: s.Address, VotingPower: s.VotingPower, pubKey: s.PubKey.Value, Node: nodeByKey[s.PubKey.Value]}
		if total > 0 {
			r.PowerPercent = float64(s.VotingPower) * 100 / float64(total)
		}
		inSet[s.PubKey.Value] = true
		rows = append(rows, r)
	}
	for _, sv := range staking {
		if !inSet[sv.ConsensusPubKey] && (sv.Jailed || nodeByKey[sv.ConsensusPubKey] != "") {
			rows = append(rows, validatorRow{pubKey: sv.ConsensusPubKey, Node: nodeByKey[sv.ConsensusPubKey]})
		}
	}
	byKey := map[string]chain.StakingValidator{}
	for _, sv := range staking {
		byKey[sv.ConsensusPubKey] = sv
	}
	for i := range rows {
		if sv, ok := byKey[rows[i].pubKey]; ok {
			rows[i].Moniker, rows[i].Operator, rows[i].Status, rows[i].Jailed, rows[i].Tokens = sv.Moniker, sv.Operator, sv.Status, sv.Jailed, sv.Tokens
		}
	}
	slices.SortStableFunc(rows, func(a, b validatorRow) int { return cmp.Compare(b.VotingPower, a.VotingPower) })
	return rows
}

type watchFlags struct {
	watch    bool
	interval time.Duration
}

func (w *watchFlags) register(cmd *cobra.Command, interval time.Duration) {
	cmd.Flags().BoolVarP(&w.watch, "watch", "w", false, "keep printing on every change until interrupted; --json emits one object per line")
	cmd.Flags().DurationVar(&w.interval, "interval", interval, "poll interval for --watch")
}

// watch polls read every interval and prints its view whenever key changes,
// as NDJSON under --json. It returns when the command's context ends. A read
// error is printed once, as a view-less line, and polling goes on, since
// nodes may be restarting.
func watch[V interface{ WriteHuman(io.Writer) error }](cmd *cobra.Command, a *app, interval time.Duration, read func(context.Context) (V, string, error)) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	enc := json.NewEncoder(w)
	last := "\x00"
	for {
		v, key, err := read(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			key = "error: " + err.Error()
		}
		if key != last {
			last = key
			switch {
			case err != nil && a.json:
				_ = output.WriteStreamError(w, err)
			case err != nil:
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
			case a.json:
				_ = enc.Encode(v)
			default:
				_, _ = fmt.Fprintf(w, "--- %s\n", time.Now().Format(time.TimeOnly))
				_ = v.WriteHuman(w)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
