package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/spf13/cobra"
)

// voteView is one validator's slot: missing, nil, or block with the block
// hash prefix.
type voteView struct {
	Kind  string `json:"kind"`
	Block string `json:"block,omitempty"`
}

func (v voteView) String() string {
	switch v.Kind {
	case "block":
		return v.Block
	case "missing":
		return "-"
	}
	return v.Kind
}

type tallyView struct {
	Bits     string  `json:"bits"`
	Power    int64   `json:"power"`
	Total    int64   `json:"total"`
	Fraction float64 `json:"fraction"`
}

type consensusValidator struct {
	Index       int      `json:"index"`
	Address     string   `json:"address"`
	Node        string   `json:"node,omitempty"`
	VotingPower int64    `json:"voting_power"`
	Prevote     voteView `json:"prevote"`
	Precommit   voteView `json:"precommit"`
	LastCommit  voteView `json:"last_commit"`
}

type nodePeers struct {
	Name  string   `json:"name"`
	Up    bool     `json:"up"`
	Error string   `json:"error,omitempty"`
	Peers []string `json:"peers"`
}

type consensusView struct {
	// Source is the node whose consensus state this is.
	Source          string               `json:"source"`
	Height          int64                `json:"height"`
	Round           int32                `json:"round"`
	Step            string               `json:"step"`
	ProposerAddress string               `json:"proposer_address"`
	ProposerNode    string               `json:"proposer_node,omitempty"`
	Prevotes        tallyView            `json:"prevotes"`
	Precommits      tallyView            `json:"precommits"`
	LastCommit      tallyView            `json:"last_commit"`
	Validators      []consensusValidator `json:"validators"`
	Nodes           []nodePeers          `json:"nodes"`
}

func (v consensusView) WriteHuman(w io.Writer) error {
	proposer := v.ProposerAddress
	if v.ProposerNode != "" {
		proposer = v.ProposerNode
	}
	_, _ = fmt.Fprintf(w, "height %d  round %d  step %s  proposer %s  (from %s)\n", v.Height, v.Round, v.Step, proposer, v.Source)
	_, _ = fmt.Fprintf(w, "prevotes %s %.2f  precommits %s %.2f  last commit %s %.2f\n\n",
		dash(v.Prevotes.Bits), v.Prevotes.Fraction, dash(v.Precommits.Bits), v.Precommits.Fraction, dash(v.LastCommit.Bits), v.LastCommit.Fraction)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tVALIDATOR\tNODE\tPOWER\tPREVOTE\tPRECOMMIT\tLAST COMMIT")
	for _, r := range v.Validators {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\n", r.Index, r.Address, dash(r.Node), r.VotingPower, r.Prevote, r.Precommit, r.LastCommit)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(tw, "NODE\tPEERS")
	for _, n := range v.Nodes {
		peers := strings.Join(n.Peers, " ")
		if !n.Up {
			peers = "down (" + n.Error + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", n.Name, peers)
	}
	return tw.Flush()
}

func newConsensusCmd(a *app) *cobra.Command {
	var ref string
	var w watchFlags
	cmd := &cobra.Command{
		Use:   "consensus",
		Short: "Show the consensus round: step, proposer, and each validator's votes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := openLab(ref)
			if err != nil {
				return err
			}
			if !w.watch {
				v, err := e.consensus(cmd.Context())
				if err != nil {
					return err
				}
				return a.print(cmd, v)
			}
			return watch(cmd, a, w.interval, func(ctx context.Context) (consensusView, string, error) {
				v, err := e.consensus(ctx)
				return v, fmt.Sprintf("%d/%d/%s %s %s %s", v.Height, v.Round, v.Step, v.Prevotes.Bits, v.Precommits.Bits, v.LastCommit.Bits), err
			})
		},
	}
	labRefFlag(cmd, &ref)
	w.register(cmd, 250*time.Millisecond)
	return cmd
}

// consensus reads the round from the first live node and peers from every
// node, and names validators after the lab nodes that run them.
func (e labEnv) consensus(ctx context.Context) (consensusView, error) {
	nodeByID := map[string]string{}
	for _, n := range e.cfg.Nodes {
		nodeByID[n.NodeID] = n.Name
	}
	v := consensusView{Nodes: []nodePeers{}, Validators: []consensusValidator{}}
	source := -1
	var errs []error
	for i, n := range e.cfg.Nodes {
		np := nodePeers{Name: n.Name, Peers: []string{}}
		_, err := e.clients[i].Status(ctx)
		if err == nil {
			var ni chain.NetInfo
			if ni, err = e.clients[i].NetInfo(ctx); err == nil {
				for _, p := range ni.Peers {
					np.Peers = append(np.Peers, cmp.Or(nodeByID[p.ID], p.ID))
				}
			}
		}
		if err != nil {
			np.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", n.Name, err))
		} else {
			np.Up = true
			if source < 0 {
				source = i
			}
		}
		v.Nodes = append(v.Nodes, np)
	}
	if source < 0 {
		return v, e.notRunning(errs)
	}
	cs, err := e.clients[source].ConsensusState(ctx)
	if err != nil {
		return v, err
	}
	v.Source = e.cfg.Nodes[source].Name
	v.Height, v.Round, v.Step = cs.Height, cs.Round, cs.Step.String()
	v.ProposerAddress = cs.ProposerAddress
	v.ProposerNode = e.nodeByAddr[strings.ToUpper(cs.ProposerAddress)]
	v.Prevotes, v.Precommits, v.LastCommit = tally(cs.Prevotes), tally(cs.Precommits), tally(cs.LastCommit)
	for i, val := range cs.Validators {
		v.Validators = append(v.Validators, consensusValidator{
			Index: i, Address: val.Address, Node: e.nodeByAddr[strings.ToUpper(val.Address)], VotingPower: val.VotingPower,
			Prevote: slot(cs.Prevotes, i), Precommit: slot(cs.Precommits, i), LastCommit: slot(cs.LastCommit, i),
		})
	}
	return v, nil
}

func tally(vs chain.VoteSet) tallyView {
	return tallyView{Bits: vs.Bits, Power: vs.Power, Total: vs.TotalPower, Fraction: vs.Fraction()}
}

// slot is validator i's vote in vs. A set with no votes yet, such as the
// current round during Propose, reads as missing.
func slot(vs chain.VoteSet, i int) voteView {
	if i >= len(vs.Votes) {
		return voteView{Kind: chain.VoteMissing.String()}
	}
	vote := vs.Votes[i]
	return voteView{Kind: vote.Kind.String(), Block: vote.BlockHashPrefix}
}
