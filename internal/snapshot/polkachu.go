package snapshot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var polkachuPage = regexp.MustCompile(`^/tendermint_snapshots/([a-z0-9_-]+)/?$`)

// PolkachuProvider discovers the dated archive on a public snapshot page.
// Client is optional; nil uses http.DefaultClient.
type PolkachuProvider struct {
	Client *http.Client
}

var _ Provider = PolkachuProvider{}

func (PolkachuProvider) Supports(src string) bool {
	u, err := url.Parse(src)
	return err == nil && IsURL(src) && u.Host == "polkachu.com" && polkachuPage.MatchString(u.Path)
}

func (p PolkachuProvider) Resolve(ctx context.Context, src string) (string, error) {
	if !p.Supports(src) {
		return "", fmt.Errorf("polkachu: unsupported snapshot page %q", src)
	}
	u, _ := url.Parse(src)
	match := polkachuPage.FindStringSubmatch(u.Path)
	client := p.Client
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("polkachu snapshot page %s: %w", src, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("polkachu snapshot page %s: %s", src, resp.Status)
	}
	const maxPageSize = 2 << 20
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxPageSize+1))
	if err != nil {
		return "", fmt.Errorf("polkachu snapshot page %s: %w", src, err)
	}
	if len(page) > maxPageSize {
		return "", fmt.Errorf("polkachu snapshot page %s exceeds 2 MiB", src)
	}
	chain := regexp.QuoteMeta(match[1])
	link := regexp.MustCompile(`href\s*=\s*["'](https://snapshots\.polkachu\.com/snapshots/` + chain + `/` + chain + `_[0-9]+\.tar\.lz4)["']`)
	archive := link.FindSubmatch(page)
	if archive == nil {
		return "", fmt.Errorf("polkachu snapshot page %s has no %s archive link; pass a direct snapshot URL with --fork", src, match[1])
	}
	return string(archive[1]), nil
}
