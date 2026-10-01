package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/kevinpita/forklab/internal/binary"
	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/profile"
	taskprogress "github.com/kevinpita/forklab/internal/progress"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

func newUpgradeCmd(a *app) *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Schedule, cancel, and follow software upgrades",
	}
	labRefFlag(cmd, &ref)
	cmd.AddCommand(newUpgradeScheduleCmd(a, &ref), newUpgradeCancelCmd(a, &ref), newUpgradeStatusCmd(a, &ref), newUpgradeRecoverCmd(a, &ref))
	return cmd
}

// upgradeNode is one node's place in an upgrade: the version lab.yaml
// records for it and the supervisor's view of its process and swap.
type upgradeNode struct {
	Name      string               `json:"name"`
	Version   string               `json:"version"`
	Binary    string               `json:"binary"`
	State     supervisor.State     `json:"state"`
	Phase     supervisor.SwapPhase `json:"upgrade,omitempty"`
	Halt      *supervisor.Halt     `json:"halt,omitempty"`
	SwapError string               `json:"swap_error,omitempty"`
}

func upgradeNodes(cfg lab.Config, nodes []supervisor.NodeStatus) []upgradeNode {
	out := make([]upgradeNode, 0, len(cfg.Nodes))
	for i, n := range cfg.Nodes {
		row := upgradeNode{Name: n.Name, Version: n.Version}
		if i < len(nodes) {
			s := nodes[i]
			row.Binary, row.State, row.Phase, row.Halt, row.SwapError = s.Binary, s.State, s.Upgrade, s.Halt, s.SwapError
		}
		out = append(out, row)
	}
	return out
}

// swapErrorWidth bounds a swap error in the human table; --json has it all.
const swapErrorWidth = 60

func (n upgradeNode) phaseText() string {
	switch n.Phase {
	case supervisor.SwapHalted:
		return fmt.Sprintf("halted at %d, waiting for swap", n.Halt.Height)
	case supervisor.SwapFailed:
		msg, _, _ := strings.Cut(n.SwapError, "\n")
		if len(msg) > swapErrorWidth {
			msg = msg[:swapErrorWidth] + "..."
		}
		return "swap failed: " + msg + " (see --json)"
	case supervisor.SwapNone:
		return "-"
	}
	return string(n.Phase)
}

func writeUpgradeNodes(w io.Writer, nodes []upgradeNode) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NODE\tVERSION\tSTATE\tUPGRADE\tBINARY")
	for _, n := range nodes {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n.Name, dash(n.Version), dash(string(n.State)), n.phaseText(), dash(n.Binary))
	}
	return tw.Flush()
}

type scheduleView struct {
	ProposalID uint64             `json:"proposal_id"`
	Plan       supervisor.Upgrade `json:"plan"`
	Votes      []voteResult       `json:"votes"`
	// Upgraded is set once every node runs the new binary and the chain
	// produced blocks past the plan height; Height is the chain height then.
	Upgraded bool          `json:"upgraded"`
	Height   int64         `json:"height,omitempty"`
	Nodes    []upgradeNode `json:"nodes"`
}

func (v scheduleView) WriteHuman(w io.Writer) error {
	p := v.Plan
	_, _ = fmt.Fprintf(w, "proposal %d passed: upgrade %q to version %s at height %d\n", v.ProposalID, p.Name, p.Version, p.Height)
	switch {
	case v.Upgraded:
		_, _ = fmt.Fprintf(w, "upgraded: every node runs %s and the chain is at height %d\n", p.Version, v.Height)
	case !p.AutoSwap:
		_, _ = fmt.Fprintf(w, "nodes halt at height %d; restart each with forklab node restart <i> --binary %s\n", p.Height, p.Version)
	default:
		_, _ = fmt.Fprintf(w, "the supervisor swaps each node to %s when it halts; follow with forklab upgrade status\n", p.Version)
	}
	return writeUpgradeNodes(w, v.Nodes)
}

type scheduleFlags struct {
	height     int64
	in         int64
	noAutoSwap bool
	name       string
	expedited  bool
	noWait     bool
	reporter   taskprogress.Reporter
}

