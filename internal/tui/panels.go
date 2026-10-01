package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type panelID int

const (
	panelNodes panelID = iota
	panelConsensus
	panelProposals
	panelUpgrades
	panelAccounts
	panelLabs
	panelProfiles
	panelBinaries
	numPanels
)

// panelSpec is one numbered panel: its list on the left, what the main pane
// shows for its selection, and the command that reproduces that view.
type panelSpec struct {
	title   string
	rows    func(*Model) []listRow
	summary func(*Model) string
	main    func(m *Model, w, h int) mainView
	view    func(*Model) Command
	keys    scope
	hasKeys bool
}

func (p panelSpec) count(m *Model) int { return len(p.rows(m)) }

func (p panelSpec) scope() (scope, bool) { return p.keys, p.hasKeys }

type listRow struct {
	glyph string
	style lipgloss.Style
	text  string
	aside string
}

type mainView struct {
	title, right string
	lines        []string
	// tail keeps the last lines in view instead of scrolling from the top.
	tail bool
}

var panels [numPanels]panelSpec

func init() {
	panels = [numPanels]panelSpec{
		panelNodes: {
			title: "Nodes", rows: nodeRows, summary: nodeSummary, main: nodeMain,
			view: func(m *Model) Command {
				if n, ok := m.selectedNode(); ok {
					return logsCmd(itoa(n.Index))
				}
				return Command{"node", "list"}
			},
			keys: scopeNodes, hasKeys: true,
		},
		panelConsensus: {
			title: "Consensus", rows: consensusRows, summary: consensusSummary, main: consensusMain,
			view: func(*Model) Command { return Command{"consensus", "-w"} },
		},
		panelProposals: {
			title: "Proposals", rows: proposalRows, summary: countSummary(func(m *Model) int { return len(m.proposals) }), main: proposalMain,
			view: func(m *Model) Command {
				if p, ok := m.selectedProposal(); ok {
					return Command{"gov", "show", strconv.FormatUint(p.ID, 10)}
				}
				return Command{"gov", "list"}
			},
			keys: scopeProposals, hasKeys: true,
		},
		panelUpgrades: {
			title: "Upgrades", rows: upgradeRows, summary: upgradeSummary, main: upgradeMain,
			view: func(*Model) Command { return Command{"upgrade", "status"} },
			keys: scopeUpgrades, hasKeys: true,
		},
		panelAccounts: {
			title: "Accounts", rows: accountRows, summary: countSummary(func(m *Model) int { return len(m.accounts) }), main: accountMain,
			view: func(*Model) Command { return Command{"account", "list"} },
			keys: scopeAccounts, hasKeys: true,
		},
		panelLabs: {
			title: "Labs", rows: labRows, summary: countSummary(func(m *Model) int { return len(m.labs) }), main: labMain,
			view: func(m *Model) Command {
				return Command{"lab", "list"}
			},
			keys: scopeLabs, hasKeys: true,
		},
		panelProfiles: {
			title: "Profiles", rows: profileRows, summary: countSummary(func(m *Model) int { return len(m.profiles) }), main: profileMain,
			view: func(m *Model) Command {
				if p, ok := m.selectedProfile(); ok {
					return Command{"profile", "show", p.Name}
				}
				return Command{"profile", "list"}
			},
			keys: scopeProfiles, hasKeys: true,
		},
		panelBinaries: {
			title: "Binaries", rows: binaryRows, summary: countSummary(func(m *Model) int { return len(m.binaries) }), main: binaryMain,
			view: func(*Model) Command { return Command{"binary", "list"} },
			keys: scopeBinaries, hasKeys: true,
		},
	}
}

func countSummary(n func(*Model) int) func(*Model) string {
	return func(m *Model) string {
		if c := n(m); c > 0 {
			return strconv.Itoa(c)
		}
		return ""
	}
}

// Nodes

func (m *Model) nodeState(n nodeInfo) (string, lipgloss.Style, string) {
	for _, u := range m.upgradeNodes() {
		if u.Name == n.Name && u.Phase != "" && u.Phase != "swapped" {
			return "◆", m.th.Warn, strings.ReplaceAll(u.Phase, "_", " ")
		}
	}
	switch n.State {
	case "running":
		return "●", m.th.Good, "running"
	case "exited":
		if n.Signal != "" {
			return "✗", m.th.Bad, strings.ToLower(n.Signal)
		}
		return "○", m.th.Bad, "exited"
	}
	return "○", m.th.Dim, n.State
}

func (m *Model) upgradeNodes() []upgradeNode {
	if m.upgrade == nil {
		return nil
	}
	return m.upgrade.Nodes
}

