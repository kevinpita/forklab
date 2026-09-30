package genesis

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"strings"
)

// NodeKey is the consensus public key of one lab node: the base64 ed25519 key
// from its priv_validator_key.json.
type NodeKey struct {
	PubKey string
}

// GovDelegator is the account that owns the injected delegations: the boost
// of every chosen validator, plus Amount (in the bond denom) delegated to
// node0's validator. It must be one of the injected accounts or an existing
// account.
type GovDelegator struct {
	Address string
	Amount  *big.Int
}

// TakeoverInput drives Takeover.
type TakeoverInput struct {
	// Validators are the lab nodes; node i takes over the i-th largest bonded
	// validator.
	Validators []NodeKey
	// Accounts are injected as by AddAccounts, in the same auth and bank
	// passes that apply the boost.
	Accounts []Account
	// GovDelegator is required.
	GovDelegator *GovDelegator
	// ChainID replaces chain_id when set.
	ChainID string
}

// ChosenValidator describes one taken-over validator.
type ChosenValidator struct {
	Operator string
	// ConsAddr is the new bech32 consensus address (the valcons prefix).
	ConsAddr string
	Tokens   *big.Int
	Power    *big.Int
}

// TakeoverReport summarizes what Takeover did.
type TakeoverReport struct {
	PowerReduction *big.Int
	BondDenom      string
	// Validators are indexed like TakeoverInput.Validators.
	Validators []ChosenValidator
	TotalPower *big.Int
	// BondedTokens is the bonded stake after the boost, the denominator of
	// the gov quorum.
	BondedTokens *big.Int
}

const (
	ed25519PubKeyType   = "/cosmos.crypto.ed25519.PubKey"
	cometEd25519KeyType = "tendermint/PubKeyEd25519"
	statusBonded        = "BOND_STATUS_BONDED"
)

// wire shapes read or written in staking, slashing, distribution, and the
// CometBFT validator set.
type (
	consensusPubKeyJSON struct {
		Type string `json:"@type"`
		Key  string `json:"key"`
	}
	validatorView struct {
		OperatorAddress string              `json:"operator_address"`
		ConsensusPubKey consensusPubKeyJSON `json:"consensus_pubkey"`
		Tokens          string              `json:"tokens"`
		DelegatorShares string              `json:"delegator_shares"`
		Status          string              `json:"status"`
	}
	lastPowerJSON struct {
		Address string `json:"address"`
		Power   string `json:"power"`
	}
	delegationJSON struct {
		DelegatorAddress string `json:"delegator_address"`
		ValidatorAddress string `json:"validator_address"`
		Shares           string `json:"shares"`
	}
	cometPubKeyJSON struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	cometValidatorJSON struct {
		Address string          `json:"address"`
		PubKey  cometPubKeyJSON `json:"pub_key"`
		Power   string          `json:"power"`
		Name    string          `json:"name"`
	}
	signingInfoJSON struct {
		Address             string `json:"address"`
		StartHeight         string `json:"start_height"`
		IndexOffset         string `json:"index_offset"`
		JailedUntil         string `json:"jailed_until"`
		Tombstoned          bool   `json:"tombstoned"`
		MissedBlocksCounter string `json:"missed_blocks_counter"`
	}
	signingInfoEntryJSON struct {
		Address              string          `json:"address"`
		ValidatorSigningInfo signingInfoJSON `json:"validator_signing_info"`
	}
	addressedView struct {
		Address string `json:"address"`
	}
	currentRewardsView struct {
		ValidatorAddress string `json:"validator_address"`
		Rewards          struct {
			Period json.Number `json:"period"`
		} `json:"rewards"`
	}
	historicalRewardsView struct {
		ValidatorAddress string      `json:"validator_address"`
		Period           json.Number `json:"period"`
	}
	startingInfoJSON struct {
		PreviousPeriod string `json:"previous_period"`
		Stake          string `json:"stake"`
		Height         string `json:"height"`
	}
	startingInfoEntryJSON struct {
		DelegatorAddress string           `json:"delegator_address"`
		ValidatorAddress string           `json:"validator_address"`
		StartingInfo     startingInfoJSON `json:"starting_info"`
	}
)

