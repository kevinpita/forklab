package genesis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/itchyny/gojq"
)

// ApplyPatches runs each gojq expression over the whole genesis document, in
// order, and keeps the single value it produces. Numbers keep their
// precision, and every top-level field and app_state module the patches leave
// equal keeps its original bytes. An error names the failing patch by index.
func ApplyPatches(g *Genesis, exprs []string) error {
	if len(exprs) == 0 {
		return nil
	}
	before, err := g.tree()
	if err != nil {
		return err
	}
	var doc any = before
	for i, expr := range exprs {
		query, err := gojq.Parse(expr)
		if err != nil {
			return fmt.Errorf("genesis patch %d: %w", i, err)
		}
		iter := query.Run(doc)
		out, ok := iter.Next()
		if !ok {
			return fmt.Errorf("genesis patch %d: produced no output", i)
		}
		if err, isErr := out.(error); isErr {
			return fmt.Errorf("genesis patch %d: %w", i, err)
		}
		if _, more := iter.Next(); more {
			return fmt.Errorf("genesis patch %d: produced more than one output", i)
		}
		doc = out
	}
	after, ok := doc.(map[string]any)
	if !ok {
		return errors.New("genesis patch: result is not a JSON object")
	}
	appState, ok := after["app_state"].(map[string]any)
	if !ok {
		return errors.New("genesis patch: result has no app_state object")
	}
	top, err := mergeTree(g.top, before, after, "app_state")
	if err != nil {
		return err
	}
	modules, err := mergeTree(g.appState, before["app_state"].(map[string]any), appState, "")
	if err != nil {
		return err
	}
	g.top, g.appState = top, modules
	return nil
}

// tree decodes the genesis into gojq values, numbers as json.Number.
func (g *Genesis) tree() (map[string]any, error) {
	top := make(map[string]any, len(g.top)+1)
	for key, raw := range g.top {
		v, err := decodeTree(raw)
		if err != nil {
			return nil, fmt.Errorf("genesis: %s: %w", key, err)
		}
		top[key] = v
	}
	appState := make(map[string]any, len(g.appState))
	for name, raw := range g.appState {
		v, err := decodeTree(raw)
		if err != nil {
			return nil, fmt.Errorf("genesis: app_state.%s: %w", name, err)
		}
		appState[name] = v
	}
	top["app_state"] = appState
	return top, nil
}

func decodeTree(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// mergeTree re-encodes only the keys whose value changed, keeps the raw bytes
// of the others, and drops keys the patches removed. skip names a key that is
// handled separately.
func mergeTree(raws map[string]json.RawMessage, before, after map[string]any, skip string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(after))
	for key, v := range after {
		if key == skip {
			continue
		}
		if old, ok := before[key]; ok && reflect.DeepEqual(old, v) {
			out[key] = raws[key]
			continue
		}
		raw, err := gojq.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("genesis patch: %s: %w", key, err)
		}
		out[key] = raw
	}
	return out, nil
}