func newUpgradeScheduleCmd(a *app, ref *string) *cobra.Command {
	var f scheduleFlags
	var progressFormat string
	cmd := &cobra.Command{
		Use:   "schedule <version> (--height H | --in N)",
		Short: "Upgrade the lab to a profile version through governance",
		Long: "Resolve the binary for version, submit a software upgrade proposal for it\n" +
			"at --height or --in blocks from now, vote it through, and hand the plan to\n" +
			"the supervisor, which restarts each node on the new binary when it halts.\n" +
			"The command then waits for the swap and for blocks past the plan height.\n" +
			"--no-auto-swap leaves halted nodes for forklab node restart --binary, and\n" +
			"--no-wait returns as soon as the plan is registered.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if progressFormat != "" && progressFormat != "json" {
				return output.Usagef("--progress must be json")
			}
			if progressFormat == "json" {
				f.reporter = taskprogress.JSON(cmd.ErrOrStderr())
			}
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			return schedule(cmd, a, e, args[0], f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&progressFormat, "progress", "", "live progress on stderr (json)")
	fl.Int64Var(&f.height, "height", 0, "plan height")
	fl.Int64Var(&f.in, "in", 0, "plan height as blocks from the current height")
	fl.BoolVar(&f.noAutoSwap, "no-auto-swap", false, "leave halted nodes for a manual node restart --binary")
	fl.StringVar(&f.name, "name", "", "plan name (default: the profile's upgrade_name for the version)")
	fl.BoolVar(&f.expedited, "expedited", false, "submit as an expedited proposal")
	fl.BoolVar(&f.noWait, "no-wait", false, "return once the plan is registered instead of waiting for the swap")
	cmd.MarkFlagsOneRequired("height", "in")
	cmd.MarkFlagsMutuallyExclusive("height", "in")
	return cmd
}

// swapGrace is added to the plan's ETA when waiting for the swap: a node
// takes a stop timeout to leave, a probe to be trusted on the new binary,
// and blocks may be slower than measured.
const swapGrace = 3 * time.Minute

// pastPlanTimeout bounds waiting for blocks after the plan height.
const pastPlanTimeout = 2 * time.Minute

// pastPlanBlocks is how many blocks after the plan height prove the upgrade
// handler ran and the chain went on.
const pastPlanBlocks = 3