func (m *Model) nodeChain(name string) (nodeStatus, bool) {
	if m.status == nil {
		return nodeStatus{}, false
	}
	for _, s := range m.status.Nodes {
		if s.Name == name {
			return s, true
		}
	}
	return nodeStatus{}, false
}

func (m *Model) nodeVersion(name string) string {
	for _, u := range m.upgradeNodes() {
		if u.Name == name {
			return u.Version
		}
	}
	return ""
}

func nodeRows(m *Model) []listRow {
	rows := make([]listRow, 0, len(m.nodes))
	for _, n := range m.nodes {
		g, st, state := m.nodeState(n)
		aside := state
		if s, ok := m.nodeChain(n.Name); ok && s.Up {
			aside = fmt.Sprintf("h%d  %dp", s.Height, s.Peers)
		}
		text := n.Name
		if v := m.nodeVersion(n.Name); v != "" {
			text += "  " + m.th.Dim.Render(v)
		}
		rows = append(rows, listRow{glyph: g, style: st, text: text, aside: aside})
	}
	return rows
}

func nodeSummary(m *Model) string {
	if len(m.nodes) == 0 {
		return ""
	}
	up := 0
	for _, n := range m.nodes {
		if n.State == "running" {
			up++
		}
	}
	return fmt.Sprintf("%d/%d up", up, len(m.nodes))
}

func nodeMain(m *Model, w, h int) mainView {
	n, ok := m.selectedNode()
	if !ok {
		return mainView{title: "Node", lines: m.emptyHint(loadNodes, "no nodes")}
	}
	th := m.th
	g, st, state := m.nodeState(n)
	head := st.Render(g) + " " + th.Title.Render(n.Name) + "  " + st.Render(state)
	switch {
	case n.State == "running":
		head += "  " + kv(th, "pid", strconv.Itoa(n.PID)) + "  " + kv(th, "up", fmtDur(time.Duration(n.UptimeSeconds)*time.Second))
	case n.ExitCode != nil:
		head += "  " + kv(th, "code", strconv.Itoa(*n.ExitCode))
	}
	if n.ExitedAt != nil && n.State != "running" {
		head += "  " + kv(th, "at", n.ExitedAt.Local().Format(time.TimeOnly))
	}
	var chain string
	if s, ok := m.nodeChain(n.Name); ok {
		chain = kv(th, "rpc", ":"+strconv.Itoa(s.RPCPort))
		if s.Up {
			chain += "  " + kv(th, "height", strconv.FormatInt(s.Height, 10)) + "  " + kv(th, "peers", strconv.Itoa(s.Peers))
			if s.CatchingUp {
				chain += "  " + th.Warn.Render("catching up")
			}
		} else if s.Error != "" {
			chain += "  " + th.Bad.Render("rpc down")
		}
	}
	if v := m.nodeVersion(n.Name); v != "" {
		chain += "  " + kv(th, "version", v)
	}
	lines := []string{head, chain + "  " + th.Dim.Render(n.Binary), th.Border.Render(strings.Repeat("─", max(w, 0)))}
	if m.upgradeNeedsRecovery() {
		lines = append(lines[:2], th.Warn.Render(m.recoveryHint()))
		for _, u := range m.upgrade.Nodes {
			if u.Name == n.Name && u.SwapError != "" {
				diagnostic := strings.Split(ansi.Wrap(th.Bad.Render(u.SwapError), max(w, 1), ""), "\n")
				lines = append(lines, diagnostic[:min(len(diagnostic), 3)]...)
			}
		}
		lines = append(lines, th.Border.Render(strings.Repeat("─", max(w, 0))))
	}
	rows := max(h-len(lines), 0)
	var logs []string
	for _, l := range m.logs.window(rows) {
		if m.logs.wrap && w > 0 {
			logs = append(logs, strings.Split(ansi.Hardwrap(colorLog(th, l), w, true), "\n")...)
		} else {
			logs = append(logs, colorLog(th, l))
		}
	}
	lines = append(lines, logs[max(len(logs)-rows, 0):]...)
	right := th.Good.Render("● following")
	if !m.logs.follow {
		right = th.Warn.Render(fmt.Sprintf("‖ paused +%d", m.logs.offset))
	}
	if err := m.streams[streamLogs].err; err != nil {
		right = th.Bad.Render("log stream: " + oneLine(err.Error()))
	}
	return mainView{title: "Logs " + n.Name, right: right + th.Dim.Render(fmt.Sprintf("  %d lines", len(m.logs.lines))), lines: lines}
}

