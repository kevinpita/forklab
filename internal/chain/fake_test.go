package chain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeNode serves the captured RPC responses in testdata/<chain>. It pages
// /validators one entry per page and filters /blockchain by height, the way a
// real node does, so paging and header lookups are exercised end to end.
type fakeNode struct {
	t     *testing.T
	dir   string
	dump  string
	mu    sync.Mutex
	calls []string
}

func newFakeNode(t *testing.T, chain string) (*fakeNode, *Client) {
	t.Helper()
	f := &fakeNode{t: t, dir: filepath.Join("testdata", chain)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeNode) fixture(name string) []byte {
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		f.t.Error(err)
	}
	return b
}

func (f *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.RequestURI())
	f.mu.Unlock()
	switch r.URL.Path {
	case "/status", "/net_info":
		_, _ = w.Write(f.fixture(r.URL.Path[1:] + ".json"))
	case "/dump_consensus_state":
		_, _ = w.Write(f.fixture(f.dump))
	case "/validators":
		f.validatorsPage(w, r)
	case "/blockchain":
		f.blockchain(w, r)
	default:
		http.NotFound(w, r)
	}
}

type envelope struct {
	Result map[string]json.RawMessage `json:"result"`
}

func (f *fakeNode) load(name string) envelope {
	var e envelope
	if err := json.Unmarshal(f.fixture(name), &e); err != nil {
		f.t.Error(err)
	}
	return e
}

func (f *fakeNode) validatorsPage(w http.ResponseWriter, r *http.Request) {
	e := f.load("validators.json")
	var vals []json.RawMessage
	_ = json.Unmarshal(e.Result["validators"], &vals)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	e.Result["validators"], _ = json.Marshal(vals[page-1 : page])
	e.Result["count"] = json.RawMessage(`"1"`)
	_ = json.NewEncoder(w).Encode(e)
}

func (f *fakeNode) blockchain(w http.ResponseWriter, r *http.Request) {
	e := f.load("blockchain.json")
	var metas []struct {
		Header struct {
			Height int64 `json:"height,string"`
		} `json:"header"`
	}
	var raws []json.RawMessage
	_ = json.Unmarshal(e.Result["block_metas"], &raws)
	_ = json.Unmarshal(e.Result["block_metas"], &metas)
	lo, _ := strconv.ParseInt(r.URL.Query().Get("minHeight"), 10, 64)
	hi, _ := strconv.ParseInt(r.URL.Query().Get("maxHeight"), 10, 64)
	var keep []json.RawMessage
	for i, m := range metas {
		if m.Header.Height >= lo && m.Header.Height <= hi {
			keep = append(keep, raws[i])
		}
	}
	e.Result["block_metas"], _ = json.Marshal(keep)
	_ = json.NewEncoder(w).Encode(e)
}
