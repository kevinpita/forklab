package genesis

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
)

// Coin is one denomination amount.
type Coin struct {
	Denom  string
	Amount *big.Int
}

// Account is a base account to inject with its initial balances.
type Account struct {
	Address  string
	Balances []Coin
}

// wire shapes of the auth and bank entries this package writes or reads.
type (
	coinJSON struct {
		Denom  string `json:"denom"`
		Amount string `json:"amount"`
	}
	balanceJSON struct {
		Address string     `json:"address"`
		Coins   []coinJSON `json:"coins"`
	}
	baseAccountJSON struct {
		Type          string  `json:"@type"`
		Address       string  `json:"address"`
		PubKey        *string `json:"pub_key"`
		AccountNumber string  `json:"account_number"`
		Sequence      string  `json:"sequence"`
	}
	// accountView reads the identity of any auth account type: base accounts
	// carry it at the top level, module and vesting accounts nest it.
	accountView struct {
		Name               string       `json:"name"`
		Address            string       `json:"address"`
		AccountNumber      json.Number  `json:"account_number"`
		BaseAccount        *accountView `json:"base_account"`
		BaseVestingAccount *accountView `json:"base_vesting_account"`
	}
)

func (a *accountView) identity() (address string, number *big.Int, err error) {
	switch {
	case a.BaseVestingAccount != nil:
		return a.BaseVestingAccount.identity()
	case a.BaseAccount != nil:
		return a.BaseAccount.identity()
	case a.Address == "":
		return "", nil, fmt.Errorf("account without address")
	}
	number, err = parseInt(a.AccountNumber.String())
	if err != nil {
		return "", nil, fmt.Errorf("account %s: account_number: %w", a.Address, err)
	}
	return a.Address, number, nil
}

// AddAccounts appends base accounts with the next free account numbers, their
// bank balances, and the matching supply. It works on fresh and exported
// genesis files. An address that already has an account or balance is an
// error.
func AddAccounts(g *Genesis, accounts []Account) error {
	_, err := addAccounts(g, accounts, nil, nil)
	return err
}

// bondedPoolName is the module account that holds the bonded tokens.
const bondedPoolName = "bonded_tokens_pool"

// addAccounts is AddAccounts plus what the takeover needs from the same
// passes over auth and bank, which on a mainnet export hold millions of
// entries: which of lookFor already have an account, and the pool boost
// added to the bonded pool balance and the supply.
func addAccounts(g *Genesis, accounts []Account, lookFor []string, poolBoost *Coin) (map[string]bool, error) {
	found := map[string]bool{}
	if len(accounts) == 0 && poolBoost == nil && len(lookFor) == 0 {
		return found, nil
	}
	auth, err := g.module("auth")
	if err != nil {
		return nil, err
	}
	bank, err := g.module("bank")
	if err != nil {
		return nil, err
	}
	existing, err := auth.list("accounts")
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	wanted := map[string]bool{}
	for _, a := range accounts {
		if wanted[a.Address] {
			return nil, fmt.Errorf("account %s given twice", a.Address)
		}
		wanted[a.Address] = true
	}
	for _, addr := range lookFor {
		wanted[addr] = true
	}
	next := big.NewInt(0)
	hrp := ""
	pool := ""
	for _, raw := range existing {
		view, err := decodeAs[accountView](raw)
		if err != nil {
			return nil, fmt.Errorf("auth account: %w", err)
		}
		address, number, err := view.identity()
		if err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		if number.Cmp(next) >= 0 {
			next = new(big.Int).Add(number, big.NewInt(1))
		}
		if hrp == "" {
			hrp, _, _ = bech32Decode(address)
		}
		if view.Name == bondedPoolName {
			pool = address
		}
		if wanted[address] {
			found[address] = true
		}
	}
	var added []Coin
	var newBalances []json.RawMessage
	injected := map[string]bool{}
	for _, a := range accounts {
		addrHRP, _, err := bech32Decode(a.Address)
		if err != nil {
			return nil, fmt.Errorf("account address: %w", err)
		}
		if hrp != "" && addrHRP != hrp {
			return nil, fmt.Errorf("account %s: prefix %q, chain uses %q", a.Address, addrHRP, hrp)
		}
		if found[a.Address] {
			return nil, fmt.Errorf("account %s already exists in genesis", a.Address)
		}
		found[a.Address] = true
		injected[a.Address] = true
		coins, err := sortedCoins(a.Balances)
		if err != nil {
			return nil, fmt.Errorf("account %s: %w", a.Address, err)
		}
		rawAcct, err := marshal(baseAccountJSON{
			Type:          "/cosmos.auth.v1beta1.BaseAccount",
			Address:       a.Address,
			AccountNumber: next.String(),
			Sequence:      "0",
		})
		if err != nil {
			return nil, err
		}
		existing = append(existing, rawAcct)
		next = new(big.Int).Add(next, big.NewInt(1))
		if len(coins) == 0 {
			continue
		}
		rawBal, err := marshal(balanceJSON{Address: a.Address, Coins: coins})
		if err != nil {
			return nil, err
		}
		newBalances = append(newBalances, rawBal)
		added = append(added, a.Balances...)
	}
	if poolBoost != nil && poolBoost.Amount.Sign() > 0 {
		if pool == "" {
			return nil, fmt.Errorf("auth: module account %q not found", bondedPoolName)
		}
		added = append(added, *poolBoost)
	} else {
		pool = ""
	}
	balances, err := bank.list("balances")
	if err != nil {
		return nil, fmt.Errorf("bank: %w", err)
	}
	boosted := false
	for i, raw := range balances {
		b, err := decodeAs[balanceJSON](raw)
		if err != nil {
			return nil, fmt.Errorf("bank balance: %w", err)
		}
		if injected[b.Address] {
			return nil, fmt.Errorf("account %s already has a bank balance in genesis", b.Address)
		}
		if b.Address != pool {
			continue
		}
		for j, c := range b.Coins {
			if c.Denom != poolBoost.Denom {
				continue
			}
			cur, err := parseInt(c.Amount)
			if err != nil {
				return nil, fmt.Errorf("bank balance %s: %w", pool, err)
			}
			b.Coins[j].Amount = new(big.Int).Add(cur, poolBoost.Amount).String()
			if balances[i], err = edit(raw, func(o obj) error { return o.set("coins", b.Coins) }); err != nil {
				return nil, err
			}
			boosted = true
		}
		if !boosted {
			return nil, fmt.Errorf("bonded pool %s holds no %s", pool, poolBoost.Denom)
		}
	}
	if pool != "" && !boosted {
		return nil, fmt.Errorf("bonded pool %s has no bank balance", pool)
	}
	if err := errors.Join(
		auth.set("accounts", existing),
		bank.set("balances", append(balances, newBalances...)),
		addSupply(bank, added),
		g.setModule("auth", auth),
		g.setModule("bank", bank),
	); err != nil {
		return nil, err
	}
	return found, nil
}

