// Package genesis rewrites Cosmos SDK genesis files as JSON: fork takeover,
// gov patch, account injection, and gojq patches. It never imports the SDK.
package genesis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
)

// Genesis holds a genesis file as raw JSON per top-level field and per
// app_state module, so every part the rewrite does not touch comes out with
// identical content (compacted).
type Genesis struct {
	top      map[string]json.RawMessage
	appState map[string]json.RawMessage
}

// Parse decodes a genesis file.
func Parse(data []byte) (*Genesis, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("genesis: %w", err)
	}
	rawApp, ok := top["app_state"]
	if !ok {
		return nil, errors.New("genesis: missing app_state")
	}
	var appState map[string]json.RawMessage
	if err := json.Unmarshal(rawApp, &appState); err != nil {
		return nil, fmt.Errorf("genesis: app_state: %w", err)
	}
	delete(top, "app_state")
	return &Genesis{top: top, appState: appState}, nil
}

// Bytes encodes the genesis as compact JSON that both genesis parsers accept.
// CometBFT's parser wants initial_height as a string and reads the consensus
// params and validator set from the top-level consensus_params and
// validators. The SDK's parser wants a number and reads them under
// .consensus; on a string it falls back to the CometBFT parser. So the string
// form is written, and .consensus.params and .consensus.validators are
// mirrored to the top level, where either parser finds them.
func (g *Genesis) Bytes() ([]byte, error) {
	top := maps.Clone(g.top)
	if _, ok := top["initial_height"]; ok {
		h, err := g.InitialHeight()
		if err != nil {
			return nil, err
		}
		raw, err := marshal(h.String())
		if err != nil {
			return nil, err
		}
		top["initial_height"] = raw
	}
	if raw, ok := top["consensus"]; ok {
		var consensus obj
		if err := json.Unmarshal(raw, &consensus); err != nil {
			return nil, fmt.Errorf("genesis: consensus: %w", err)
		}
		for inner, outer := range map[string]string{"params": "consensus_params", "validators": "validators"} {
			src, ok := consensus[inner]
			if !ok || bytes.Equal(src, []byte("null")) {
				continue
			}
			if err := sameContent(top[outer], src); err != nil {
				return nil, fmt.Errorf("genesis: %s and consensus.%s differ (patch consensus.%s): %w", outer, inner, inner, err)
			}
			top[outer] = src
		}
	}
	app, err := marshal(g.appState)
	if err != nil {
		return nil, err
	}
	top["app_state"] = app
	return marshal(top)
}

// InitialHeight returns initial_height, accepting both the string and the
// number encoding. A missing field means 1.
func (g *Genesis) InitialHeight() (*big.Int, error) {
	raw, ok := g.top["initial_height"]
	if !ok {
		return big.NewInt(1), nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("genesis: initial_height: %w", err)
	}
	return parseInt(n.String())
}

// BondDenom returns the staking bond denom.
func (g *Genesis) BondDenom() (string, error) {
	st, err := g.module("staking")
	if err != nil {
		return "", err
	}
	var params struct {
		BondDenom string `json:"bond_denom"`
	}
	if err := st.get("params", &params); err != nil {
		return "", fmt.Errorf("staking: %w", err)
	}
	if params.BondDenom == "" {
		return "", errors.New("staking: params.bond_denom is empty")
	}
	return params.BondDenom, nil
}

// Supply returns the bank supply of denom. When the export carries no supply
// list the bank module computes it from balances, and so does this.
func (g *Genesis) Supply(denom string) (*big.Int, error) {
	bank, err := g.module("bank")
	if err != nil {
		return nil, err
	}
	var supply []coinJSON
	if raw, ok := bank["supply"]; ok {
		if err := json.Unmarshal(raw, &supply); err != nil {
			return nil, fmt.Errorf("bank supply: %w", err)
		}
	}
	total := new(big.Int)
	if len(supply) > 0 {
		for _, c := range supply {
			if c.Denom == denom {
				return parseInt(c.Amount)
			}
		}
		return total, nil
	}
	var balances []balanceJSON
	if err := bank.get("balances", &balances); err != nil {
		return nil, fmt.Errorf("bank: %w", err)
	}
	for _, b := range balances {
		for _, c := range b.Coins {
			if c.Denom != denom {
				continue
			}
			n, err := parseInt(c.Amount)
			if err != nil {
				return nil, fmt.Errorf("bank balance %s: %w", b.Address, err)
			}
			total.Add(total, n)
		}
	}
	return total, nil
}

// sameContent reports whether a top-level copy, when it holds anything,
// equals the value under .consensus, ignoring key order.
func sameContent(copy, src json.RawMessage) error {
	if copy == nil || bytes.Equal(copy, []byte("null")) || bytes.Equal(copy, []byte("[]")) {
		return nil
	}
	a, errA := decodeTree(copy)
	b, errB := decodeTree(src)
	if err := errors.Join(errA, errB); err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("contents differ")
	}
	return nil
}

// obj is one JSON object with each field kept raw.
type obj map[string]json.RawMessage

// module decodes one app_state module.
func (g *Genesis) module(name string) (obj, error) {
	raw, ok := g.appState[name]
	if !ok {
		return nil, fmt.Errorf("genesis: app_state.%s: missing", name)
	}
	var o obj
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("genesis: app_state.%s: %w", name, err)
	}
	return o, nil
}

func (g *Genesis) hasModule(name string) bool {
	_, ok := g.appState[name]
	return ok
}

func (g *Genesis) setModule(name string, o obj) error {
	raw, err := marshal(o)
	if err != nil {
		return err
	}
	g.appState[name] = raw
	return nil
}

// get decodes field key into v. A missing key is an error.
func (o obj) get(key string, v any) error {
	raw, ok := o[key]
	if !ok {
		return fmt.Errorf("missing field %q", key)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("field %q: %w", key, err)
	}
	return nil
}

func (o obj) set(key string, v any) error {
	raw, err := marshal(v)
	if err != nil {
		return fmt.Errorf("field %q: %w", key, err)
	}
	o[key] = raw
	return nil
}

// list decodes an array field into raw elements. A missing or null field is
// an empty list.
func (o obj) list(key string) ([]json.RawMessage, error) {
	raw, ok := o[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("field %q: %w", key, err)
	}
	return items, nil
}

// decodeAs decodes raw into a typed view; unknown fields are ignored.
func decodeAs[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}

// edit decodes raw as an object, lets fn change fields, and re-encodes it.
func edit(raw json.RawMessage, fn func(obj) error) (json.RawMessage, error) {
	var o obj
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	if err := fn(o); err != nil {
		return nil, err
	}
	return marshal(o)
}

// marshal encodes without HTML escaping so raw strings pass through unchanged.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
