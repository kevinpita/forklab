package chain

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Status is a node's view of itself and the chain tip.
type Status struct {
	NodeID          string
	Moniker         string
	Network         string
	LatestHeight    int64
	LatestBlockTime time.Time
	EarliestHeight  int64
	CatchingUp      bool
	// ValidatorAddress and VotingPower describe this node's own consensus key.
	// VotingPower is 0 when the key is not in the active set.
	ValidatorAddress string
	VotingPower      int64
}

// Status fetches /status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var r struct {
		NodeInfo struct {
			ID      string `json:"id"`
			Network string `json:"network"`
			Moniker string `json:"moniker"`
		} `json:"node_info"`
		SyncInfo struct {
			LatestHeight   int64     `json:"latest_block_height,string"`
			LatestTime     time.Time `json:"latest_block_time"`
			EarliestHeight int64     `json:"earliest_block_height,string"`
			CatchingUp     bool      `json:"catching_up"`
		} `json:"sync_info"`
		ValidatorInfo struct {
			Address     string `json:"address"`
			VotingPower int64  `json:"voting_power,string"`
		} `json:"validator_info"`
	}
	if err := c.call(ctx, "status", nil, &r); err != nil {
		return Status{}, err
	}
	return Status{
		NodeID:           r.NodeInfo.ID,
		Moniker:          r.NodeInfo.Moniker,
		Network:          r.NodeInfo.Network,
		LatestHeight:     r.SyncInfo.LatestHeight,
		LatestBlockTime:  r.SyncInfo.LatestTime,
		EarliestHeight:   r.SyncInfo.EarliestHeight,
		CatchingUp:       r.SyncInfo.CatchingUp,
		ValidatorAddress: r.ValidatorInfo.Address,
		VotingPower:      r.ValidatorInfo.VotingPower,
	}, nil
}

// PubKey is a consensus public key as CometBFT encodes it.
type PubKey struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Validator is one entry of the CometBFT validator set.
type Validator struct {
	Address          string `json:"address"`
	PubKey           PubKey `json:"pub_key"`
	VotingPower      int64  `json:"voting_power,string"`
	ProposerPriority int64  `json:"proposer_priority,string"`
}

const validatorsPerPage = 100

// Validators returns the full validator set at height, or at the latest height
// when height is 0. Pages after the first are pinned to the first page's
// height so the set cannot change mid-walk.
func (c *Client) Validators(ctx context.Context, height int64) ([]Validator, error) {
	var all []Validator
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(validatorsPerPage)}}
		if height > 0 {
			q.Set("height", strconv.FormatInt(height, 10))
		}
		var r struct {
			BlockHeight int64       `json:"block_height,string"`
			Validators  []Validator `json:"validators"`
			Total       int         `json:"total,string"`
		}
		if err := c.call(ctx, "validators", q, &r); err != nil {
			return nil, err
		}
		height = r.BlockHeight
		all = append(all, r.Validators...)
		if len(all) >= r.Total || len(r.Validators) == 0 {
			return all, nil
		}
	}
}

// Peer is one connected peer.
type Peer struct {
	ID       string
	Moniker  string
	RemoteIP string
	Outbound bool
}

// NetInfo is the node's p2p state.
type NetInfo struct {
	Listening bool
	Peers     []Peer
}

// NetInfo fetches /net_info.
func (c *Client) NetInfo(ctx context.Context) (NetInfo, error) {
	var r struct {
		Listening bool `json:"listening"`
		Peers     []struct {
			NodeInfo struct {
				ID      string `json:"id"`
				Moniker string `json:"moniker"`
			} `json:"node_info"`
			IsOutbound bool   `json:"is_outbound"`
			RemoteIP   string `json:"remote_ip"`
		} `json:"peers"`
	}
	if err := c.call(ctx, "net_info", nil, &r); err != nil {
		return NetInfo{}, err
	}
	info := NetInfo{Listening: r.Listening, Peers: make([]Peer, 0, len(r.Peers))}
	for _, p := range r.Peers {
		info.Peers = append(info.Peers, Peer{ID: p.NodeInfo.ID, Moniker: p.NodeInfo.Moniker, RemoteIP: p.RemoteIP, Outbound: p.IsOutbound})
	}
	return info, nil
}

// ErrNotEnoughBlocks means the node holds too few blocks to measure block time.
var ErrNotEnoughBlocks = errors.New("not enough blocks to measure block time")

// AvgBlockTime is the mean interval over the last n blocks. The node's
// earliest block is excluded because at initial height it carries the genesis
// time, which on a fork is far older than the blocks after it.
func (c *Client) AvgBlockTime(ctx context.Context, n int64) (time.Duration, error) {
	st, err := c.Status(ctx)
	if err != nil {
		return 0, err
	}
	hi := st.LatestHeight
	lo := max(hi-n, st.EarliestHeight+1)
	if lo >= hi {
		return 0, ErrNotEnoughBlocks
	}
	tLo, err := c.blockTime(ctx, lo)
	if err != nil {
		return 0, err
	}
	tHi, err := c.blockTime(ctx, hi)
	if err != nil {
		return 0, err
	}
	return tHi.Sub(tLo) / time.Duration(hi-lo), nil
}

func (c *Client) blockTime(ctx context.Context, h int64) (time.Time, error) {
	hs := strconv.FormatInt(h, 10)
	var r struct {
		BlockMetas []struct {
			Header struct {
				Height int64     `json:"height,string"`
				Time   time.Time `json:"time"`
			} `json:"header"`
		} `json:"block_metas"`
	}
	if err := c.call(ctx, "blockchain", url.Values{"minHeight": {hs}, "maxHeight": {hs}}, &r); err != nil {
		return time.Time{}, err
	}
	for _, m := range r.BlockMetas {
		if m.Header.Height == h {
			return m.Header.Time, nil
		}
	}
	return time.Time{}, fmt.Errorf("rpc blockchain: no header for height %d", h)
}

// WaitHeight polls /status until the latest height reaches h and returns that
// height. Request errors are retried, since the node may be restarting. If ctx
// ends first, the error names the last height seen or the last request error.
func (c *Client) WaitHeight(ctx context.Context, h int64) (int64, error) {
	last := errors.New("no response yet")
	for {
		st, err := c.Status(ctx)
		switch {
		case err == nil && st.LatestHeight >= h:
			return st.LatestHeight, nil
		case err == nil:
			last = fmt.Errorf("at height %d", st.LatestHeight)
		case ctx.Err() == nil:
			last = err
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for height %d: %w (%v)", h, ctx.Err(), last)
		case <-time.After(c.pollInterval):
		}
	}
}
