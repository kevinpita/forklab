package chain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Step is the CometBFT round step.
type Step uint8

const (
	StepNewHeight     Step = 1
	StepNewRound      Step = 2
	StepPropose       Step = 3
	StepPrevote       Step = 4
	StepPrevoteWait   Step = 5
	StepPrecommit     Step = 6
	StepPrecommitWait Step = 7
	StepCommit        Step = 8
)

var stepNames = map[Step]string{
	StepNewHeight:     "NewHeight",
	StepNewRound:      "NewRound",
	StepPropose:       "Propose",
	StepPrevote:       "Prevote",
	StepPrevoteWait:   "PrevoteWait",
	StepPrecommit:     "Precommit",
	StepPrecommitWait: "PrecommitWait",
	StepCommit:        "Commit",
}

func (s Step) String() string {
	if n, ok := stepNames[s]; ok {
		return n
	}
	return "Unknown(" + strconv.Itoa(int(s)) + ")"
}

// VoteType is the signed message type of a vote.
type VoteType uint8

const (
	VoteTypeUnknown VoteType = iota
	Prevote
	Precommit
)

func (t VoteType) String() string {
	switch t {
	case Prevote:
		return "Prevote"
	case Precommit:
		return "Precommit"
	}
	return "Unknown"
}

// VoteKind says what a validator's slot in a vote set holds.
type VoteKind uint8

const (
	// VoteMissing means no vote was received ("nil-Vote" in the RPC).
	VoteMissing VoteKind = iota
	// VoteNil is a signed vote for no block.
	VoteNil
	// VoteBlock is a signed vote for the block in BlockHashPrefix.
	VoteBlock
	// VoteUnknown is a vote string this parser could not read. Raw holds it.
	VoteUnknown
)

func (k VoteKind) String() string {
	switch k {
	case VoteMissing:
		return "missing"
	case VoteNil:
		return "nil"
	case VoteBlock:
		return "block"
	}
	return "unknown"
}

// Vote is one validator's slot in a vote set. Only a VoteNil or VoteBlock
// vote carries Height, Round, Type, and ValidatorAddressPrefix.
type Vote struct {
	Kind                   VoteKind
	ValidatorIndex         int
	ValidatorAddressPrefix string
	Height                 int64
	Round                  int32
	Type                   VoteType
	BlockHashPrefix        string
	Raw                    string
}

// VoteSet is the votes of one type for one round, indexed like the validator
// set, with the tally CometBFT reports for it.
type VoteSet struct {
	Votes []Vote
	// Bits is the received-vote bitmap, "x" for received and "_" for missing.
	Bits string
	// Power is the voting power received out of TotalPower. Both are 0 when
	// the tally string could not be read.
	Power      int64
	TotalPower int64
}

// Fraction is Power over TotalPower, the "0.90" in "270/300 = 0.90".
func (v VoteSet) Fraction() float64 {
	if v.TotalPower == 0 {
		return 0
	}
	return float64(v.Power) / float64(v.TotalPower)
}

// ConsensusState is a snapshot of the node's consensus round.
type ConsensusState struct {
	Height int64
	Round  int32
	Step   Step
	// Validators is the set for Height; vote slots use its indexes.
	Validators      []Validator
	ProposerAddress string
	Prevotes        VoteSet
	Precommits      VoteSet
	// LastCommit holds the precommits that committed Height-1. Polls often land
	// in Propose, where the current round has no votes yet.
	LastCommit VoteSet
}

