package chain

import (
	"context"
	"fmt"
	"time"
)

// AppInfo describes committed application state. CometBFT's block store can
// be one block ahead when FinalizeBlock fails, so Status is not a pause proof.
type AppInfo struct {
	Height  int64  `json:"height"`
	AppHash string `json:"app_hash"`
}

func (c *Client) AppInfo(ctx context.Context) (AppInfo, error) {
	var r struct {
		Response struct {
			Height  int64  `json:"last_block_height,string"`
			AppHash string `json:"last_block_app_hash"`
		} `json:"response"`
	}
	if err := c.call(ctx, "abci_info", nil, &r); err != nil {
		return AppInfo{}, err
	}
	return AppInfo{Height: r.Response.Height, AppHash: r.Response.AppHash}, nil
}

func (c *Client) WaitAppHeight(ctx context.Context, height int64) (int64, error) {
	for {
		info, err := c.AppInfo(ctx)
		if err == nil && info.Height >= height {
			return info.Height, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for application height %d: %w", height, ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}