// validator is the typed view of one staking validator plus its raw entry.
type validator struct {
	raw      json.RawMessage
	operator string
	pubType  string
	pubKey   string // base64 as in genesis
	tokens   *big.Int
	shares   *big.Int // LegacyDec scaled by 1e18
	bonded   bool
	// consAddr and boostShares are only set for chosen validators: the new
	// consensus address, and the shares the boost added, which become the gov
	// account's delegation so shares keep summing to delegator_shares.
	consAddr    []byte
	boostShares *big.Int
}

// takeover carries the state shared by the module rewrites.
type takeover struct {
	g          *Genesis
	bondDenom  string
	reduction  *big.Int
	validators []validator
	chosen     []int // indices into validators, node order
	// remap maps an old consensus address to the new one, for chosen validators.
	remap      map[string][]byte
	valconsHRP string
	boost      *big.Int // tokens added to the bonded pool
	gov        string   // the account that owns every injected delegation
	injected   []injectedDelegation
}

// injectedDelegation is one delegation from the gov account, ready for
// staking and distribution.
type injectedDelegation struct {
	operator string
	shares   *big.Int // LegacyDec
	stake    *big.Int // LegacyDec, tokens the shares are worth
}

// Takeover rewrites an exported genesis so the lab nodes control the chain.
// Node i takes over the i-th largest bonded validator; the chosen set is
// boosted to hold at least 90% of the voting power, split evenly, and the
// pool, supply, CometBFT set, slashing, and distribution state follow. The
// boost is delegated from the gov account, so the gov account alone holds
// the controlling stake and no mainnet delegator gains voting power.
// On error the genesis is left unchanged.
func Takeover(g *Genesis, in TakeoverInput) (*TakeoverReport, error) {
	top, appState := maps.Clone(g.top), maps.Clone(g.appState)
	report, err := takeOver(g, in)
	if err != nil {
		g.top, g.appState = top, appState
		return nil, err
	}
	return report, nil
}

func takeOver(g *Genesis, in TakeoverInput) (*TakeoverReport, error) {
	newKeys, err := parseNodeKeys(in.Validators)
	if err != nil {
		return nil, err
	}
	if in.GovDelegator == nil || in.GovDelegator.Address == "" {
		return nil, errors.New("takeover: a gov delegator is required, it owns the boost delegations")
	}
	if in.GovDelegator.Amount == nil || in.GovDelegator.Amount.Sign() <= 0 {
		return nil, errors.New("takeover: gov delegation amount must be positive")
	}
	t := &takeover{g: g, remap: map[string][]byte{}, boost: new(big.Int), gov: in.GovDelegator.Address}
	if err := t.loadStaking(); err != nil {
		return nil, err
	}
	if err := t.choose(len(newKeys)); err != nil {
		return nil, err
	}
	oldSet, err := loadConsensusSet(g)
	if err != nil {
		return nil, err
	}
	if err := t.assignKeys(newKeys, oldSet); err != nil {
		return nil, err
	}
	t.planDelegations(in.GovDelegator.Amount)
	report, err := t.writeStaking()
	if err != nil {
		return nil, err
	}
	if err := t.writeConsensusSet(oldSet); err != nil {
		return nil, err
	}
	if err := t.writeSlashing(); err != nil {
		return nil, err
	}
	if err := t.writeDistribution(); err != nil {
		return nil, err
	}
	if err := t.writeAccounts(in.Accounts); err != nil {
		return nil, err
	}
	if in.ChainID != "" {
		raw, err := marshal(in.ChainID)
		if err != nil {
			return nil, err
		}
		g.top["chain_id"] = raw
	}
	return report, nil
}

func parseNodeKeys(keys []NodeKey) ([]string, error) {
	if len(keys) == 0 {
		return nil, errors.New("takeover: at least one validator key is required")
	}
	out := make([]string, len(keys))
	seen := map[string]bool{}
	for i, k := range keys {
		raw, err := base64.StdEncoding.DecodeString(k.PubKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("takeover: validator %d: not a base64 ed25519 public key", i)
		}
		if seen[k.PubKey] {
			return nil, fmt.Errorf("takeover: validator %d: duplicate public key", i)
		}
		seen[k.PubKey] = true
		out[i] = k.PubKey
	}
	return out, nil
}

