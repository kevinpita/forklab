package genesis_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/genesis"
)

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func load(t *testing.T, name string) *genesis.Genesis {
	t.Helper()
	g, err := genesis.Parse(fixtureBytes(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// patched loads a fixture and applies gojq expressions to build a variant.
func patched(t *testing.T, name string, exprs ...string) *genesis.Genesis {
	t.Helper()
	g := load(t, name)
	if err := genesis.ApplyPatches(g, exprs); err != nil {
		t.Fatal(err)
	}
	return g
}

// doc is a decoded genesis document with path helpers. Numbers stay json.Number.
type doc map[string]any

func decode(t *testing.T, g *genesis.Genesis) doc {
	t.Helper()
	data, err := g.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return decodeBytes(t, data)
}

func decodeBytes(t *testing.T, data []byte) doc {
	t.Helper()
	var d doc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// at walks a dotted path; it returns nil when any segment is missing.
func (d doc) at(path string) any {
	var cur any = map[string]any(d)
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[key]; !ok {
			return nil
		}
	}
	return cur
}

func (d doc) str(path string) string {
	s, _ := d.at(path).(string)
	return s
}

func (d doc) list(path string) []map[string]any {
	items, _ := d.at(path).([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		out = append(out, m)
	}
	return out
}

func field(m map[string]any, path string) any {
	return doc(m).at(path)
}

func fieldStr(m map[string]any, path string) string {
	return doc(m).str(path)
}

func bigStr(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("not an integer: %q", s)
	}
	return n
}

// accountIdentity returns address and account_number of any auth account type.
func accountIdentity(a map[string]any) (string, string) {
	for _, nested := range []string{"base_vesting_account", "base_account"} {
		if inner, ok := a[nested].(map[string]any); ok {
			return accountIdentity(inner)
		}
	}
	return fieldStr(a, "address"), fmt.Sprint(a["account_number"])
}

// moduleAccount returns the address of a named module account.
func moduleAccount(t *testing.T, d doc, name string) string {
	t.Helper()
	for _, a := range d.list("app_state.auth.accounts") {
		if fieldStr(a, "name") == name {
			addr, _ := accountIdentity(a)
			return addr
		}
	}
	t.Fatalf("no module account %s", name)
	return ""
}

// bech32Prefix returns the account prefix of a chain from an operator
// address, e.g. "cosmos" from "cosmosvaloper1...".
func bech32Prefix(operator string) string {
	hrp, _, _ := strings.Cut(operator, "1")
	return strings.TrimSuffix(hrp, "valoper")
}

// accountPrefix returns the chain's account prefix from its first account.
func accountPrefix(d doc) string {
	addr, _ := accountIdentity(d.list("app_state.auth.accounts")[0])
	hrp, _, _ := strings.Cut(addr, "1")
	return hrp
}

// testAddr builds a valid bech32 account address from a seed byte.
func testAddr(prefix string, seed byte) string {
	return genesis.Bech32Encode(prefix, bytes.Repeat([]byte{seed}, 20))
}

// modulesUnchanged asserts that every app_state module outside touched has
// identical content (after compaction) between the input and the output.
func modulesUnchanged(t *testing.T, input, output []byte, touched ...string) {
	t.Helper()
	modules := func(data []byte) map[string]json.RawMessage {
		var top struct {
			AppState map[string]json.RawMessage `json:"app_state"`
		}
		if err := json.Unmarshal(data, &top); err != nil {
			t.Fatal(err)
		}
		return top.AppState
	}
	in, out := modules(input), modules(output)
	skip := map[string]bool{}
	for _, m := range touched {
		skip[m] = true
	}
	for name, raw := range in {
		if skip[name] {
			continue
		}
		var want bytes.Buffer
		if err := json.Compact(&want, raw); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want.Bytes(), out[name]) {
			t.Errorf("module %s changed", name)
		}
	}
}

func errContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want it to mention %q", err, want)
	}
}