func schedule(cmd *cobra.Command, a *app, e labEnv, version string, f scheduleFlags) error {
	ctx := cmd.Context()
	unlock, err := lockUpgrade(e.dir)
	if err != nil {
		return err
	}
	defer unlock()
	if f.height <= 0 && f.in <= 0 {
		return output.Usagef("--height and --in must be positive")
	}
	if !slices.ContainsFunc(e.cfg.Nodes, func(n lab.Node) bool { return n.Version != version }) {
		return fmt.Errorf("every node already runs version %s", version)
	}
	bin, err := resolveBinary(ctx, a, cmd.ErrOrStderr(), e.profile, version, binary.Options{Reporter: f.reporter})
	if err != nil {
		return err
	}
	f.reporter.Emit("upgrade.prepare", "Checking upgrade window", taskprogress.Started)
	c, rpc, err := e.liveCLI(ctx)
	if err != nil {
		return err
	}
	if plan, err := c.UpgradePlan(ctx); err != nil {
		return err
	} else if plan != nil {
		return fmt.Errorf("upgrade %q is already scheduled at height %d; run forklab upgrade cancel first", plan.Name, plan.Height)
	}
	sup, err := ensureSupervisor(ctx, e.dir)
	if err != nil {
		return err
	}
	if st, err := sup.Upgrade(); err != nil {
		return err
	} else if pending := st.Upgrade; pending != nil && pending.ProposalID != 0 {
		if prop, err := c.Proposal(ctx, pending.ProposalID); err == nil && !terminal(prop.Status) {
			return fmt.Errorf("upgrade %q is registered and its proposal %d is still in %s; wait for it or run forklab upgrade cancel", pending.Name, pending.ProposalID, prop.Status)
		}
	}
	st, err := sup.Upgrade()
	if err != nil {
		return err
	}
	if st.Upgrade != nil && st.Upgrade.Recovery != nil {
		return errors.New("upgrade recovery is pending; finish it before scheduling another upgrade")
	}
	if st.RecoveryProtocol < 1 {
		if err := sup.Exit(); err != nil {
			return err
		}
		if sup, err = ensureSupervisor(ctx, e.dir); err != nil {
			return err
		}
		st, err = sup.Upgrade()
		if err != nil {
			return err
		}
		if st.RecoveryProtocol < 1 {
			return errors.New("supervisor cannot preserve original upgrade binaries; rebuild forklab")
		}
	}
	current, blockTime, voting, err := planWindow(ctx, c, rpc, e.profile.BlockTime, f.expedited)
	if err != nil {
		return err
	}
	height := f.height
	if f.in > 0 {
		height = current + f.in
	}
	if err := chain.CheckUpgradeHeight(height, current, blockTime, voting); err != nil {
		return err
	}
	name := cmp.Or(f.name, e.profile.UpgradeName.Expand(profile.Vars{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, ChainID: e.cfg.ChainID}))
	plan := supervisor.Upgrade{Name: name, Height: height, Version: version, Binary: bin.Path, AutoSwap: !f.noAutoSwap}
	specs, err := supervisor.LoadNodes(e.dir)
	if err != nil {
		return err
	}
	for i, spec := range specs {
		plan.Previous = append(plan.Previous, supervisor.UpgradeTarget{Index: i, Binary: spec.Binary, Version: e.cfg.Nodes[i].Version})
	}

	p, err := buildProposal(ctx, c, proposalFlags{template: "upgrade", name: name, height: height, info: "forklab upgrade to " + version, expedited: f.expedited})
	if err != nil {
		return err
	}
	// The supervisor knows the plan before the chain does, so a command that
	// dies after the proposal passes still leaves the swap to happen.
	if _, err := sup.SetUpgrade(&plan); err != nil {
		return err
	}
	f.reporter.Emit("upgrade.prepare", fmt.Sprintf("Plan registered at height %d", height), taskprogress.Completed)
	v := scheduleView{Plan: plan}
	v.ProposalID, v.Votes, err = submitAndPass(ctx, c, rpc, e.cfg, p, voting, func(id uint64) {
		plan.ProposalID = id
		_, _ = sup.SetUpgrade(&plan)
	}, f.reporter)
	if errors.Is(err, errUndecided) {
		return fmt.Errorf("%w; the plan stays registered with the supervisor and the nodes are swapped when they halt; follow with forklab upgrade status", err)
	}
	if err != nil {
		// Submission failed or the proposal is rejected: nothing will halt,
		// so the supervisor forgets the plan.
		if sup, dialErr := ensureSupervisor(ctx, e.dir); dialErr == nil {
			_, _ = sup.SetUpgrade(nil)
		}
		return err
	}
	v.Plan = plan
	nodes, err := sup.Status()
	if err != nil {
		return err
	}
	v.Nodes = upgradeNodes(e.cfg, nodes)
	if f.noWait || f.noAutoSwap {
		return a.print(cmd, v)
	}

	swapCtx, cancel := context.WithTimeout(ctx, time.Duration(height-current)*blockTime+swapGrace)
	defer cancel()
	if nodes, err = waitSwapped(swapCtx, e.dir, name, f.reporter); err != nil {
		return fmt.Errorf("upgrade %q: %w%s", name, err, e.unswappedLogs(nodes))
	}
	pastCtx, cancelPast := context.WithTimeout(ctx, pastPlanTimeout)
	defer cancelPast()
	if v.Height, err = e.waitPast(pastCtx, height+pastPlanBlocks, f.reporter); err != nil {
		return fmt.Errorf("upgrade %q: every node was swapped to %s, but the chain did not pass height %d:\n%w", name, bin.Path, height+pastPlanBlocks, err)
	}
	v.Upgraded = true
	f.reporter.Emit("upgrade.complete", "Recording completed upgrade", taskprogress.Started)
	if sup, err = ensureSupervisor(ctx, e.dir); err == nil {
		_, err = sup.CompleteExpectedUpgrade(&plan)
	}
	if err != nil {
		return fmt.Errorf("upgrade %q: the chain passed height %d on %s, but the supervisor did not mark the upgrade completed: %w", name, v.Height, version, err)
	}
	if cfg, err := lab.Load(e.dir); err == nil {
		e.cfg = cfg
	}
	v.Nodes = upgradeNodes(e.cfg, nodes)
	f.reporter.Emit("upgrade.complete", "Upgrade completed", taskprogress.Completed)
	return a.print(cmd, v)
}

