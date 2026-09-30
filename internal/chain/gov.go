package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProposalFile is the JSON `tx gov submit-proposal` reads.
type ProposalFile struct {
	Messages []any `json:"messages"`
	// Metadata must be set when Messages is empty: gov rejects a proposal
	// with neither.
	Metadata  string `json:"metadata"`
	Deposit   string `json:"deposit"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Expedited bool   `json:"expedited,omitempty"`
}

// SoftwareUpgradeMsg is MsgSoftwareUpgrade for plan, signed by authority,
// the gov module account.
func SoftwareUpgradeMsg(authority string, plan Plan) map[string]any {
	return map[string]any{
		"@type":     "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
		"authority": authority,
		"plan":      map[string]any{"name": plan.Name, "height": strconv.FormatInt(plan.Height, 10), "info": plan.Info},
	}
}

// UpdateParamsMsgType is the MsgUpdateParams type URL of an SDK module. gov
// moved to v1; the others are still v1beta1.
func UpdateParamsMsgType(module string) string {
	if module == "gov" {
		return "/cosmos.gov.v1.MsgUpdateParams"
	}
	return "/cosmos." + module + ".v1beta1.MsgUpdateParams"
}

// UpdateParamsMsg is MsgUpdateParams of typeURL carrying the full params
// object, which the chain replaces wholesale.
func UpdateParamsMsg(typeURL, authority string, params map[string]any) map[string]any {
	return map[string]any{"@type": typeURL, "authority": authority, "params": protoDurations(params)}
}

var goDuration = regexp.MustCompile(`^([0-9.]+h)?([0-9.]+m)?([0-9.]+s)?$`)

// protoDurations rewrites Go durations such as 504h0m0s, which `q <module>
// params` prints, into the seconds form 1814400s that the proposal decoder
// requires.
func protoDurations(v map[string]any) map[string]any {
	out := make(map[string]any, len(v))
	for k, x := range v {
		switch x := x.(type) {
		case string:
			if d, err := time.ParseDuration(x); err == nil && x != "" && goDuration.MatchString(x) {
				out[k] = strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
				continue
			}
			out[k] = x
		case map[string]any:
			out[k] = protoDurations(x)
		default:
			out[k] = x
		}
	}
	return out
}

// SetParam sets key in params to value, read as JSON when it parses and
// as a string otherwise. The key must already exist, so a typo fails here
// instead of in the proposal.
func SetParam(params map[string]any, assignment string) error {
	key, raw, ok := strings.Cut(assignment, "=")
	if !ok || key == "" {
		return fmt.Errorf("--set %q: want key=value", assignment)
	}
	if _, exists := params[key]; !exists {
		return fmt.Errorf("--set %q: params have no key %s", assignment, key)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		value = raw
	}
	params[key] = value
	return nil
}

// SubmitProposal writes p to a temporary file and submits it from the key
// named from. It returns the broadcast hash.
func (c CLI) SubmitProposal(ctx context.Context, from string, p ProposalFile) (string, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "forklab-proposal-*.json")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return c.Broadcast(ctx, from, "gov", "submit-proposal", f.Name())
}

// ProposalID reads the id gov assigned from a committed submit-proposal tx.
func ProposalID(r TxResult) (uint64, error) {
	v, ok := r.Attr("submit_proposal", "proposal_id")
	if !ok {
		return 0, fmt.Errorf("tx %s has no submit_proposal event", r.Hash)
	}
	return strconv.ParseUint(v, 10, 64)
}

// CheckUpgradeHeight fails when the chain, at blockTime per block from
// height current, likely reaches the plan height before a proposal
// submitted now ends its voting period. The proposal would then fail with
// "upgrade cannot be scheduled in the past".
func CheckUpgradeHeight(height, current int64, blockTime, votingPeriod time.Duration) error {
	eta := time.Duration(height-current) * blockTime
	if eta >= votingPeriod {
		return nil
	}
	need := current + int64((votingPeriod+blockTime-1)/blockTime)
	return fmt.Errorf("height %d is %d blocks away, about %s at %s per block, but voting takes %s; use a height of at least %d",
		height, height-current, eta.Round(time.Second), blockTime.Round(time.Millisecond), votingPeriod, need)
}

// VoteOptions maps the vote words forklab takes to the chain CLI's.
var VoteOptions = map[string]string{"yes": "yes", "no": "no", "abstain": "abstain", "veto": "no_with_veto", "no_with_veto": "no_with_veto"}
