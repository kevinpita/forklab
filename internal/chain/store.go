package chain

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// StoreRequest addresses raw bytes in a module's KV store. A prefix query
// requires the chain's ABCI subspace route, which some SDK versions disable.
type StoreRequest struct {
	Name   string `yaml:"name" json:"name"`
	KeyHex string `yaml:"key_hex" json:"key_hex"`
	Prefix bool   `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Height int64  `yaml:"height,omitempty" json:"height,omitempty"`
}

type StoreResult struct {
	Height      int64  `json:"height"`
	KeyHex      string `json:"key_hex"`
	ValueHex    string `json:"value_hex"`
	ValueBase64 string `json:"value_base64"`
}

func (r StoreRequest) Validate() error {
	if r.Name == "" || strings.ContainsAny(r.Name, "/\\") {
		return fmt.Errorf("store name must be a nonempty module store name")
	}
	if r.Height < 0 {
		return fmt.Errorf("store height must not be negative")
	}
	key, err := hex.DecodeString(r.KeyHex)
	if err != nil || len(key) == 0 {
		return fmt.Errorf("store key_hex must contain nonempty hexadecimal bytes")
	}
	return nil
}

// Store queries CometBFT's read-only ABCI interface. Values are deliberately
// kept as bytes: decoding a module's protobuf is chain specific.
func (c *Client) Store(ctx context.Context, r StoreRequest) (StoreResult, error) {
	if err := r.Validate(); err != nil {
		return StoreResult{}, err
	}
	key, _ := hex.DecodeString(r.KeyHex)
	mode := "key"
	if r.Prefix {
		mode = "subspace"
	}
	params := url.Values{"path": {strconv.Quote("/store/" + r.Name + "/" + mode)}, "data": {"0x" + hex.EncodeToString(key)}, "height": {strconv.FormatInt(r.Height, 10)}, "prove": {"false"}}
	var resp struct {
		Response struct {
			Code   uint32 `json:"code"`
			Log    string `json:"log"`
			Key    string `json:"key"`
			Value  string `json:"value"`
			Height string `json:"height"`
		} `json:"response"`
	}
	if err := c.call(ctx, "abci_query", params, &resp); err != nil {
		return StoreResult{}, err
	}
	if resp.Response.Code != 0 {
		return StoreResult{}, fmt.Errorf("store %s %s query failed (code %d): %s", r.Name, mode, resp.Response.Code, resp.Response.Log)
	}
	value, err := base64.StdEncoding.DecodeString(resp.Response.Value)
	if err != nil {
		return StoreResult{}, err
	}
	returnedKey, err := base64.StdEncoding.DecodeString(resp.Response.Key)
	if err != nil {
		return StoreResult{}, err
	}
	height, err := strconv.ParseInt(resp.Response.Height, 10, 64)
	if err != nil {
		return StoreResult{}, err
	}
	return StoreResult{Height: height, KeyHex: hex.EncodeToString(returnedKey), ValueHex: hex.EncodeToString(value), ValueBase64: resp.Response.Value}, nil
}