// planWindow is what a plan height is checked against: the current height,
// the block time (measured, or the profile's before enough blocks exist),
// and the voting period a proposal submitted now would take.
func planWindow(ctx context.Context, c chain.CLI, rpc *chain.Client, profileBlockTime time.Duration, expedited bool) (current int64, blockTime, voting time.Duration, err error) {
	params, err := c.GovParams(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	period := params.VotingPeriod
	if expedited {
		period = params.ExpeditedVotingPeriod
	}
	if voting, err = time.ParseDuration(period); err != nil {
		return 0, 0, 0, fmt.Errorf("gov voting period %q: %w", period, err)
	}
	st, err := rpc.Status(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	blockTime, err = rpc.AvgBlockTime(ctx, 10)
	if errors.Is(err, chain.ErrNotEnoughBlocks) || (err == nil && blockTime <= 0) {
		blockTime, err = profileBlockTime, nil
	}
	if err != nil {
		return 0, 0, 0, err
	}
	return st.LatestHeight, blockTime, voting, nil
}

// errUndecided marks an error after submission while the proposal can still
// pass, so the plan must stay registered with the supervisor.
var errUndecided = errors.New("the proposal may still pass")

// terminal reports whether a proposal status can no longer change.
func terminal(status string) bool {
	return status != "VOTING_PERIOD" && status != "DEPOSIT_PERIOD"
}

// submitAndPass submits p from the gov key, tells submitted its id, votes
// yes from every lab key that can pass it, and waits for the proposal to
// pass. A proposal that ends any other way is an error naming the chain's
// reason; any other error after submission wraps errUndecided.
func submitAndPass(ctx context.Context, c chain.CLI, rpc *chain.Client, cfg lab.Config, p chain.ProposalFile, voting time.Duration, submitted func(id uint64), report taskprogress.Reporter) (uint64, []voteResult, error) {
	report.Emit("upgrade.submit", "Submitting upgrade proposal", taskprogress.Started)
	hash, err := c.SubmitProposal(ctx, govKey, p)
	if err != nil {
		return 0, nil, err
	}
	report.Emit("upgrade.submit", "Proposal broadcast", taskprogress.Completed)
	report.Emit("upgrade.confirm", "Waiting for proposal transaction", taskprogress.Started)
	tx, err := waitTx(ctx, rpc, hash)
	if err != nil {
		return 0, nil, err
	}
	id, err := chain.ProposalID(tx)
	if err != nil {
		return 0, nil, err
	}
	report.Emit("upgrade.confirm", fmt.Sprintf("Proposal #%d confirmed", id), taskprogress.Completed)
	submitted(id)
	report.Emit("upgrade.vote", fmt.Sprintf("Voting yes on proposal #%d", id), taskprogress.Started)
	undecided := func(err error) error { return fmt.Errorf("%w (%w)", err, errUndecided) }
	votes, err := voteAll(ctx, c, rpc, autoVoters(cfg), id, "yes")
	if err != nil {
		return id, votes, undecided(fmt.Errorf("proposal %d submitted, but voting failed: %w", id, err))
	}
	report.Emit("upgrade.vote", fmt.Sprintf("%d votes confirmed", len(votes)), taskprogress.Completed)
	report.Emit("upgrade.voting", "Waiting for voting to close", taskprogress.Started)
	waitCtx, cancel := context.WithTimeout(ctx, voting+txTimeout)
	defer cancel()
	for {
		prop, err := c.Proposal(waitCtx, id)
		if err != nil && waitCtx.Err() == nil {
			return id, votes, undecided(fmt.Errorf("proposal %d: %w", id, err))
		}
		if err == nil {
			if report != nil {
				detail := fmt.Sprintf("Proposal #%d · %s", id, prop.Status)
				if prop.VotingEndTime != nil {
					detail += " · voting ends " + prop.VotingEndTime.Local().Format("15:04:05 MST")
				}
				report(taskprogress.Event{Phase: "upgrade.voting", Message: "Waiting for voting to close", Detail: detail, State: taskprogress.Updated})
			}
			if prop.Status == "PASSED" {
				report.Emit("upgrade.voting", fmt.Sprintf("Proposal #%d passed", id), taskprogress.Completed)
				return id, votes, nil
			}
			if terminal(prop.Status) {
				return id, votes, fmt.Errorf("proposal %d %s: %s", id, prop.Status, cmp.Or(prop.FailedReason, "not enough yes votes"))
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return id, votes, undecided(fmt.Errorf("proposal %d still voting: %w", id, ctx.Err()))
			}
			return id, votes, undecided(fmt.Errorf("proposal %d still voting after %s", id, voting+txTimeout))
		case <-time.After(time.Second):
		}
	}
}

// waitSwapped polls the supervisor until every node is swapped to plan name,
// spawning a new supervisor when the last one died so it finishes the swap.
// A plan another command already completed counts as swapped. A failed swap
// or a cleared plan ends the wait.
func waitSwapped(ctx context.Context, dir, name string, report taskprogress.Reporter) ([]supervisor.NodeStatus, error) {
	report.Emit("upgrade.swap", "Waiting for upgrade halt and restart", taskprogress.Started)
	last := errors.New("no supervisor status yet")
	var nodes []supervisor.NodeStatus
	for {
		sup, err := ensureSupervisor(ctx, dir)
		if err == nil {
			var st supervisor.Response
			if st, err = sup.Upgrade(); err == nil {
				nodes = st.Nodes
				if report != nil {
					detail := swapSummary(nodes)
					if st.Upgrade != nil {
						detail = fmt.Sprintf("Halt at height %d · %s", st.Upgrade.Height, detail)
					}
					report(taskprogress.Event{Phase: "upgrade.swap", Message: "Waiting for upgrade halt and restart", Detail: detail, State: taskprogress.Updated})
				}
				if st.Upgrade == nil && st.Completed != nil && st.Completed.Name == name {
					report.Emit("upgrade.swap", "All nodes restarted on the new binary", taskprogress.Completed)
					return nodes, nil
				}
				if st.Upgrade == nil {
					return nodes, errors.New("the plan was cleared from the supervisor")
				}
				if st.Upgrade.Name != name || st.Upgrade.Recovery != nil {
					return nodes, errors.New("pending upgrade changed or recovery started")
				}
				done := true
				for _, n := range nodes {
					if n.Upgrade == supervisor.SwapFailed || n.Upgrade == supervisor.SwapDone && n.State != supervisor.StateRunning {
						return nodes, fmt.Errorf("%s: %s (%s)", n.Name, n.State, n.SwapError)
					}
					done = done && n.Upgrade == supervisor.SwapDone
				}
				if done {
					report.Emit("upgrade.swap", "All nodes restarted on the new binary", taskprogress.Completed)
					return nodes, nil
				}
				err = fmt.Errorf("nodes at %s", swapSummary(nodes))
			}
		}
		if ctx.Err() == nil {
			last = err
		}
		select {
		case <-ctx.Done():
			return nodes, fmt.Errorf("waiting for every node to swap: %w (%v)", ctx.Err(), last)
		case <-time.After(time.Second):
		}
	}
}

func swapSummary(nodes []supervisor.NodeStatus) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = n.Name + " " + cmp.Or(string(n.Upgrade), string(n.State))
	}
	return strings.Join(parts, ", ")
}

