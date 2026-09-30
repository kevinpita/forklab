package chain

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TxResult is a transaction committed in a block.
type TxResult struct {
	Hash      string  `json:"hash"`
	Height    int64   `json:"height"`
	Code      uint32  `json:"code"`
	Codespace string  `json:"codespace,omitempty"`
	Log       string  `json:"log,omitempty"`
	Events    []Event `json:"-"`
}

type Event struct {
	Type       string
	Attributes []EventAttribute
}

type EventAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Attr returns the first value of key in an event of type typ.
func (r TxResult) Attr(typ, key string) (string, bool) {
	for _, e := range r.Events {
		if e.Type != typ {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key == key {
				return a.Value, true
			}
		}
	}
	return "", false
}

// Tx fetches /tx for a committed transaction hash.
func (c *Client) Tx(ctx context.Context, hash string) (TxResult, error) {
	var r struct {
		Hash     string `json:"hash"`
		Height   int64  `json:"height,string"`
		TxResult struct {
			Code      uint32 `json:"code"`
			Codespace string `json:"codespace"`
			Log       string `json:"log"`
			Events    []struct {
				Type       string           `json:"type"`
				Attributes []EventAttribute `json:"attributes"`
			} `json:"events"`
		} `json:"tx_result"`
	}
	if err := c.call(ctx, "tx", url.Values{"hash": {"0x" + strings.TrimPrefix(hash, "0x")}}, &r); err != nil {
		return TxResult{}, err
	}
	res := TxResult{Hash: r.Hash, Height: r.Height, Code: r.TxResult.Code, Codespace: r.TxResult.Codespace, Log: r.TxResult.Log}
	for _, e := range r.TxResult.Events {
		res.Events = append(res.Events, Event{Type: e.Type, Attributes: e.Attributes})
	}
	return res, nil
}

// WaitTx polls /tx until hash is in a block. A transaction that failed in
// the block is a *TxError. If ctx ends first, the error names the last
// response.
func (c *Client) WaitTx(ctx context.Context, hash string) (TxResult, error) {
	last := errors.New("no response yet")
	for {
		r, err := c.Tx(ctx, hash)
		switch {
		case err == nil && r.Code != 0:
			return r, &TxError{Hash: r.Hash, Code: r.Code, Codespace: r.Codespace, Log: r.Log}
		case err == nil:
			return r, nil
		case ctx.Err() == nil:
			last = err
		}
		select {
		case <-ctx.Done():
			return TxResult{}, fmt.Errorf("wait for tx %s: %w (%v)", hash, ctx.Err(), last)
		case <-time.After(c.pollInterval):
		}
	}
}