func (t *takeover) loadStaking() error {
	st, err := t.g.module("staking")
	if err != nil {
		return err
	}
	var params struct {
		BondDenom string `json:"bond_denom"`
	}
	if err := st.get("params", &params); err != nil {
		return fmt.Errorf("staking: %w", err)
	}
	if params.BondDenom == "" {
		return errors.New("staking: params.bond_denom is empty")
	}
	t.bondDenom = params.BondDenom
	raws, err := st.list("validators")
	if err != nil {
		return fmt.Errorf("staking: %w", err)
	}
	for _, raw := range raws {
		view, err := decodeAs[validatorView](raw)
		if err != nil {
			return fmt.Errorf("staking validator: %w", err)
		}
		tokens, err := parseInt(view.Tokens)
		if err != nil {
			return fmt.Errorf("staking validator %s: tokens: %w", view.OperatorAddress, err)
		}
		shares, err := parseDec(view.DelegatorShares)
		if err != nil {
			return fmt.Errorf("staking validator %s: delegator_shares: %w", view.OperatorAddress, err)
		}
		t.validators = append(t.validators, validator{
			raw:      raw,
			operator: view.OperatorAddress,
			pubType:  view.ConsensusPubKey.Type,
			pubKey:   view.ConsensusPubKey.Key,
			tokens:   tokens,
			shares:   shares,
			bonded:   view.Status == statusBonded,
		})
	}
	var lastPowers []lastPowerJSON
	if err := st.get("last_validator_powers", &lastPowers); err != nil {
		return fmt.Errorf("staking: %w", err)
	}
	t.reduction, err = inferPowerReduction(lastPowers, t.validators)
	return err
}

// inferPowerReduction finds the power of ten that maps every recorded
// validator power to its tokens (power = tokens / reduction). The reduction
// is not a genesis field; 1e6 is the SDK default and EVM chains use 1e18.
func inferPowerReduction(lastPowers []lastPowerJSON, validators []validator) (*big.Int, error) {
	tokensByOperator := map[string]*big.Int{}
	for _, v := range validators {
		tokensByOperator[v.operator] = v.tokens
	}
	type sample struct{ tokens, power *big.Int }
	var samples []sample
	for _, lp := range lastPowers {
		power, err := parseInt(lp.Power)
		if err != nil {
			return nil, fmt.Errorf("staking last_validator_powers %s: %w", lp.Address, err)
		}
		if tokens, ok := tokensByOperator[lp.Address]; ok && power.Sign() > 0 {
			samples = append(samples, sample{tokens, power})
		}
	}
	if len(samples) == 0 {
		return nil, errors.New("staking: no last_validator_powers entry to infer the power reduction from (not an exported genesis?)")
	}
	unit := big.NewInt(1)
	ten := big.NewInt(10)
	for range 40 {
		ok := true
		for _, s := range samples {
			if new(big.Int).Quo(s.tokens, unit).Cmp(s.power) != 0 {
				ok = false
				break
			}
		}
		if ok {
			return unit, nil
		}
		unit = new(big.Int).Mul(unit, ten)
	}
	return nil, errors.New("staking: last_validator_powers do not match tokens for any power-of-ten reduction")
}

// choose picks the n largest bonded validators, ties broken by operator address.
func (t *takeover) choose(n int) error {
	var bonded []int
	for i, v := range t.validators {
		if v.bonded {
			bonded = append(bonded, i)
		}
	}
	if len(bonded) < n {
		return fmt.Errorf("takeover: %d validators requested but genesis has only %d bonded validators", n, len(bonded))
	}
	slices.SortStableFunc(bonded, func(a, b int) int {
		if c := t.validators[b].tokens.Cmp(t.validators[a].tokens); c != 0 {
			return c
		}
		return strings.Compare(t.validators[a].operator, t.validators[b].operator)
	})
	t.chosen = bonded[:n]
	return nil
}