func colorLog(th Theme, line string) string {
	fields := strings.SplitN(line, " ", 3)
	if len(fields) < 3 {
		return th.Text.Render(line)
	}
	var lvl lipgloss.Style
	switch fields[1] {
	case "INF":
		lvl = th.Good
	case "WRN":
		lvl = th.Warn
	case "ERR", "FTL", "PNC":
		lvl = th.Bad
	case "DBG":
		lvl = th.Dim
	default:
		return th.Text.Render(line)
	}
	msg, kvs := fields[2], ""
	if i := strings.Index(msg, "="); i > 0 {
		if j := strings.LastIndex(msg[:i], " "); j > 0 {
			msg, kvs = msg[:j], msg[j:]
		}
	}
	return th.Dim.Render(fields[0]) + " " + lvl.Render(fields[1]) + " " + th.Text.Render(msg) + th.Dim.Render(kvs)
}

// Consensus

func voteMark(th Theme, v vote) string {
	switch v.Kind {
	case "block":
		return th.Good.Render("✓")
	case "nil":
		return th.Warn.Render("∅")
	}
	return th.Dim.Render("·")
}

func consensusRows(m *Model) []listRow {
	c := m.consensus
	if c == nil {
		return nil
	}
	rows := make([]listRow, 0, len(c.Validators))
	for _, v := range c.Validators {
		name := cmpOr(v.Node, shortHash(v.Address))
		g, st := "●", m.th.Good
		if !m.peerUp(v.Node) {
			g, st = "○", m.th.Bad
		}
		if v.Address == c.ProposerAddress {
			name += " " + m.th.Accent2.Render("★")
		}
		rows = append(rows, listRow{
			glyph: g, style: st, text: name,
			aside: "pv " + voteMark(m.th, v.Prevote) + " pc " + voteMark(m.th, v.Precommit),
		})
	}
	return rows
}

func (m *Model) peerUp(node string) bool {
	if m.consensus == nil {
		return false
	}
	for _, n := range m.consensus.Nodes {
		if n.Name == node {
			return n.Up
		}
	}
	return node == ""
}

func consensusSummary(m *Model) string {
	if c := m.consensus; c != nil {
		return fmt.Sprintf("r%d %s", c.Round, c.Step)
	}
	return ""
}

func consensusMain(m *Model, w, _ int) mainView {
	c, th := m.consensus, m.th
	if c == nil {
		return mainView{title: "Consensus", lines: m.streamHint(streamConsensus)}
	}
	lines := []string{
		kv(th, "height", th.Val.Render(strconv.FormatInt(c.Height, 10))) + "   " + kv(th, "round", th.Val.Render(strconv.Itoa(int(c.Round)))) +
			"   " + kv(th, "step", th.Accent2.Render(c.Step)) + "   " + kv(th, "from", c.Source),
		kv(th, "proposer", th.Title.Render(cmpOr(c.ProposerNode, "external"))+" "+th.Dim.Render(shortHash(c.ProposerAddress))),
		"",
	}
	barW := min(max(w-34, 6), 40)
	for _, t := range []struct {
		name string
		v    voteTally
	}{{"prevotes", c.Prevotes}, {"precommits", c.Precommits}, {"last commit", c.LastCommit}} {
		lines = append(lines, fmt.Sprintf("%s %s %s  %s", th.Key.Render(padRight(t.name, 11)), bar(th, t.v.Fraction, barW, 2.0/3),
			padLeft(fmt.Sprintf("%.1f%%", t.v.Fraction*100), 6), th.Dim.Render(t.v.Bits)))
	}
	lines = append(lines, "", th.Title.Render(fmt.Sprintf("%-12s %7s  %-16s %-16s %-16s", "VALIDATOR", "POWER", "PREVOTE", "PRECOMMIT", "LAST COMMIT")))
	for _, v := range c.Validators {
		pct := 0.0
		if c.Prevotes.Total > 0 {
			pct = float64(v.VotingPower) / float64(c.Prevotes.Total) * 100
		}
		name := cmpOr(v.Node, shortHash(v.Address))
		if v.Address == c.ProposerAddress {
			name += "★"
		}
		lines = append(lines, fmt.Sprintf("%-12s %6.1f%%  %s %s %s", padRight(name, 12), pct,
			voteCell(th, v.Prevote), voteCell(th, v.Precommit), voteCell(th, v.LastCommit)))
	}
	lines = append(lines, "", th.Title.Render("PEERS"))
	for _, n := range c.Nodes {
		state := th.Good.Render("● up  ")
		if !n.Up {
			state = th.Bad.Render("○ down")
		}
		peers := strings.Join(n.Peers, " ")
		if n.Error != "" {
			peers = th.Dim.Render(n.Error)
		} else if peers == "" {
			peers = th.Warn.Render("no peers")
		}
		lines = append(lines, fmt.Sprintf("%-12s %s  %s", padRight(n.Name, 12), state, peers))
	}
	return mainView{title: "Consensus", right: th.Dim.Render("h" + strconv.FormatInt(c.Height, 10)), lines: lines}
}