// unswappedLogs names the log of every node not yet swapped, with its
// last error lines.
func (e labEnv) unswappedLogs(nodes []supervisor.NodeStatus) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.Upgrade == supervisor.SwapDone && n.State == supervisor.StateRunning || n.Index >= len(e.cfg.Nodes) {
			continue
		}
		logPath := filepath.Join(lab.NodeHome(e.dir, e.cfg.Nodes[n.Index]), "node.log")
		fmt.Fprintf(&b, "\n%s: see %s\n%s", n.Name, logPath, lastErrorLines(logPath, 5))
	}
	return b.String()
}

// waitPast waits for every node to report height h and returns the highest
// height seen. A node that does not get there is reported with its log
// path and its last error lines.
func (e labEnv) waitPast(ctx context.Context, h int64, report taskprogress.Reporter) (int64, error) {
	report.Emit("upgrade.blocks", fmt.Sprintf("Waiting for blocks through height %d", h), taskprogress.Started)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				sup, err := supervisor.Dial(e.dir)
				if err != nil {
					cancel(err)
					return
				}
				nodes, err := sup.Status()
				if err != nil {
					cancel(err)
					return
				}
				for _, n := range nodes {
					if n.State != supervisor.StateRunning {
						cancel(fmt.Errorf("%s exited after restart%s", n.Name, e.unswappedLogs(nodes)))
						return
					}
				}
			}
		}
	}()
	heights := make([]int64, len(e.clients))
	errs := make([]error, len(e.clients))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var reached int64
	total := int64(len(e.clients))
	for i, c := range e.clients {
		wg.Go(func() {
			heights[i], errs[i] = c.WaitHeight(ctx, h)
			if errs[i] == nil && report != nil {
				mu.Lock()
				reached++
				report(taskprogress.Event{Phase: "upgrade.blocks", Message: fmt.Sprintf("Waiting for blocks through height %d", h), Detail: fmt.Sprintf("%s reached height %d", e.cfg.Nodes[i].Name, heights[i]), State: taskprogress.Updated, Done: reached, Total: &total, Unit: "nodes"})
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	var top int64
	for i, err := range errs {
		top = max(top, heights[i])
		if err != nil {
			logPath := filepath.Join(lab.NodeHome(e.dir, e.cfg.Nodes[i]), "node.log")
			errs[i] = fmt.Errorf("%s: %w; see %s\n%s", e.cfg.Nodes[i].Name, err, logPath, lastErrorLines(logPath, 5))
		}
	}
	err := errors.Join(errs...)
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	if err == nil {
		report.Emit("upgrade.blocks", fmt.Sprintf("Chain resumed at height %d", top), taskprogress.Completed)
	}
	return top, err
}

// lastErrorLines returns the last n lines of a node log that mention an
// error or a panic, colors stripped and indented.
func lastErrorLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "  " + err.Error()
	}
	var found []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		line = supervisor.StripANSI(line)
		if strings.HasPrefix(strings.TrimSpace(line), "-") {
			continue
		}
		if strings.Contains(strings.ToLower(line), "error") || strings.Contains(line, "ERR") || strings.Contains(line, "panic") {
			found = append(found, "  "+line)
		}
	}
	if len(found) > n {
		found = found[len(found)-n:]
	}
	return strings.Join(found, "\n")
}