// targetTokens returns the token count every chosen validator ends with: the
// chosen set holds at least 90% of the bonded tokens, split evenly, and no
// chosen validator loses tokens.
func (t *takeover) targetTokens() *big.Int {
	others := new(big.Int)
	largest := new(big.Int)
	chosen := map[int]bool{}
	for _, i := range t.chosen {
		chosen[i] = true
		largest = maxInt(largest, t.validators[i].tokens)
	}
	for i, v := range t.validators {
		if v.bonded && !chosen[i] {
			others.Add(others, v.tokens)
		}
	}
	target := ceilDiv(new(big.Int).Mul(others, big.NewInt(9)), big.NewInt(int64(len(t.chosen))))
	return roundUp(maxInt(target, largest), t.reduction)
}

func maxInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

// assignKeys gives each chosen validator its node's key and boosts its tokens
// at the validator's exchange rate, so existing delegations keep their value.
func (t *takeover) assignKeys(newKeys []string, oldSet consensusSet) error {
	target := t.targetTokens()
	for i, vi := range t.chosen {
		v := &t.validators[vi]
		oldAddr, err := oldSet.address(v)
		if err != nil {
			return err
		}
		newAddr, err := consAddress(newKeys[i])
		if err != nil {
			return fmt.Errorf("takeover: validator %d: %w", i, err)
		}
		t.remap[string(oldAddr)] = newAddr
		boost := new(big.Int).Sub(target, v.tokens)
		t.boost.Add(t.boost, boost)
		v.boostShares = sharesFromTokens(v, boost)
		v.tokens = new(big.Int).Set(target)
		v.shares = new(big.Int).Add(v.shares, v.boostShares)
		v.pubType = ed25519PubKeyType
		v.pubKey = newKeys[i]
		v.consAddr = newAddr
	}
	return t.deriveValconsHRP()
}

// sharesFromTokens is the SDK share math: amount * delegator_shares / tokens,
// truncated, as a LegacyDec.
func sharesFromTokens(v *validator, amount *big.Int) *big.Int {
	shares := new(big.Int).Mul(amount, v.shares)
	return shares.Quo(shares, v.tokens)
}

// deriveValconsHRP takes the consensus-address prefix from an existing
// signing info, or from the operator prefix by the SDK convention
// (<prefix>valoper -> <prefix>valcons).
func (t *takeover) deriveValconsHRP() error {
	if t.g.hasModule("slashing") {
		sl, err := t.g.module("slashing")
		if err != nil {
			return err
		}
		infos, err := sl.list("signing_infos")
		if err != nil {
			return fmt.Errorf("slashing: %w", err)
		}
		if len(infos) > 0 {
			view, err := decodeAs[addressedView](infos[0])
			if err != nil {
				return fmt.Errorf("slashing signing info: %w", err)
			}
			hrp, _, err := bech32Decode(view.Address)
			if err != nil {
				return fmt.Errorf("slashing signing info: %w", err)
			}
			t.valconsHRP = hrp
			return nil
		}
	}
	hrp, _, err := bech32Decode(t.validators[t.chosen[0]].operator)
	if err != nil {
		return fmt.Errorf("staking validator: %w", err)
	}
	prefix, ok := strings.CutSuffix(hrp, "valoper")
	if !ok {
		return fmt.Errorf("takeover: cannot derive the valcons prefix from operator prefix %q", hrp)
	}
	t.valconsHRP = prefix + "valcons"
	return nil
}

// govQuorumInverse is 1/govQuorum: the gov delegations must be at least this
// fraction of the bonded tokens for a lone vote to reach quorum.
var govQuorumInverse = big.NewInt(1_000_000)

// planDelegations adds the explicit gov amount to node0's validator and turns
// every chosen validator's boost shares (plus that amount) into a delegation
// from the gov account, valued at the validator's final exchange rate.
func (t *takeover) planDelegations(amount *big.Int) {
	node0 := &t.validators[t.chosen[0]]
	govShares := sharesFromTokens(node0, amount)
	node0.tokens = new(big.Int).Add(node0.tokens, amount)
	node0.shares = new(big.Int).Add(node0.shares, govShares)
	t.boost.Add(t.boost, amount)
	for i, vi := range t.chosen {
		v := &t.validators[vi]
		shares := new(big.Int).Set(v.boostShares)
		if i == 0 {
			shares.Add(shares, govShares)
		}
		if shares.Sign() == 0 {
			continue
		}
		stake := new(big.Int).Mul(shares, intToDec(v.tokens))
		stake.Quo(stake, v.shares)
		t.injected = append(t.injected, injectedDelegation{operator: v.operator, shares: shares, stake: stake})
	}
}

