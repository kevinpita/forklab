// Package chain is a small typed client for the CometBFT JSON RPC. It decodes
// only the fields forklab uses and ignores the rest, so it works across
// CometBFT 0.37 and 0.38.
package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds a single RPC request.
const DefaultTimeout = 5 * time.Second

// Client talks to one node's RPC endpoint.
type Client struct {
	base         *url.URL
	timeout      time.Duration
	pollInterval time.Duration
	http         *http.Client
}

// New returns a client for rpcURL, for example http://127.0.0.1:26657. The
// tcp:// form used by config.toml is accepted. timeout <= 0 uses
// DefaultTimeout.
func New(rpcURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.Replace(rpcURL, "tcp://", "http://", 1))
	if err != nil {
		return nil, fmt.Errorf("rpc url %q: %w", rpcURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("rpc url %q: want http://host:port", rpcURL)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{base: u, timeout: timeout, pollInterval: 500 * time.Millisecond, http: &http.Client{}}, nil
}

// RPCError is an error object returned by the node.
type RPCError struct {
	Method  string
	Code    int
	Message string
	Data    string
}

func (e *RPCError) Error() string {
	if e.Data == "" {
		return fmt.Sprintf("rpc %s: %s", e.Method, e.Message)
	}
	return fmt.Sprintf("rpc %s: %s: %s", e.Method, e.Message, e.Data)
}

// call GETs method with query params and decodes the JSON-RPC result into out.
func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	u := c.base.JoinPath(method)
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("rpc %s: %s", method, resp.Status)
		}
		return fmt.Errorf("rpc %s: decode response: %w", method, err)
	}
	if env.Error != nil {
		return &RPCError{Method: method, Code: env.Error.Code, Message: env.Error.Message, Data: env.Error.Data}
	}
	if len(env.Result) == 0 {
		return fmt.Errorf("rpc %s: %s: response has no result", method, resp.Status)
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("rpc %s: decode result: %w", method, err)
	}
	return nil
}