func voteCell(th Theme, v vote) string {
	var s string
	switch v.Kind {
	case "block":
		s = th.Good.Render("✓ " + shortHash(v.Block))
	case "nil":
		s = th.Warn.Render("∅ nil")
	default:
		s = th.Dim.Render("· " + cmpOr(v.Kind, "missing"))
	}
	return padRight(s, 16)
}

// Proposals

func (m *Model) sortedProposals() []proposal {
	out := slices.Clone(m.proposals)
	slices.SortFunc(out, func(a, b proposal) int { return int(b.ID) - int(a.ID) })
	return out
}

func (m *Model) proposalStatus(p proposal) (string, lipgloss.Style, string) {
	s := strings.TrimPrefix(p.Status, "PROPOSAL_STATUS_")
	switch s {
	case "VOTING_PERIOD":
		left := "voting"
		if p.VotingEndTime != nil {
			left = fmtDur(time.Until(*p.VotingEndTime).Round(time.Second)) + " left"
		}
		return "◷", m.th.Warn, left
	case "PASSED":
		return "✓", m.th.Good, "passed"
	case "REJECTED":
		return "✗", m.th.Bad, "rejected"
	case "FAILED":
		return "✗", m.th.Bad, "failed"
	case "DEPOSIT_PERIOD":
		return "…", m.th.Dim, "deposit"
	}
	return "·", m.th.Dim, strings.ToLower(s)
}

func proposalRows(m *Model) []listRow {
	var rows []listRow
	for _, p := range m.sortedProposals() {
		g, st, label := m.proposalStatus(p)
		rows = append(rows, listRow{glyph: g, style: st, text: fmt.Sprintf("#%d %s", p.ID, p.Title), aside: label})
	}
	return rows
}

func proposalMain(m *Model, w, _ int) mainView {
	p, ok := m.selectedProposal()
	th := m.th
	if !ok {
		return mainView{title: "Proposal", lines: m.emptyHint(loadProposals, "no proposals yet")}
	}
	if m.proposal != nil && m.proposal.ID == p.ID {
		p = *m.proposal
	}
	live := p.FinalTally
	if m.proposal != nil && m.proposal.ID == p.ID && m.proposal.Tally != nil {
		live = *m.proposal.Tally
	}
	g, st, label := m.proposalStatus(p)
	lines := []string{
		th.Title.Render(fmt.Sprintf("#%d  %s", p.ID, p.Title)) + "   " + st.Render(g+" "+strings.TrimPrefix(p.Status, "PROPOSAL_STATUS_")),
	}
	if p.Summary != "" && p.Summary != p.Title {
		lines = append(lines, th.Text.Render(p.Summary))
	}
	lines = append(lines, "")
	if p.SubmitTime != nil {
		lines = append(lines, kv(th, "submitted  ", p.SubmitTime.Local().Format(time.RFC3339)))
	}
	if p.VotingEndTime != nil {
		lines = append(lines, kv(th, "voting end ", p.VotingEndTime.Local().Format(time.RFC3339)+"  "+st.Render(label)))
	}
	if p.Expedited {
		lines = append(lines, kv(th, "expedited  ", th.Accent2.Render("yes")))
	}
	lines = append(lines, kv(th, "deposit    ", coins(p.TotalDeposit)), kv(th, "proposer   ", p.Proposer))
	for _, msg := range p.Messages {
		lines = append(lines, kv(th, "message    ", msg))
	}
	if p.FailedReason != "" {
		lines = append(lines, kv(th, "failed     ", th.Bad.Render(p.FailedReason)))
	}
	if p.DepositEndTime != nil {
		lines = append(lines, "deposit end  "+p.DepositEndTime.Local().Format(time.RFC3339))
	}
	if p.VotingStartTime != nil {
		lines = append(lines, "voting start "+p.VotingStartTime.Local().Format(time.RFC3339))
	}
	if p.Metadata != "" {
		lines = append(lines, "", th.Title.Render("METADATA"), p.Metadata)
	}
	for _, raw := range p.MessagePayloads {
		var pretty bytes.Buffer
		if json.Indent(&pretty, raw, "", "  ") == nil {
			lines = append(lines, "", th.Title.Render("MESSAGE JSON"))
			lines = append(lines, strings.Split(pretty.String(), "\n")...)
		}
	}
	lines = append(lines, "", th.Title.Render("TALLY"))
	yes, no, abs, veto := bigOf(live.Yes), bigOf(live.No), bigOf(live.Abstain), bigOf(live.NoWithVeto)
	total := new(big.Float).Add(new(big.Float).Add(yes, no), new(big.Float).Add(abs, veto))
	barW := min(max(w-40, 6), 36)
	for _, t := range []struct {
		name string
		v    *big.Float
		st   lipgloss.Style
	}{{"yes", yes, th.Good}, {"no", no, th.Bad}, {"abstain", abs, th.Dim}, {"veto", veto, th.Warn}} {
		frac := 0.0
		if total.Sign() > 0 {
			frac, _ = new(big.Float).Quo(t.v, total).Float64()
		}
		lines = append(lines, fmt.Sprintf("%s %s %s  %s", t.st.Render(padRight(t.name, 8)), bar(th, frac, barW, -1),
			padLeft(fmt.Sprintf("%.1f%%", frac*100), 6), th.Dim.Render(compactAmount(t.v.Text('f', 0)))))
	}
	var wrapped []string
	for _, line := range lines {
		for _, part := range strings.Split(line, "\n") {
			wrapped = append(wrapped, strings.Split(ansi.Wrap(part, max(w, 1), ""), "\n")...)
		}
	}
	lines = append(strings.Split(ansi.Wrap(th.Dim.Render("Enter: scroll details · C: clone · s: export · E: draft"), max(w, 1), ""), "\n"), "")
	lines = append(lines, wrapped...)
	return mainView{title: fmt.Sprintf("Proposal #%d", p.ID), right: st.Render(label), lines: lines}
}

