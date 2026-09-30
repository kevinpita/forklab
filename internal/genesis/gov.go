package genesis

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"
)

// GovParams are the lab governance settings: short periods, a quorum any
// single delegation meets, and a deposit the gov account can always pay.
type GovParams struct {
	VotingPeriod          time.Duration
	ExpeditedVotingPeriod time.Duration
	// MinDeposit is the minimum proposal deposit; the expedited minimum is
	// twice it, as the SDK requires it to be larger.
	MinDeposit Coin
}

// govQuorum is the smallest quorum an SDK LegacyDec can express in practice.
const govQuorum = "0.000001000000000000"

// GovPatch rewrites the gov parameters of a fresh or exported genesis. It
// handles the v0.47+ params object, with or without the expedited fields,
// and the older voting_params / deposit_params / tally_params layout.
func GovPatch(g *Genesis, p GovParams) error {
	if p.VotingPeriod <= 0 {
		return errors.New("gov: voting period must be positive")
	}
	deposit, err := sortedCoins([]Coin{p.MinDeposit})
	if err != nil {
		return fmt.Errorf("gov: min deposit: %w", err)
	}
	expedited := []coinJSON{{Denom: p.MinDeposit.Denom, Amount: new(big.Int).Mul(p.MinDeposit.Amount, big.NewInt(2)).String()}}
	gov, err := g.module("gov")
	if err != nil {
		return err
	}
	if raw, ok := gov["params"]; ok {
		gov["params"], err = edit(raw, func(o obj) error {
			errs := []error{
				o.set("voting_period", durationJSON(p.VotingPeriod)),
				o.set("max_deposit_period", durationJSON(p.VotingPeriod)),
				o.set("quorum", govQuorum),
				o.set("min_deposit", deposit),
			}
			if _, ok := o["expedited_voting_period"]; ok {
				if p.ExpeditedVotingPeriod <= 0 || p.ExpeditedVotingPeriod >= p.VotingPeriod {
					return errors.New("expedited voting period must be positive and shorter than the voting period")
				}
				errs = append(errs, o.set("expedited_voting_period", durationJSON(p.ExpeditedVotingPeriod)))
			}
			if _, ok := o["expedited_min_deposit"]; ok {
				errs = append(errs, o.set("expedited_min_deposit", expedited))
			}
			return errors.Join(errs...)
		})
		if err != nil {
			return fmt.Errorf("gov params: %w", err)
		}
		return g.setModule("gov", gov)
	}
	legacy := []struct {
		key    string
		fields map[string]any
	}{
		{"voting_params", map[string]any{"voting_period": durationJSON(p.VotingPeriod)}},
		{"deposit_params", map[string]any{"min_deposit": deposit, "max_deposit_period": durationJSON(p.VotingPeriod)}},
		{"tally_params", map[string]any{"quorum": govQuorum}},
	}
	for _, section := range legacy {
		raw, ok := gov[section.key]
		if !ok {
			return fmt.Errorf("gov: no params object and no %s (unknown gov genesis layout)", section.key)
		}
		gov[section.key], err = edit(raw, func(o obj) error {
			var errs []error
			for k, v := range section.fields {
				errs = append(errs, o.set(k, v))
			}
			return errors.Join(errs...)
		})
		if err != nil {
			return fmt.Errorf("gov %s: %w", section.key, err)
		}
	}
	return g.setModule("gov", gov)
}

// durationJSON renders a duration the way protobuf JSON does: seconds with
// an "s" suffix.
func durationJSON(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}