// writeStaking stores the validators, the injected delegations, and
// recomputes last_validator_powers and last_total_power from tokens / reduction.
func (t *takeover) writeStaking() (*TakeoverReport, error) {
	st, err := t.g.module("staking")
	if err != nil {
		return nil, err
	}
	raws := make([]json.RawMessage, len(t.validators))
	var lastPowers []lastPowerJSON
	total := new(big.Int)
	for i := range t.validators {
		v := &t.validators[i]
		raws[i] = v.raw
		if v.consAddr != nil {
			raws[i], err = edit(v.raw, func(o obj) error {
				return errors.Join(
					o.set("consensus_pubkey", consensusPubKeyJSON{Type: v.pubType, Key: v.pubKey}),
					o.set("tokens", v.tokens.String()),
					o.set("delegator_shares", formatDec(v.shares)),
					o.set("jailed", false),
				)
			})
			if err != nil {
				return nil, fmt.Errorf("staking validator %s: %w", v.operator, err)
			}
		}
		if v.bonded {
			power := new(big.Int).Quo(v.tokens, t.reduction)
			total.Add(total, power)
			lastPowers = append(lastPowers, lastPowerJSON{Address: v.operator, Power: power.String()})
		}
	}
	// CometBFT rejects a validator set above MaxInt64/8 total power.
	if total.Cmp(new(big.Int).SetInt64(math.MaxInt64/8)) > 0 {
		return nil, fmt.Errorf("takeover: total voting power %s exceeds the CometBFT maximum %d", total, int64(math.MaxInt64/8))
	}
	bondedTokens := new(big.Int)
	for _, v := range t.validators {
		if v.bonded {
			bondedTokens.Add(bondedTokens, v.tokens)
		}
	}
	govStake := new(big.Int)
	for _, d := range t.injected {
		govStake.Add(govStake, d.stake)
	}
	govStake.Quo(govStake, decUnit)
	if new(big.Int).Mul(govStake, govQuorumInverse).Cmp(bondedTokens) < 0 {
		return nil, fmt.Errorf("takeover: gov delegations of %s%s are below the quorum share (1/%s) of the %s bonded tokens after the boost",
			govStake, t.bondDenom, govQuorumInverse, bondedTokens)
	}
	if err := errors.Join(
		st.set("validators", raws),
		st.set("last_validator_powers", lastPowers),
		st.set("last_total_power", total.String()),
		t.writeDelegations(st),
	); err != nil {
		return nil, err
	}
	if err := t.g.setModule("staking", st); err != nil {
		return nil, err
	}
	report := &TakeoverReport{PowerReduction: t.reduction, BondDenom: t.bondDenom, TotalPower: total, BondedTokens: bondedTokens}
	for _, vi := range t.chosen {
		v := t.validators[vi]
		report.Validators = append(report.Validators, ChosenValidator{
			Operator: v.operator,
			ConsAddr: bech32Encode(t.valconsHRP, v.consAddr),
			Tokens:   v.tokens,
			Power:    new(big.Int).Quo(v.tokens, t.reduction),
		})
	}
	return report, nil
}

// writeDelegations appends the gov account's delegations; the gov account
// must not already delegate to a chosen validator.
func (t *takeover) writeDelegations(st obj) error {
	delegations, err := st.list("delegations")
	if err != nil {
		return fmt.Errorf("staking: %w", err)
	}
	chosen := map[string]bool{}
	for _, vi := range t.chosen {
		chosen[t.validators[vi].operator] = true
	}
	for _, raw := range delegations {
		d, err := decodeAs[delegationJSON](raw)
		if err != nil {
			return fmt.Errorf("staking delegation: %w", err)
		}
		if d.DelegatorAddress == t.gov && chosen[d.ValidatorAddress] {
			return fmt.Errorf("takeover: %s already delegates to %s", t.gov, d.ValidatorAddress)
		}
	}
	for _, d := range t.injected {
		raw, err := marshal(delegationJSON{DelegatorAddress: t.gov, ValidatorAddress: d.operator, Shares: formatDec(d.shares)})
		if err != nil {
			return err
		}
		delegations = append(delegations, raw)
	}
	return st.set("delegations", delegations)
}