// Upgrades

func upgradeRows(m *Model) []listRow {
	var rows []listRow
	for _, u := range m.upgradeNodes() {
		g, st := "●", m.th.Good
		aside := u.State
		switch {
		case u.Phase == "swap_failed" || (u.Phase == "swapped" && u.State != "running"):
			g, st, aside = "✗", m.th.Bad, "swap failed"
		case u.Phase != "":
			g, st, aside = "◆", m.th.Warn, u.Phase
		case u.State != "running":
			g, st = "○", m.th.Bad
		}
		rows = append(rows, listRow{glyph: g, style: st, text: u.Name + "  " + m.th.Dim.Render(u.Version), aside: aside})
	}
	return rows
}

func (m *Model) plan() *chainPlan {
	if m.upgrade != nil && m.upgrade.Plan != nil {
		return m.upgrade.Plan
	}
	if m.status != nil {
		return m.status.UpgradePlan
	}
	return nil
}

func upgradeSummary(m *Model) string {
	p := m.plan()
	switch {
	case m.upgradeNeedsRecovery():
		return "attention " + m.upgrade.Pending.Name
	case p != nil:
		return fmt.Sprintf("%s@%d", p.Name, p.Height)
	case m.upgrade != nil && m.upgrade.Pending != nil:
		return fmt.Sprintf("%s@%d", m.upgrade.Pending.Name, m.upgrade.Pending.Height)
	case m.completedUpgrade() != nil:
		return "done " + m.completedUpgrade().Name
	}
	return "none"
}

// completedUpgrade is the last completed upgrade while nothing is pending.
func (m *Model) completedUpgrade() *pendingUpgrade {
	if m.upgrade == nil || m.upgrade.Pending != nil {
		return nil
	}
	return m.upgrade.Completed
}

// blocksLeft describes how far the chain is from height h.
func (m *Model) blocksLeft(h int64) string {
	if m.status == nil || m.status.Height == 0 {
		return ""
	}
	left := h - m.status.Height
	if left <= 0 {
		return "reached"
	}
	s := fmt.Sprintf("in %d blocks", left)
	if d, err := time.ParseDuration(m.status.AvgBlockTime); err == nil {
		s += " ~" + fmtDur((d * time.Duration(left)).Round(time.Second))
	}
	return s
}