type cancelView struct {
	ProposalID uint64       `json:"proposal_id"`
	Plan       chain.Plan   `json:"plan"`
	Votes      []voteResult `json:"votes"`
	Warnings   []string     `json:"warnings,omitempty"`
}

func (v cancelView) WriteHuman(w io.Writer) error {
	_, _ = fmt.Fprintf(w, "proposal %d passed: upgrade %q at height %d is cancelled\n", v.ProposalID, v.Plan.Name, v.Plan.Height)
	return nil
}

func newUpgradeCancelCmd(a *app, ref *string) *cobra.Command {
	var expedited bool
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel the scheduled upgrade through governance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			unlock, err := lockUpgrade(e.dir)
			if err != nil {
				return err
			}
			defer unlock()
			c, rpc, err := e.liveCLI(ctx)
			if err != nil {
				return err
			}
			plan, err := c.UpgradePlan(ctx)
			if err != nil {
				return err
			}
			if plan == nil {
				return errors.New("no upgrade is scheduled")
			}
			current, blockTime, voting, err := planWindow(ctx, c, rpc, e.profile.BlockTime, expedited)
			if err != nil {
				return err
			}
			v := cancelView{Plan: *plan}
			if err := chain.CheckUpgradeHeight(plan.Height, current, blockTime, voting); err != nil {
				warn := "the cancel vote may end after the plan height: " + err.Error()
				v.Warnings = append(v.Warnings, warn)
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warn)
			}
			auth, err := c.ModuleAddress(ctx, "gov")
			if err != nil {
				return err
			}
			title := fmt.Sprintf("cancel upgrade %s at height %d", plan.Name, plan.Height)
			p := chain.ProposalFile{Messages: []any{chain.CancelUpgradeMsg(auth)}, Title: title, Summary: title, Metadata: title, Expedited: expedited}
			if p.Deposit, err = minDeposit(ctx, c, expedited); err != nil {
				return err
			}
			if v.ProposalID, v.Votes, err = submitAndPass(ctx, c, rpc, e.cfg, p, voting, func(uint64) {}, nil); err != nil {
				return err
			}
			sup, err := ensureSupervisor(ctx, e.dir)
			if err != nil {
				return err
			}
			if _, err := sup.SetUpgrade(nil); err != nil {
				return fmt.Errorf("proposal %d passed, but clearing the supervisor's plan failed: %w", v.ProposalID, err)
			}
			return a.print(cmd, v)
		},
	}
	cmd.Flags().BoolVar(&expedited, "expedited", false, "submit as an expedited proposal")
	return cmd
}