// consensusSet is the CometBFT validator set of the genesis, which lives at
// .consensus.validators (v0.53) or at the top-level .validators (older).
type consensusSet struct {
	inConsensus bool
	byPubKey    map[string]cometValidatorJSON
}

func loadConsensusSet(g *Genesis) (consensusSet, error) {
	var set consensusSet
	var entries []json.RawMessage
	var consensus obj
	if raw, ok := g.top["consensus"]; ok {
		if err := json.Unmarshal(raw, &consensus); err != nil {
			return set, fmt.Errorf("genesis: consensus: %w", err)
		}
	}
	consensusEntries, err := consensus.list("validators")
	if err != nil {
		return set, fmt.Errorf("genesis: consensus: %w", err)
	}
	topEntries, err := obj(g.top).list("validators")
	if err != nil {
		return set, fmt.Errorf("genesis: %w", err)
	}
	_, hasConsensus := consensus["validators"]
	_, hasTop := g.top["validators"]
	switch {
	case len(consensusEntries) > 0:
		set.inConsensus, entries = true, consensusEntries
	case len(topEntries) > 0:
		entries = topEntries
	case hasConsensus:
		set.inConsensus = true
	case hasTop:
		// an empty top-level set is rewritten in place
	default:
		return set, errors.New("genesis: no validator set at .consensus.validators or .validators")
	}
	set.byPubKey = map[string]cometValidatorJSON{}
	for _, raw := range entries {
		v, err := decodeAs[cometValidatorJSON](raw)
		if err != nil {
			return set, fmt.Errorf("genesis validator set: %w", err)
		}
		set.byPubKey[v.PubKey.Value] = v
	}
	return set, nil
}

// address returns the consensus address of a validator: the one recorded in
// the set, or sha256(key)[:20] for an ed25519 key that is not in the set.
func (s consensusSet) address(v *validator) ([]byte, error) {
	if entry, ok := s.byPubKey[v.pubKey]; ok {
		addr, err := hex.DecodeString(entry.Address)
		if err != nil || len(addr) != consAddrLen {
			return nil, fmt.Errorf("genesis validator set: bad address %q", entry.Address)
		}
		return addr, nil
	}
	if v.pubType != ed25519PubKeyType {
		return nil, fmt.Errorf("takeover: validator %s has key type %s and is not in the genesis validator set", v.operator, v.pubType)
	}
	addr, err := consAddress(v.pubKey)
	if err != nil {
		return nil, fmt.Errorf("takeover: validator %s: %w", v.operator, err)
	}
	return addr, nil
}

// writeConsensusSet rebuilds the set from the bonded validators: chosen ones
// get their node's ed25519 key, the rest keep their recorded entry.
func (t *takeover) writeConsensusSet(oldSet consensusSet) error {
	var entries []cometValidatorJSON
	for i := range t.validators {
		v := &t.validators[i]
		if !v.bonded {
			continue
		}
		power := new(big.Int).Quo(v.tokens, t.reduction).String()
		entry, ok := oldSet.byPubKey[v.pubKey]
		if !ok {
			addr, err := oldSet.address(v)
			if err != nil {
				return err
			}
			entry = cometValidatorJSON{
				Address: strings.ToUpper(hex.EncodeToString(addr)),
				PubKey:  cometPubKeyJSON{Type: cometEd25519KeyType, Value: v.pubKey},
			}
		}
		entry.Power = power
		entries = append(entries, entry)
	}
	if !oldSet.inConsensus {
		raw, err := marshal(entries)
		if err != nil {
			return err
		}
		t.g.top["validators"] = raw
		return nil
	}
	var consensus obj
	if err := json.Unmarshal(t.g.top["consensus"], &consensus); err != nil {
		return fmt.Errorf("genesis: consensus: %w", err)
	}
	if err := consensus.set("validators", entries); err != nil {
		return err
	}
	raw, err := marshal(consensus)
	if err != nil {
		return err
	}
	t.g.top["consensus"] = raw
	// A genesis forklab wrote carries the mirror Bytes adds; keep it in step.
	if _, ok := t.g.top["validators"]; ok {
		t.g.top["validators"] = consensus["validators"]
	}
	return nil
}