func upgradeMain(m *Model, width, _ int) mainView {
	th := m.th
	u := m.upgrade
	if u == nil {
		return mainView{title: "Upgrades", lines: m.emptyHint(loadUpgrade, "no lab running")}
	}
	var lines []string
	if m.upgradeNeedsRecovery() {
		lines = append(lines, th.Warn.Render(m.recoveryHint()), "")
	}
	if p := m.plan(); p != nil {
		lines = append(lines, th.Title.Render("PLAN")+"  "+th.Val.Render(p.Name)+"  "+kv(th, "at", strconv.FormatInt(p.Height, 10))+"  "+th.Warn.Render(m.blocksLeft(p.Height)))
		if p.Info != "" {
			lines = append(lines, kv(th, "info", p.Info))
		}
	} else if u.Pending != nil {
		lines = append(lines, th.Title.Render("PENDING")+fmt.Sprintf("  %s at %d", u.Pending.Name, u.Pending.Height))
	} else if m.completedUpgrade() == nil {
		lines = append(lines, th.Dim.Render("no upgrade plan on chain"))
	}
	if p := u.Pending; p != nil {
		swap := th.Good.Render("auto swap")
		if !p.AutoSwap {
			swap = th.Warn.Render("manual swap")
		}
		line := th.Title.Render("SWAP") + "  " + kv(th, "to", th.Val.Render(p.Version)) + "  " + swap
		if p.ProposalID != 0 {
			line += "  " + kv(th, "proposal", fmt.Sprintf("#%d", p.ProposalID))
		}
		lines = append(lines, line, "  "+th.Dim.Render(p.Binary))
		for _, previous := range p.Previous {
			lines = append(lines, kv(th, fmt.Sprintf("previous node%d", previous.Index), previous.Version))
		}
		if p.Recovery != nil {
			lines = append(lines, kv(th, "recovery", p.Recovery.Mode))
		}
	} else if c := m.completedUpgrade(); c != nil {
		lines = append(lines, th.Title.Render("LAST")+"  "+th.Val.Render(c.Name)+"  "+kv(th, "to", c.Version)+"  "+th.Good.Render(fmt.Sprintf("completed at %d", c.Height)))
	}
	lines = append(lines, "", th.Title.Render(fmt.Sprintf("%-10s %-12s %-9s %-12s %s", "NODE", "VERSION", "STATE", "UPGRADE", "HALT")))
	for _, n := range u.Nodes {
		haltAt := "-"
		if n.Halt != nil {
			haltAt = fmt.Sprintf("%s@%d", n.Halt.Name, n.Halt.Height)
		}
		phase := cmpOr(n.Phase, "-")
		st := th.Text
		switch n.Phase {
		case "swap_failed":
			st = th.Bad
		case "halted", "swapping":
			st = th.Warn
		case "swapped":
			st = th.Good
			if n.State != "running" {
				phase, st = "failed", th.Bad
			}
		}
		lines = append(lines, fmt.Sprintf("%-10s %-12s %-9s %s %s", n.Name, n.Version, n.State, st.Render(padRight(phase, 12)), haltAt))
		if n.SwapError != "" {
			lines = append(lines, "  "+th.Bad.Render(n.SwapError))
		}
	}
	for _, w := range u.Warnings {
		lines = append(lines, th.Warn.Render("! "+w))
	}
	if m.plan() == nil && u.Pending == nil {
		lines = append(lines, "", th.Dim.Render("press u to schedule one"))
	}
	lines = strings.Split(ansi.Wrap(strings.Join(lines, "\n"), max(width, 1), ""), "\n")
	return mainView{title: "Upgrades", right: th.Dim.Render(upgradeSummary(m)), lines: lines}
}

// Accounts

func accountRows(m *Model) []listRow {
	rows := make([]listRow, 0, len(m.accounts))
	for _, a := range m.accounts {
		aside := ""
		if len(a.Balances) > 0 {
			aside = compactAmount(a.Balances[0].Amount) + " " + a.Balances[0].Denom
		}
		rows = append(rows, listRow{glyph: "◇", style: m.th.Accent2, text: a.Name, aside: aside})
	}
	return rows
}

func accountMain(m *Model, _, _ int) mainView {
	th := m.th
	if len(m.accounts) == 0 {
		return mainView{title: "Accounts", lines: m.emptyHint(loadAccounts, "no lab running")}
	}
	lines := []string{th.Title.Render(fmt.Sprintf("  %-8s %-46s %s", "NAME", "ADDRESS", "BALANCES"))}
	for i, a := range m.accounts {
		var bal []string
		for _, c := range a.Balances {
			bal = append(bal, groupDigits(c.Amount)+" "+c.Denom)
		}
		line := fmt.Sprintf("%-8s %-46s %s", a.Name, a.Address, strings.Join(bal, ", "))
		if i == m.cursor[panelAccounts] {
			lines = append(lines, th.SelDim.Render("▌ "+line))
		} else {
			lines = append(lines, "  "+th.Text.Render(line))
		}
	}
	return mainView{title: "Accounts", right: th.Dim.Render(fmt.Sprintf("%d accounts", len(m.accounts))), lines: lines}
}

// Labs

