package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/spf13/cobra"
)

// govKey is the lab key that submits proposals. In fork mode it also holds
// the delegation that carries the vote.
const govKey = "gov"

func newGovCmd(a *app) *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "gov",
		Short: "Submit, vote on, and inspect governance proposals",
	}
	labRefFlag(cmd, &ref)
	cmd.AddCommand(newGovSubmitCmd(a, &ref), newGovVoteCmd(a, &ref), newGovListCmd(a, &ref), newGovShowCmd(a, &ref), newGovFileCmd(a, &ref, false), newGovFileCmd(a, &ref, true), newGovWriteCmd(a))
	return cmd
}

// autoVoters are the lab keys whose votes pass a proposal on their own. In a
// fresh lab the validator operators are lab keys; in a fork they are not,
// and the gov key's injected delegation carries the vote instead.
func autoVoters(c lab.Config) []string {
	var keys []string
	if c.Mode == lab.ModeFresh {
		for _, n := range c.Nodes {
			keys = append(keys, n.Validator)
		}
	}
	return append(keys, govKey)
}

type voteResult struct {
	Voter  string `json:"voter"`
	Option string `json:"option"`
	chain.TxResult
}

type submitView struct {
	ProposalID uint64         `json:"proposal_id"`
	Tx         chain.TxResult `json:"tx"`
	Votes      []voteResult   `json:"votes,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
}

func (v submitView) WriteHuman(w io.Writer) error {
	_, _ = fmt.Fprintf(w, "proposal %d submitted in tx %s (block %d)\n", v.ProposalID, v.Tx.Hash, v.Tx.Height)
	for _, warn := range v.Warnings {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warn)
	}
	return votesView(v.Votes).WriteHuman(w)
}

type proposalFlags struct {
	template  string
	title     string
	summary   string
	metadata  string
	deposit   string
	expedited bool
	// upgrade
	name   string
	height int64
	info   string
	// params
	module  string
	msgType string
	set     []string
}

func newGovSubmitCmd(a *app, ref *string) *cobra.Command {
	var f proposalFlags
	var autoVote bool
	var savePath string
	var force bool
	cmd := &cobra.Command{
		Use:   "submit [file.json]",
		Short: "Submit a proposal from a file or a template, from the lab's gov key",
		Long: "Submit a proposal from the lab's gov key, either a submit-proposal JSON file\n" +
			"or --template text|upgrade|params. The deposit defaults to the chain's\n" +
			"minimum. --auto-vote then votes yes from every lab key that can pass it:\n" +
			"the validator operators and gov in a fresh lab, gov alone in a fork.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 1) == (f.template != "") {
				return output.Usagef("give a proposal file or --template, not both")
			}
			if err := f.validate(); err != nil {
				return err
			}
			if cmd.Flags().Changed("output") {
				if savePath == "" {
					return output.Usagef("--output needs a nonempty file path")
				}
				if len(args) != 0 {
					return output.Usagef("--output requires a template")
				}
			}
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, rpc, err := e.liveCLI(ctx)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("output") {
				p, err := buildProposal(ctx, c, f)
				if err != nil {
					return err
				}
				if err := writeProposalFile(savePath, p, force); err != nil {
					return err
				}
				return a.print(cmd, proposalSaved{savePath})
			}
			var hash string
			var warnings []string
			if f.template == "upgrade" {
				warn, err := upgradeWarning(ctx, c, rpc, e.profile.BlockTime, f)
				if err != nil {
					return err
				}
				if warn != "" {
					warnings = append(warnings, warn)
				}
			}
			if len(args) == 1 {
				hash, err = c.Broadcast(ctx, govKey, "gov", "submit-proposal", args[0])
			} else {
				var p chain.ProposalFile
				if p, err = buildProposal(ctx, c, f); err != nil {
					return err
				}
				hash, err = c.SubmitProposal(ctx, govKey, p)
			}
			if err != nil {
				return err
			}
			tx, err := waitTx(ctx, rpc, hash)
			if err != nil {
				return err
			}
			v := submitView{Tx: tx, Warnings: warnings}
			if v.ProposalID, err = chain.ProposalID(tx); err != nil {
				return err
			}
			if autoVote {
				if v.Votes, err = voteAll(ctx, c, rpc, autoVoters(e.cfg), v.ProposalID, "yes"); err != nil {
					return fmt.Errorf("proposal %d submitted, but voting failed: %w", v.ProposalID, err)
				}
			}
			return a.print(cmd, v)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&savePath, "output", "", "save the template to a JSON file without submitting or voting")
	fl.BoolVar(&force, "force", false, "replace the output file atomically")
	fl.StringVar(&f.template, "template", "", "build the proposal: text, upgrade, or params")
	fl.StringVar(&f.title, "title", "", "proposal title (default per template)")
	fl.StringVar(&f.summary, "summary", "", "proposal summary (default: the title)")
	fl.StringVar(&f.metadata, "metadata", "", "proposal metadata (default: the title)")
	fl.StringVar(&f.deposit, "deposit", "", "deposit (default: the chain's minimum)")
	fl.BoolVar(&f.expedited, "expedited", false, "submit as an expedited proposal")
	fl.StringVar(&f.name, "name", "", "upgrade: plan name")
	fl.Int64Var(&f.height, "height", 0, "upgrade: plan height")
	fl.StringVar(&f.info, "info", "", "upgrade: plan info")
	fl.StringVar(&f.module, "module", "", "params: module whose params change, such as staking")
	fl.StringVar(&f.msgType, "msg-type", "", "params: MsgUpdateParams type URL (default: /cosmos.<module>.v1beta1.MsgUpdateParams)")
	fl.StringArrayVar(&f.set, "set", nil, "params: key=value to change, repeatable; value is JSON or a string")
	fl.BoolVar(&autoVote, "auto-vote", false, "vote yes from every lab key that can pass the proposal")
	return cmd
}

// validate checks the template flags before anything talks to the chain.
func (f proposalFlags) validate() error {
	switch f.template {
	case "", "text":
	case "upgrade":
		if f.name == "" || f.height <= 0 {
			return output.Usagef("--template upgrade needs --name and --height")
		}
	case "params":
		if f.module == "" || len(f.set) == 0 {
			return output.Usagef("--template params needs --module and at least one --set")
		}
	default:
		return output.Usagef("--template %q: want text, upgrade, or params", f.template)
	}
	return nil
}

// upgradeWarning is chain.CheckUpgradeHeight's complaint about the plan in f,
// or empty. gov submit only warns; upgrade schedule refuses.
func upgradeWarning(ctx context.Context, c chain.CLI, rpc *chain.Client, profileBlockTime time.Duration, f proposalFlags) (string, error) {
	current, blockTime, voting, err := planWindow(ctx, c, rpc, profileBlockTime, f.expedited)
	if err != nil {
		return "", err
	}
	if err := chain.CheckUpgradeHeight(f.height, current, blockTime, voting); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

// buildProposal fills a template with the gov module authority and the
// chain's minimum deposit.
func buildProposal(ctx context.Context, c chain.CLI, f proposalFlags) (chain.ProposalFile, error) {
	p := chain.ProposalFile{Messages: []any{}, Title: f.title, Summary: f.summary, Metadata: f.metadata, Deposit: f.deposit, Expedited: f.expedited}
	authority := func() (string, error) { return c.ModuleAddress(ctx, "gov") }
	switch f.template {
	case "text":
		p.Title = cmp.Or(p.Title, "text proposal")
	case "upgrade":
		auth, err := authority()
		if err != nil {
			return p, err
		}
		p.Messages = append(p.Messages, chain.SoftwareUpgradeMsg(auth, chain.Plan{Name: f.name, Height: f.height, Info: f.info}))
		p.Title = cmp.Or(p.Title, "upgrade to "+f.name+" at height "+strconv.FormatInt(f.height, 10))
	case "params":
		params, err := c.Params(ctx, f.module)
		if err != nil {
			return p, err
		}
		for _, s := range f.set {
			if err := chain.SetParam(params, s); err != nil {
				return p, output.Usage(err)
			}
		}
		auth, err := authority()
		if err != nil {
			return p, err
		}
		p.Messages = append(p.Messages, chain.UpdateParamsMsg(cmp.Or(f.msgType, chain.UpdateParamsMsgType(f.module)), auth, params))
		p.Title = cmp.Or(p.Title, "update "+f.module+" params")
	}
	p.Summary = cmp.Or(p.Summary, p.Title)
	p.Metadata = cmp.Or(p.Metadata, p.Title)
	if p.Deposit == "" {
		var err error
		if p.Deposit, err = minDeposit(ctx, c, f.expedited); err != nil {
			return p, err
		}
	}
	return p, nil
}

// voteAll broadcasts every vote before waiting on any, since each voter
// signs with its own account sequence.
func voteAll(ctx context.Context, c chain.CLI, rpc *chain.Client, voters []string, id uint64, option string) ([]voteResult, error) {
	hashes := make([]string, len(voters))
	for i, voter := range voters {
		h, err := c.Broadcast(ctx, voter, "gov", "vote", strconv.FormatUint(id, 10), chain.VoteOptions[option])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", voter, err)
		}
		hashes[i] = h
	}
	var out []voteResult
	for i, h := range hashes {
		tx, err := waitTx(ctx, rpc, h)
		if err != nil {
			return out, fmt.Errorf("%s: %w", voters[i], err)
		}
		out = append(out, voteResult{Voter: voters[i], Option: option, TxResult: tx})
	}
	return out, nil
}

func proposalID(arg string) (uint64, error) {
	id, err := strconv.ParseUint(arg, 10, 64)
	if err != nil {
		return 0, output.Usagef("proposal id %q: want a number", arg)
	}
	return id, nil
}

type votesView []voteResult

func (v votesView) WriteHuman(w io.Writer) error {
	for _, vote := range v {
		_, _ = fmt.Fprintf(w, "%s voted %s in tx %s (block %d)\n", vote.Voter, vote.Option, vote.Hash, vote.Height)
	}
	return nil
}

func newGovVoteCmd(a *app, ref *string) *cobra.Command {
	var from []string
	cmd := &cobra.Command{
		Use:   "vote <id> <yes|no|abstain|veto|no_with_veto>",
		Short: "Vote on a proposal from lab keys",
		Long:  "Vote on a proposal from lab keys. veto and no_with_veto are the same option.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := proposalID(args[0])
			if err != nil {
				return err
			}
			if _, ok := chain.VoteOptions[args[1]]; !ok {
				return output.Usagef("vote option %q: want yes, no, abstain, veto, or no_with_veto", args[1])
			}
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			from = dedupe(from)
			for _, k := range from {
				if _, ok := e.account(k); !ok {
					return output.Usagef("--from %q is not a lab key (have %s)", k, e.accountNames())
				}
			}
			c, rpc, err := e.liveCLI(cmd.Context())
			if err != nil {
				return err
			}
			votes, err := voteAll(cmd.Context(), c, rpc, from, id, args[1])
			if err != nil {
				return err
			}
			return a.print(cmd, votesView(votes))
		},
	}
	cmd.Flags().StringSliceVar(&from, "from", []string{govKey}, "lab keys that vote, comma separated")
	return cmd
}

type proposalList []chain.Proposal

func (l proposalList) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tVOTING ENDS\tTITLE")
	for _, p := range l {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", p.ID, p.Status, timeOrDash(p.VotingEndTime), p.Title)
	}
	return tw.Flush()
}

func timeOrDash(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format(time.DateTime)
}

func newGovListCmd(a *app, ref *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List proposals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			c, _, err := e.liveCLI(cmd.Context())
			if err != nil {
				return err
			}
			ps, err := c.Proposals(cmd.Context())
			if err != nil {
				return err
			}
			return a.print(cmd, proposalList(ps))
		},
	}
}

type proposalView struct {
	chain.Proposal
	// Tally is the running count while voting and the final one after.
	Tally chain.Tally `json:"tally"`
}

func (v proposalView) WriteHuman(w io.Writer) error {
	_, _ = fmt.Fprintf(w, "proposal %d: %s\nstatus %s, voting ends %s\n", v.ID, v.Title, v.Status, timeOrDash(v.VotingEndTime))
	if v.FailedReason != "" {
		_, _ = fmt.Fprintf(w, "failed: %s\n", v.FailedReason)
	}
	for _, m := range v.Messages {
		_, _ = fmt.Fprintf(w, "message %s\n", m)
	}
	t := v.Tally
	_, err := fmt.Fprintf(w, "tally yes %s, no %s, abstain %s, veto %s\n", t.Yes, t.No, t.Abstain, t.NoWithVeto)
	return err
}

func newGovShowCmd(a *app, ref *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a proposal with its tally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := proposalID(args[0])
			if err != nil {
				return err
			}
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			c, _, err := e.liveCLI(cmd.Context())
			if err != nil {
				return err
			}
			p, err := c.Proposal(cmd.Context(), id)
			if err != nil {
				return err
			}
			v := proposalView{Proposal: p, Tally: p.FinalTally}
			if p.Status == "VOTING_PERIOD" {
				if v.Tally, err = c.Tally(cmd.Context(), id); err != nil {
					return err
				}
			}
			return a.print(cmd, v)
		},
	}
}

// dedupe drops repeated entries, keeping the first of each.
func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