type upgradeStatusView struct {
	Lab string `json:"lab"`
	// Plan is what the chain has scheduled; Pending what the supervisor
	// swaps to; Completed the last upgrade every node was swapped to and
	// the chain went past.
	Plan      *chain.Plan         `json:"plan"`
	Pending   *supervisor.Upgrade `json:"pending"`
	Completed *supervisor.Upgrade `json:"completed"`
	Nodes     []upgradeNode       `json:"nodes"`
	Warnings  []string            `json:"warnings,omitempty"`
}

func (v upgradeStatusView) WriteHuman(w io.Writer) error {
	if v.Plan == nil {
		_, _ = fmt.Fprintln(w, "chain: no upgrade scheduled")
	} else {
		_, _ = fmt.Fprintf(w, "chain: upgrade %q at height %d\n", v.Plan.Name, v.Plan.Height)
	}
	switch {
	case v.Pending == nil && v.Completed != nil:
		c := v.Completed
		_, _ = fmt.Fprintf(w, "supervisor: last upgrade %q to version %s completed at height %d\n", c.Name, c.Version, c.Height)
	case v.Pending == nil:
		_, _ = fmt.Fprintln(w, "supervisor: no pending upgrade")
	default:
		swap := "manual swap"
		if v.Pending.AutoSwap {
			swap = "auto swap"
		}
		_, _ = fmt.Fprintf(w, "supervisor: upgrade %q at height %d to version %s (%s)\n", v.Pending.Name, v.Pending.Height, v.Pending.Version, swap)
	}
	for _, warn := range v.Warnings {
		_, _ = fmt.Fprintf(w, "warning: %s\n", warn)
	}
	return writeUpgradeNodes(w, v.Nodes)
}

