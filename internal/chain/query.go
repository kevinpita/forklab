package chain

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Coin is an amount of one denom. Amount stays a decimal string because
// chain amounts overflow int64.
type Coin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

type Coins []Coin

// String is the CLI form, such as 1stake,5uatom.
func (cs Coins) String() string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.Amount + c.Denom
	}
	return strings.Join(parts, ",")
}

func decode[T any](what string, data []byte) (T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("%s: unreadable output: %w", what, err)
	}
	return v, nil
}

// Balances is `q bank balances <addr>`.
func (c CLI) Balances(ctx context.Context, addr string) (Coins, error) {
	out, err := c.query(ctx, "bank", "balances", addr)
	if err != nil {
		return nil, err
	}
	return parseBalances(out)
}

func parseBalances(data []byte) (Coins, error) {
	r, err := decode[struct {
		Balances Coins `json:"balances"`
	}]("bank balances", data)
	if r.Balances == nil {
		r.Balances = Coins{}
	}
	return r.Balances, err
}

// Plan is an x/upgrade plan.
type Plan struct {
	Name   string `json:"name"`
	Height int64  `json:"height,string"`
	Info   string `json:"info,omitempty"`
}

// UpgradePlan is `q upgrade plan`: the scheduled upgrade, or nil.
func (c CLI) UpgradePlan(ctx context.Context) (*Plan, error) {
	out, err := c.query(ctx, "upgrade", "plan")
	var ce *CommandError
	if errors.As(err, &ce) && strings.Contains(ce.Stderr, "no upgrade scheduled") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseUpgradePlan(out)
}

// parseUpgradePlan reads {"plan": {...}} (SDK 0.50) or the bare plan (0.47,
// which fails with "no upgrade scheduled" instead of printing {}).
func parseUpgradePlan(data []byte) (*Plan, error) {
	r, err := decode[struct {
		Wrapped *Plan `json:"plan"`
		Plan
	}]("upgrade plan", data)
	if err == nil && r.Wrapped == nil && r.Name != "" {
		return &r.Plan, nil
	}
	return r.Wrapped, err
}

// ModuleAddress is the address of the module account name, such as gov,
// from `q auth module-account`.
func (c CLI) ModuleAddress(ctx context.Context, name string) (string, error) {
	out, err := c.query(ctx, "auth", "module-account", name)
	if err != nil {
		return "", err
	}
	return parseModuleAccount(out)
}

type addressField struct {
	Address string `json:"address"`
}

// parseModuleAccount reads the account's value.address (SDK 0.50) or
// base_account.address (0.47).
func parseModuleAccount(data []byte) (string, error) {
	r, err := decode[struct {
		Account struct {
			Value       addressField `json:"value"`
			BaseAccount addressField `json:"base_account"`
		} `json:"account"`
	}]("module account", data)
	addr := cmp.Or(r.Account.Value.Address, r.Account.BaseAccount.Address)
	if err == nil && addr == "" {
		err = fmt.Errorf("module account: no address in %s", strings.TrimSpace(string(data)))
	}
	return addr, err
}

// GovParams are the x/gov params forklab needs to submit proposals.
type GovParams struct {
	MinDeposit          Coins `json:"min_deposit"`
	ExpeditedMinDeposit Coins `json:"expedited_min_deposit"`
	// VotingPeriod and ExpeditedVotingPeriod are durations such as 30s.
	VotingPeriod          string `json:"voting_period"`
	ExpeditedVotingPeriod string `json:"expedited_voting_period"`
}

func (c CLI) GovParams(ctx context.Context) (GovParams, error) {
	out, err := c.query(ctx, "gov", "params")
	if err != nil {
		return GovParams{}, err
	}
	return parseGovParams(out)
}

func parseGovParams(data []byte) (GovParams, error) {
	r, err := decode[struct {
		Params GovParams `json:"params"`
	}]("gov params", data)
	return r.Params, err
}

// Params is `q <module> params`, the module's params object as the binary
// prints it.
func (c CLI) Params(ctx context.Context, module string) (map[string]any, error) {
	out, err := c.query(ctx, module, "params")
	if err != nil {
		return nil, err
	}
	return parseParams(module, out)
}

