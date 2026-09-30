package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStoreRawBytesHeightAndUnsupportedPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("data") != "0x01ff" || q.Get("height") != "20" {
			t.Errorf("query=%v", q)
		}
		code := 0
		log := ""
		path := q.Get("path")
		switch path {
		case `"/store/bank/key"`:
		case `"/store/bank/subspace"`:
			code = 6
			log = "subspace queries unsupported"
		default:
			t.Errorf("path=%q", path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"response": map[string]any{"code": code, "log": log, "height": "20", "key": "Af8=", "value": "AKo="}}})
	}))
	defer srv.Close()
	c, err := New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := StoreRequest{Name: "bank", KeyHex: "01ff", Height: 20}
	result, err := c.Store(context.Background(), request)
	if err != nil || result.Height != 20 || result.KeyHex != "01ff" || result.ValueHex != "00aa" || result.ValueBase64 != "AKo=" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	request.Prefix = true
	if _, err := c.Store(context.Background(), request); err == nil {
		t.Fatal("unsupported prefix accepted")
	}
	request.Name = "bank/key"
	if _, err := c.Store(context.Background(), request); err == nil {
		t.Fatal("invalid store name accepted")
	}
}