// writeSlashing moves signing infos and missed blocks of the chosen
// validators to their new consensus addresses with a clean record, and adds a
// signing info for a chosen validator that has none (the slashing BeginBlock
// fails on a signer without one).
func (t *takeover) writeSlashing() error {
	if !t.g.hasModule("slashing") {
		return nil
	}
	sl, err := t.g.module("slashing")
	if err != nil {
		return err
	}
	infos, err := sl.list("signing_infos")
	if err != nil {
		return fmt.Errorf("slashing: %w", err)
	}
	seen := map[string]bool{}
	for i, raw := range infos {
		newAddr, ok, err := t.remapAddress(raw)
		if err != nil {
			return fmt.Errorf("slashing signing info: %w", err)
		}
		if !ok {
			continue
		}
		seen[newAddr] = true
		infos[i], err = marshal(signingInfoEntryJSON{Address: newAddr, ValidatorSigningInfo: t.freshSigningInfo(raw, newAddr)})
		if err != nil {
			return err
		}
	}
	for _, vi := range t.chosen {
		addr := bech32Encode(t.valconsHRP, t.validators[vi].consAddr)
		if seen[addr] {
			continue
		}
		raw, err := marshal(signingInfoEntryJSON{Address: addr, ValidatorSigningInfo: t.freshSigningInfo(nil, addr)})
		if err != nil {
			return err
		}
		infos = append(infos, raw)
	}
	if err := sl.set("signing_infos", infos); err != nil {
		return err
	}
	missed, err := sl.list("missed_blocks")
	if err != nil {
		return fmt.Errorf("slashing: %w", err)
	}
	for i, raw := range missed {
		newAddr, ok, err := t.remapAddress(raw)
		if err != nil {
			return fmt.Errorf("slashing missed blocks: %w", err)
		}
		if !ok {
			continue
		}
		missed[i], err = edit(raw, func(o obj) error {
			return errors.Join(o.set("address", newAddr), o.set("missed_blocks", []json.RawMessage{}))
		})
		if err != nil {
			return err
		}
	}
	if len(missed) > 0 {
		if err := sl.set("missed_blocks", missed); err != nil {
			return err
		}
	}
	return t.g.setModule("slashing", sl)
}

// freshSigningInfo keeps index_offset of an existing entry and clears
// everything that could jail or tombstone the validator.
func (t *takeover) freshSigningInfo(existing json.RawMessage, addr string) signingInfoJSON {
	info := signingInfoJSON{
		Address:             addr,
		StartHeight:         "0",
		IndexOffset:         "0",
		JailedUntil:         "1970-01-01T00:00:00Z",
		MissedBlocksCounter: "0",
	}
	if existing != nil {
		if entry, err := decodeAs[signingInfoEntryJSON](existing); err == nil && entry.ValidatorSigningInfo.IndexOffset != "" {
			info.IndexOffset = entry.ValidatorSigningInfo.IndexOffset
		}
	}
	return info
}

// remapAddress reads the bech32 "address" of an entry and returns its new
// encoding when it belongs to a chosen validator.
func (t *takeover) remapAddress(raw json.RawMessage) (string, bool, error) {
	view, err := decodeAs[addressedView](raw)
	if err != nil {
		return "", false, err
	}
	hrp, addr, err := bech32Decode(view.Address)
	if err != nil {
		return "", false, err
	}
	newAddr, ok := t.remap[string(addr)]
	if !ok {
		return "", false, nil
	}
	return bech32Encode(hrp, newAddr), true, nil
}

// writeDistribution points previous_proposer at node0 and records each
// injected delegation the way the SDK does when a delegation is created: a
// starting info at the validator's last ended period, whose historical entry
// gains one reference. Unlike the SDK it does not end the current period
// first, so the injected delegations also take their share of the rewards
// that accrued in the current period before the fork. That share comes out
// of the original delegators, not the pool: the period ratio is computed
// over the boosted tokens, so the payouts still sum to what was accrued.
func (t *takeover) writeDistribution() error {
	if !t.g.hasModule("distribution") {
		return nil
	}
	dist, err := t.g.module("distribution")
	if err != nil {
		return err
	}
	var proposer string
	if err := dist.get("previous_proposer", &proposer); err == nil && proposer != "" {
		hrp, _, err := bech32Decode(proposer)
		if err != nil {
			return fmt.Errorf("distribution previous_proposer: %w", err)
		}
		if err := dist.set("previous_proposer", bech32Encode(hrp, t.validators[t.chosen[0]].consAddr)); err != nil {
			return err
		}
	}
	if err := t.addStartingInfos(dist); err != nil {
		return err
	}
	return t.g.setModule("distribution", dist)
}