func labRows(m *Model) []listRow {
	rows := make([]listRow, 0, len(m.labs))
	for _, l := range m.labs {
		g, st := "○", m.th.Dim
		if l.Running {
			g, st = "●", m.th.Good
		}
		if l.Error != "" {
			g, st = "✗", m.th.Bad
		}
		rows = append(rows, listRow{glyph: g, style: st, text: l.Name, aside: fmt.Sprintf("%s %dv", l.Mode, l.Validators)})
	}
	return rows
}

func labMain(m *Model, _, _ int) mainView {
	l, ok := m.selectedLab()
	th := m.th
	if !ok {
		return mainView{title: "Lab", lines: m.emptyHint(loadLabs, "No lab yet. Press n to create one")}
	}
	state := th.Dim.Render("○ stopped")
	if l.Running {
		state = th.Good.Render("● running")
	}
	lines := []string{th.Title.Render(l.Name) + "  " + state}
	if l.Error != "" {
		lines = append(lines, th.Bad.Render(l.Error))
	}
	lines = append(lines,
		kv(th, "mode      ", l.Mode),
		kv(th, "chain id  ", l.ChainID),
		kv(th, "profile   ", l.Profile.Name),
		kv(th, "version   ", l.Version),
		kv(th, "validators", strconv.Itoa(l.Validators)),
	)
	if l.CreatedAt != nil {
		lines = append(lines, kv(th, "created   ", l.CreatedAt.Local().Format("2006-01-02 15:04")))
	}
	lines = append(lines, kv(th, "dir       ", l.Dir), "",
		th.Title.Render(fmt.Sprintf("%-8s %-10s %-8s %-7s %-7s %-7s %s", "NODE", "VERSION", "VALIDATOR", "RPC", "P2P", "API", "GRPC")))
	for _, n := range l.Nodes {
		ports := map[string]int{}
		for _, p := range n.Ports {
			ports[p.Key] = p.Port
		}
		lines = append(lines, fmt.Sprintf("%-8s %-10s %-9s %-7d %-7d %-7d %d", n.Name, n.Version, n.Validator,
			ports["rpc.laddr"], ports["p2p.laddr"], ports["api.address"], ports["grpc.address"]))
	}
	if len(l.Accounts) > 0 {
		lines = append(lines, "", th.Title.Render("ACCOUNTS"))
		for _, a := range l.Accounts {
			lines = append(lines, fmt.Sprintf("%-8s %s", a.Name, th.Dim.Render(a.Address)))
		}
	}
	return mainView{title: "Lab " + l.Name, right: state, lines: lines}
}

// Profiles

func profileRows(m *Model) []listRow {
	rows := make([]listRow, 0, len(m.profiles))
	for _, p := range m.profiles {
		g, st := "▪", m.th.Accent2
		if p.Origin == "builtin" {
			g, st = "▫", m.th.Dim
		}
		rows = append(rows, listRow{glyph: g, style: st, text: p.Name, aside: p.Origin})
	}
	return rows
}

func profileMain(m *Model, _, _ int) mainView {
	p, ok := m.selectedProfile()
	th := m.th
	if !ok {
		return mainView{title: "Profile", lines: m.emptyHint(loadProfiles, "No profiles. Press n to create one")}
	}
	lines := []string{th.Title.Render(p.Name) + "  " + th.Dim.Render(p.Origin)}
	if p.Path != "" {
		lines = append(lines, kv(th, "path", p.Path))
	}
	lines = append(lines, "")
	if m.profile != nil && m.profile.Name == p.Name {
		lines = append(lines, renderTree(th, m.profile.Profile)...)
	} else {
		lines = append(lines, th.Dim.Render("loading…"))
	}
	return mainView{title: "Profile " + p.Name, right: th.Dim.Render(p.Origin), lines: lines}
}

// renderTree renders a JSON document as indented YAML-like lines in the
// document's own key order.
func renderTree(th Theme, raw json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var lines []string
	var walk func(prefix string, depth int) bool
	walk = func(prefix string, depth int) bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		ind := strings.Repeat("  ", depth)
		switch t := tok.(type) {
		case json.Delim:
			if prefix != "" {
				lines = append(lines, ind+th.Key.Render(prefix))
			}
			for dec.More() {
				key := "-"
				if t == '{' {
					k, err := dec.Token()
					if err != nil {
						return false
					}
					key = fmt.Sprint(k) + ":"
				}
				d := depth + 1
				if prefix == "" && depth == 0 {
					d = 0
				}
				if !walk(key, d) {
					return false
				}
			}
			_, err := dec.Token()
			return err == nil
		default:
			v := fmt.Sprint(t)
			if t == nil {
				v = "null"
			}
			lines = append(lines, ind+th.Key.Render(prefix)+" "+th.Text.Render(v))
		}
		return true
	}
	walk("", 0)
	return lines
}