// parseParams reads {"params": {...}} (SDK 0.50) or the bare params (0.47).
func parseParams(module string, data []byte) (map[string]any, error) {
	r, err := decode[map[string]any](module+" params", data)
	if err != nil {
		return nil, err
	}
	if p, ok := r["params"].(map[string]any); ok {
		return p, nil
	}
	if len(r) == 0 {
		return nil, fmt.Errorf("%s params: no params in %s", module, strings.TrimSpace(string(data)))
	}
	return r, nil
}

// Tally counts voting power per option.
type Tally struct {
	Yes        string `json:"yes"`
	No         string `json:"no"`
	Abstain    string `json:"abstain"`
	NoWithVeto string `json:"no_with_veto"`
}

type rawTally struct {
	Yes        string `json:"yes_count"`
	No         string `json:"no_count"`
	Abstain    string `json:"abstain_count"`
	NoWithVeto string `json:"no_with_veto_count"`
}

// Proposal is a gov v1 proposal. Status is the enum name without its
// PROPOSAL_STATUS_ prefix, such as VOTING_PERIOD or PASSED.
type Proposal struct {
	ID       uint64   `json:"id"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Status   string   `json:"status"`
	Messages []string `json:"messages"`
	// FinalTally is set once voting ends; use CLI.Tally while it runs.
	FinalTally    Tally      `json:"final_tally"`
	TotalDeposit  Coins      `json:"total_deposit"`
	SubmitTime    *time.Time `json:"submit_time,omitempty"`
	VotingEndTime *time.Time `json:"voting_end_time,omitempty"`
	Expedited     bool       `json:"expedited,omitempty"`
	FailedReason  string     `json:"failed_reason,omitempty"`
	Proposer      string     `json:"proposer,omitempty"`
}

type rawProposal struct {
	ID       string `json:"id"`
	Messages []struct {
		Type   string `json:"type"`
		AtType string `json:"@type"`
	} `json:"messages"`
	Status        string     `json:"status"`
	FinalTally    rawTally   `json:"final_tally_result"`
	TotalDeposit  Coins      `json:"total_deposit"`
	SubmitTime    *time.Time `json:"submit_time"`
	VotingEndTime *time.Time `json:"voting_end_time"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	Expedited     bool       `json:"expedited"`
	FailedReason  string     `json:"failed_reason"`
	Proposer      string     `json:"proposer"`
}

func (r rawProposal) proposal() (Proposal, error) {
	id, err := strconv.ParseUint(r.ID, 10, 64)
	if err != nil {
		return Proposal{}, fmt.Errorf("proposal id %q: %w", r.ID, err)
	}
	p := Proposal{
		ID: id, Title: r.Title, Summary: r.Summary,
		Status:     strings.TrimPrefix(r.Status, "PROPOSAL_STATUS_"),
		Messages:   []string{},
		FinalTally: Tally(r.FinalTally), TotalDeposit: r.TotalDeposit,
		SubmitTime: r.SubmitTime, VotingEndTime: r.VotingEndTime,
		Expedited: r.Expedited, FailedReason: r.FailedReason, Proposer: r.Proposer,
	}
	for _, m := range r.Messages {
		p.Messages = append(p.Messages, cmp.Or(m.AtType, m.Type))
	}
	return p, nil
}

// Proposals is `q gov proposals`, oldest first.
func (c CLI) Proposals(ctx context.Context) ([]Proposal, error) {
	out, err := c.query(ctx, "gov", "proposals")
	if err != nil {
		return nil, err
	}
	return parseProposals(out)
}