// addStartingInfos records the injected delegations in one pass over each
// distribution list.
func (t *takeover) addStartingInfos(dist obj) error {
	wanted := map[string]bool{}
	for _, d := range t.injected {
		wanted[d.operator] = true
	}
	current, err := dist.list("validator_current_rewards")
	if err != nil {
		return fmt.Errorf("distribution: %w", err)
	}
	previous := map[string]string{} // operator -> last ended period
	for _, raw := range current {
		view, err := decodeAs[currentRewardsView](raw)
		if err != nil {
			return fmt.Errorf("distribution current rewards: %w", err)
		}
		if !wanted[view.ValidatorAddress] {
			continue
		}
		period, err := parseInt(view.Rewards.Period.String())
		if err != nil {
			return fmt.Errorf("distribution current rewards %s: %w", view.ValidatorAddress, err)
		}
		previous[view.ValidatorAddress] = period.Sub(period, big.NewInt(1)).String()
	}
	for _, d := range t.injected {
		if _, ok := previous[d.operator]; !ok {
			return fmt.Errorf("distribution: no validator_current_rewards entry for %s", d.operator)
		}
	}
	historical, err := dist.list("validator_historical_rewards")
	if err != nil {
		return fmt.Errorf("distribution: %w", err)
	}
	referenced := map[string]bool{}
	for i, raw := range historical {
		view, err := decodeAs[historicalRewardsView](raw)
		if err != nil {
			return fmt.Errorf("distribution historical rewards: %w", err)
		}
		if period, ok := previous[view.ValidatorAddress]; !ok || view.Period.String() != period {
			continue
		}
		historical[i], err = edit(raw, func(o obj) error {
			rewards, err := edit(o["rewards"], func(r obj) error {
				var count uint32
				if err := r.get("reference_count", &count); err != nil {
					return err
				}
				return r.set("reference_count", count+1)
			})
			o["rewards"] = rewards
			return err
		})
		if err != nil {
			return fmt.Errorf("distribution historical rewards %s: %w", view.ValidatorAddress, err)
		}
		referenced[view.ValidatorAddress] = true
	}
	for _, d := range t.injected {
		if !referenced[d.operator] {
			return fmt.Errorf("distribution: no validator_historical_rewards entry for %s period %s", d.operator, previous[d.operator])
		}
	}
	height, err := t.g.InitialHeight()
	if err != nil {
		return err
	}
	infos, err := dist.list("delegator_starting_infos")
	if err != nil {
		return fmt.Errorf("distribution: %w", err)
	}
	for _, d := range t.injected {
		raw, err := marshal(startingInfoEntryJSON{
			DelegatorAddress: t.gov,
			ValidatorAddress: d.operator,
			StartingInfo: startingInfoJSON{
				PreviousPeriod: previous[d.operator],
				Stake:          formatDec(d.stake),
				Height:         height.String(),
			},
		})
		if err != nil {
			return err
		}
		infos = append(infos, raw)
	}
	return errors.Join(
		dist.set("validator_historical_rewards", historical),
		dist.set("delegator_starting_infos", infos),
	)
}

// writeAccounts injects the accounts and the boosted tokens (bonded pool and
// supply) in one auth pass and one bank pass, then checks that the gov
// delegator has an account.
func (t *takeover) writeAccounts(accounts []Account) error {
	found, err := addAccounts(t.g, accounts, []string{t.gov}, &Coin{Denom: t.bondDenom, Amount: t.boost})
	if err != nil {
		return err
	}
	if !found[t.gov] {
		return fmt.Errorf("takeover: gov delegator %s has no account; add it to Accounts", t.gov)
	}
	return nil
}