// Binaries

func binaryRows(m *Model) []listRow {
	rows := make([]listRow, 0, len(m.binaries))
	for _, b := range m.binaries {
		g, st := "■", m.th.Good
		switch b.VersionCheck {
		case "mismatch":
			g, st = "■", m.th.Bad
		case "unknown", "skipped":
			g, st = "□", m.th.Warn
		}
		rows = append(rows, listRow{glyph: g, style: st, text: b.Profile + " " + b.Version, aside: b.Kind})
	}
	return rows
}

func binaryMain(m *Model, _, _ int) mainView {
	b, ok := pick(m.binaries, m.cursor[panelBinaries])
	th := m.th
	if !ok {
		return mainView{title: "Binary", lines: m.emptyHint(loadBinaries, "No binaries cached yet. Press f to fetch one")}
	}
	lines := []string{
		th.Title.Render(b.Profile+" "+b.Version) + "  " + th.Dim.Render(b.Kind),
		"",
		kv(th, "source  ", b.Source),
		kv(th, "path    ", b.Path),
		kv(th, "reports ", cmpOr(b.ReportedVersion, "-")),
		kv(th, "check   ", b.VersionCheck),
		kv(th, "size    ", fmtBytes(b.Size)),
	}
	if b.MtimeUnixNano > 0 {
		lines = append(lines, kv(th, "modified", time.Unix(0, b.MtimeUnixNano).Local().Format("2006-01-02 15:04")))
	}
	return mainView{title: "Binary " + b.Version, right: th.Dim.Render(b.Profile), lines: lines}
}

// emptyHint explains an empty panel. While no lab runs, a panel about the
// chain says what to do next; an error the lab's state does not explain is
// in the status line, so the panel only points there.
func (m *Model) emptyHint(k loadKind, empty string) []string {
	chain := k != loadLabs && k != loadProfiles && k != loadProfile && k != loadBinaries
	if chain && m.labPhase() != phaseRunning {
		return []string{m.th.Text.Render(m.labHint())}
	}
	if err := m.loads[k].err; err != nil {
		if m.explained(k, err) {
			return []string{m.th.Dim.Render(m.labHint())}
		}
		return []string{m.th.Bad.Render("Could not load this; the error is in the status line. ctrl+r retries.")}
	}
	if chain && k != loadNodes && !m.chainUp() {
		return []string{m.th.Dim.Render("waiting for the chain…")}
	}
	if m.loads[k].seq == 0 || m.loads[k].inflight && m.loads[k].seq == 1 {
		return []string{m.th.Dim.Render("loading…")}
	}
	return []string{m.th.Dim.Render(empty)}
}

func (m *Model) streamHint(k streamKind) []string {
	if m.labPhase() != phaseRunning {
		return []string{m.th.Text.Render(m.labHint())}
	}
	if err := m.streams[k].err; err != nil {
		if m.chainSilent() {
			return []string{m.th.Warn.Render("The chain is not answering; the error is in the status line.")}
		}
		return []string{m.th.Dim.Render("Starting the chain…")}
	}
	return []string{m.th.Dim.Render("waiting for the chain…")}
}

// formatting helpers

func kv(th Theme, k, v string) string { return th.Key.Render(k) + " " + v }

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:8] + "…" + h[len(h)-4:]
	}
	return h
}

// bar draws a meter; mark, when in (0,1), draws the threshold tick.
func bar(th Theme, frac float64, w int, mark float64) string {
	frac = min(max(frac, 0), 1)
	full := int(frac*float64(w) + 0.5)
	var b strings.Builder
	for i := range w {
		switch {
		case i < full:
			b.WriteString(th.BorderActive.Render("█"))
		case mark > 0 && i == int(mark*float64(w)):
			b.WriteString(th.Warn.Render("┃"))
		default:
			b.WriteString(th.Border.Render("░"))
		}
	}
	return b.String()
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func bigOf(s string) *big.Float {
	f, ok := new(big.Float).SetString(s)
	if !ok {
		return new(big.Float)
	}
	return f
}

// compactAmount shortens a base-unit integer: 10000000000000000000000
// becomes 1.00e22, small amounts get digit groups.
func compactAmount(s string) string {
	if len(s) <= 9 {
		return groupDigits(s)
	}
	return fmt.Sprintf("%s.%se%d", s[:1], s[1:3], len(s)-1)
}

func groupDigits(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func coins(cs []coin) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = groupDigits(c.Amount) + c.Denom
	}
	return cmpOr(strings.Join(parts, ", "), "-")
}