// sortedCoins validates and sorts coins the way the SDK requires in genesis:
// unique denoms in ascending order, positive amounts.
func sortedCoins(coins []Coin) ([]coinJSON, error) {
	out := make([]coinJSON, 0, len(coins))
	for _, c := range coins {
		if c.Denom == "" || c.Amount == nil || c.Amount.Sign() <= 0 {
			return nil, fmt.Errorf("invalid coin %s%s", c.Amount, c.Denom)
		}
		out = append(out, coinJSON{Denom: c.Denom, Amount: c.Amount.String()})
	}
	slices.SortFunc(out, func(a, b coinJSON) int { return strings.Compare(a.Denom, b.Denom) })
	for i := 1; i < len(out); i++ {
		if out[i].Denom == out[i-1].Denom {
			return nil, fmt.Errorf("duplicate denom %s", out[i].Denom)
		}
	}
	return out, nil
}

// addSupply adds coins to bank.supply, creating denoms that do not exist yet.
// An empty supply stays empty: the bank module then computes it from the
// balances, while a partial one makes it panic.
func addSupply(bank obj, coins []Coin) error {
	if len(coins) == 0 {
		return nil
	}
	var supply []coinJSON
	if raw, ok := bank["supply"]; ok {
		if err := json.Unmarshal(raw, &supply); err != nil {
			return fmt.Errorf("bank supply: %w", err)
		}
	}
	if len(supply) == 0 {
		return nil
	}
	amounts := map[string]*big.Int{}
	for _, c := range supply {
		n, err := parseInt(c.Amount)
		if err != nil {
			return fmt.Errorf("bank supply %s: %w", c.Denom, err)
		}
		amounts[c.Denom] = n
	}
	for _, c := range coins {
		if cur, ok := amounts[c.Denom]; ok {
			amounts[c.Denom] = new(big.Int).Add(cur, c.Amount)
		} else {
			amounts[c.Denom] = c.Amount
		}
	}
	supply = supply[:0]
	for denom, n := range amounts {
		if n.Sign() > 0 {
			supply = append(supply, coinJSON{Denom: denom, Amount: n.String()})
		}
	}
	slices.SortFunc(supply, func(a, b coinJSON) int { return strings.Compare(a.Denom, b.Denom) })
	return bank.set("supply", supply)
}