// ConsensusState reads /dump_consensus_state, the one endpoint that carries
// the validator set, current-round votes, and the last commit together.
func (c *Client) ConsensusState(ctx context.Context) (ConsensusState, error) {
	type rawVoteSet struct {
		Votes    []string `json:"votes"`
		BitArray string   `json:"votes_bit_array"`
	}
	var r struct {
		RoundState struct {
			Height     int64 `json:"height,string"`
			Round      int32 `json:"round"`
			Step       Step  `json:"step"`
			Validators struct {
				Validators []Validator `json:"validators"`
				Proposer   struct {
					Address string `json:"address"`
				} `json:"proposer"`
			} `json:"validators"`
			Votes []struct {
				Round          int32    `json:"round"`
				Prevotes       []string `json:"prevotes"`
				PrevotesBits   string   `json:"prevotes_bit_array"`
				Precommits     []string `json:"precommits"`
				PrecommitsBits string   `json:"precommits_bit_array"`
			} `json:"votes"`
			LastCommit *rawVoteSet `json:"last_commit"`
		} `json:"round_state"`
	}
	if err := c.call(ctx, "dump_consensus_state", nil, &r); err != nil {
		return ConsensusState{}, err
	}
	rs := r.RoundState
	cs := ConsensusState{
		Height:          rs.Height,
		Round:           rs.Round,
		Step:            rs.Step,
		Validators:      rs.Validators.Validators,
		ProposerAddress: rs.Validators.Proposer.Address,
	}
	// The vote list also holds the next round, so select by round number.
	for _, v := range rs.Votes {
		if v.Round == rs.Round {
			cs.Prevotes = parseVoteSet(v.Prevotes, v.PrevotesBits)
			cs.Precommits = parseVoteSet(v.Precommits, v.PrecommitsBits)
		}
	}
	if rs.LastCommit != nil {
		cs.LastCommit = parseVoteSet(rs.LastCommit.Votes, rs.LastCommit.BitArray)
	}
	return cs, nil
}

func parseVoteSet(votes []string, bits string) VoteSet {
	vs := VoteSet{Votes: make([]Vote, len(votes))}
	for i, s := range votes {
		vs.Votes[i] = parseVote(i, s)
	}
	vs.Bits, vs.Power, vs.TotalPower = parseBitArray(bits)
	return vs
}

// parseVote reads CometBFT's Vote.String():
//
//	0.38: Vote{0:0A8C9CFF520E 100/00/SIGNED_MSG_TYPE_PRECOMMIT(Precommit) D924621D5270 8E30450CFD83 000000000000 @ 2026-09-29T23:04:40.838787099Z}
//	0.37: Vote{0:0A8C9CFF520E 100/00/SIGNED_MSG_TYPE_PRECOMMIT(Precommit) D924621D5270 8E30450CFD83 @ 2026-09-29T23:04:40.838787099Z}
//
// The block hash is a 6-byte fingerprint, all zeros for a nil vote. index is
// the slot position, used when the string cannot be read.
func parseVote(index int, s string) Vote {
	if s == "nil-Vote" {
		return Vote{Kind: VoteMissing, ValidatorIndex: index}
	}
	unknown := Vote{Kind: VoteUnknown, ValidatorIndex: index, Raw: s}
	body, ok := strings.CutPrefix(s, "Vote{")
	if !ok {
		return unknown
	}
	body, ok = strings.CutSuffix(body, "}")
	if !ok {
		return unknown
	}
	f := strings.Fields(body)
	if len(f) < 3 {
		return unknown
	}
	idx, addr, ok := strings.Cut(f[0], ":")
	if !ok {
		return unknown
	}
	vi, err := strconv.Atoi(idx)
	if err != nil {
		return unknown
	}
	hrt := strings.SplitN(f[1], "/", 3)
	if len(hrt) != 3 {
		return unknown
	}
	h, err := strconv.ParseInt(hrt[0], 10, 64)
	if err != nil {
		return unknown
	}
	round, err := strconv.ParseInt(hrt[1], 10, 32)
	if err != nil {
		return unknown
	}
	var typ VoteType
	switch {
	case strings.HasPrefix(hrt[2], "SIGNED_MSG_TYPE_PREVOTE"):
		typ = Prevote
	case strings.HasPrefix(hrt[2], "SIGNED_MSG_TYPE_PRECOMMIT"):
		typ = Precommit
	default:
		return unknown
	}
	v := Vote{Kind: VoteBlock, ValidatorIndex: vi, ValidatorAddressPrefix: addr, Height: h, Round: int32(round), Type: typ, BlockHashPrefix: f[2]}
	if strings.Trim(f[2], "0") == "" {
		v.Kind, v.BlockHashPrefix = VoteNil, ""
	}
	return v
}

// parseBitArray reads a tally such as "BA{4:xx__} 270/300 = 0.90". A string it
// cannot read yields zero values.
func parseBitArray(s string) (bits string, power, total int64) {
	var n int
	var raw string
	if _, err := fmt.Sscanf(s, "BA{%d:%s %d/%d", &n, &raw, &power, &total); err != nil {
		return "", 0, 0
	}
	bits, ok := strings.CutSuffix(raw, "}")
	if !ok || len(bits) != n {
		return "", 0, 0
	}
	return bits, power, total
}