func newUpgradeStatusCmd(a *app, ref *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the scheduled plan and every node's version and swap state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			v := upgradeStatusView{Lab: e.cfg.Name}
			sup, dialErr := supervisor.Dial(e.dir)
			var st supervisor.Response
			if dialErr == nil {
				st, err = sup.Upgrade()
			} else {
				st, err = supervisor.LoadUpgradeStatus(e.dir)
				v.Warnings = append(v.Warnings, "supervisor offline; showing persisted upgrade")
			}
			if err != nil {
				return err
			}
			// Halted nodes answer no query, so the chain's plan is best effort.
			c, rpc, err := e.liveCLI(ctx)
			if err != nil {
				v.Warnings = append(v.Warnings, err.Error())
			} else if v.Plan, err = c.UpgradePlan(ctx); err != nil {
				v.Warnings = append(v.Warnings, "upgrade plan: "+err.Error())
			}
			if sup != nil && rpc != nil && st.Upgrade != nil && st.Upgrade.Recovery == nil && allSwapped(st.Nodes) {
				ready := true
				for i, rpc := range e.clients {
					chainSt, queryErr := rpc.Status(ctx)
					if queryErr != nil || chainSt.LatestHeight < st.Upgrade.Height+pastPlanBlocks || st.Nodes[i].Binary != st.Upgrade.Binary {
						ready = false
						break
					}
				}
				if ready {
					if done, err := sup.CompleteExpectedUpgrade(st.Upgrade); err != nil {
						v.Warnings = append(v.Warnings, "complete upgrade: "+err.Error())
					} else {
						st = done
					}
				}
			}
			v.Pending, v.Completed, v.Nodes = st.Upgrade, st.Completed, upgradeNodes(e.cfg, st.Nodes)
			if v.Pending != nil {
				for i := range v.Nodes {
					if v.Nodes[i].State != supervisor.StateRunning {
						if diagnostic := strings.TrimSpace(lastErrorLines(filepath.Join(lab.NodeHome(e.dir, e.cfg.Nodes[i]), "node.log"), 1)); diagnostic != "" {
							v.Nodes[i].SwapError = diagnostic
						}
					}
				}
			}
			return a.print(cmd, v)
		},
	}
}

func allSwapped(nodes []supervisor.NodeStatus) bool {
	return !slices.ContainsFunc(nodes, func(n supervisor.NodeStatus) bool {
		return n.Upgrade != supervisor.SwapDone || n.State != supervisor.StateRunning
	})
}

// minDeposit is the chain's minimum deposit for a proposal.
func minDeposit(ctx context.Context, c chain.CLI, expedited bool) (string, error) {
	params, err := c.GovParams(ctx)
	if err != nil {
		return "", err
	}
	if expedited {
		return params.ExpeditedMinDeposit.String(), nil
	}
	return params.MinDeposit.String(), nil
}

// restartBinary turns node restart's --binary into the path the node runs
// and the profile version to record: a value with a slash is a path and
// needs --version, anything else a version of the lab's profile, fetched
// if needed.
func restartBinary(ctx context.Context, a *app, stderr io.Writer, labDir, arg, version string) (string, string, error) {
	switch {
	case arg == "":
		return "", "", nil
	case strings.Contains(arg, "/") && version == "":
		return "", "", output.Usagef("--binary %s is a path; add --version <profile version> so lab.yaml records what the node runs", arg)
	case strings.Contains(arg, "/"):
		return arg, version, nil
	case version != "":
		return "", "", output.Usagef("--version goes with a --binary path; --binary %s is already a version", arg)
	}
	cfg, err := lab.Load(labDir)
	if err != nil {
		return "", "", err
	}
	p, err := cfg.Profile.Profile()
	if err != nil {
		return "", "", fmt.Errorf("lab %s: profile: %w", cfg.Name, err)
	}
	b, err := resolveBinary(ctx, a, stderr, p, arg, binary.Options{})
	if err != nil {
		return "", "", err
	}
	return b.Path, arg, nil
}

// recordVersion writes the profile version a node runs into lab.yaml. The
// lab's own Version stays the creation version, which lab reset goes back
// to. The supervisor is the only writer and serializes the calls.
func recordVersion(labDir string) func(index int, version string) error {
	return func(index int, version string) error {
		c, err := lab.Load(labDir)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(c.Nodes) {
			return fmt.Errorf("node %d: lab %s has %d nodes", index, c.Name, len(c.Nodes))
		}
		c.Nodes[index].Version = version
		return lab.Save(labDir, c)
	}
}