func parseProposals(data []byte) ([]Proposal, error) {
	r, err := decode[struct {
		Proposals []rawProposal `json:"proposals"`
	}]("gov proposals", data)
	if err != nil {
		return nil, err
	}
	out := []Proposal{}
	for _, raw := range r.Proposals {
		p, err := raw.proposal()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Proposal is `q gov proposal <id>`.
func (c CLI) Proposal(ctx context.Context, id uint64) (Proposal, error) {
	out, err := c.query(ctx, "gov", "proposal", strconv.FormatUint(id, 10))
	if err != nil {
		return Proposal{}, err
	}
	return parseProposal(out)
}

// parseProposal reads {"proposal": {...}} (SDK 0.50) or the bare proposal
// (0.47).
func parseProposal(data []byte) (Proposal, error) {
	r, err := decode[struct {
		Proposal *rawProposal `json:"proposal"`
		rawProposal
	}]("gov proposal", data)
	if err != nil {
		return Proposal{}, err
	}
	if r.Proposal != nil {
		return r.Proposal.proposal()
	}
	return r.proposal()
}

// Tally is `q gov tally <id>`, the running count of a proposal in voting.
func (c CLI) Tally(ctx context.Context, id uint64) (Tally, error) {
	out, err := c.query(ctx, "gov", "tally", strconv.FormatUint(id, 10))
	if err != nil {
		return Tally{}, err
	}
	return parseTally(out)
}

// parseTally reads {"tally": {...}} (SDK 0.50) or the bare tally (0.47).
func parseTally(data []byte) (Tally, error) {
	r, err := decode[struct {
		Tally *rawTally `json:"tally"`
		rawTally
	}]("gov tally", data)
	if r.Tally != nil {
		return Tally(*r.Tally), err
	}
	return Tally(r.rawTally), err
}

// StakingValidator is an x/staking validator. Status is the bond status
// without its BOND_STATUS_ prefix: BONDED, UNBONDING, or UNBONDED.
type StakingValidator struct {
	Operator string `json:"operator"`
	Moniker  string `json:"moniker"`
	// ConsensusPubKey is the base64 key, the same value CometBFT reports.
	ConsensusPubKey string `json:"consensus_pubkey"`
	Status          string `json:"status"`
	Jailed          bool   `json:"jailed"`
	Tokens          string `json:"tokens"`
}

// StakingValidators is `q staking validators`, every validator in one page.
func (c CLI) StakingValidators(ctx context.Context) ([]StakingValidator, error) {
	out, err := c.query(ctx, "staking", "validators", c.limitFlag(ctx), "10000")
	if err != nil {
		return nil, err
	}
	return parseStakingValidators(out)
}

// limitFlags caches limitFlag per binary path.
var limitFlags sync.Map

// limitFlag is the page size flag of list queries: --page-limit in SDK 0.50
// autocli, --limit before it. The binary's help is read once.
func (c CLI) limitFlag(ctx context.Context) string {
	if f, ok := limitFlags.Load(c.Bin); ok {
		return f.(string)
	}
	flag := "--limit"
	if help, err := c.run(ctx, "q", "staking", "validators", "--help"); err == nil && strings.Contains(string(help), "--page-limit") {
		flag = "--page-limit"
	}
	limitFlags.Store(c.Bin, flag)
	return flag
}

func parseStakingValidators(data []byte) ([]StakingValidator, error) {
	r, err := decode[struct {
		Validators []struct {
			Operator string `json:"operator_address"`
			// value in SDK 0.50 output, key in the 0.47 Any.
			ConsensusPubKey struct {
				Value string `json:"value"`
				Key   string `json:"key"`
			} `json:"consensus_pubkey"`
			Status      string `json:"status"`
			Jailed      bool   `json:"jailed"`
			Tokens      string `json:"tokens"`
			Description struct {
				Moniker string `json:"moniker"`
			} `json:"description"`
		} `json:"validators"`
	}]("staking validators", data)
	if err != nil {
		return nil, err
	}
	out := []StakingValidator{}
	for _, v := range r.Validators {
		out = append(out, StakingValidator{
			Operator: v.Operator, Moniker: v.Description.Moniker, ConsensusPubKey: cmp.Or(v.ConsensusPubKey.Value, v.ConsensusPubKey.Key),
			Status: strings.TrimPrefix(v.Status, "BOND_STATUS_"), Jailed: v.Jailed, Tokens: v.Tokens,
		})
	}
	return out, nil
}
