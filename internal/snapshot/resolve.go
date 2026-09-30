package snapshot

import (
	"context"
	"net/http"
)

// Provider discovers an archive URL from a provider-specific source.
// Supports must perform no I/O. Resolve must respect the context and return
// a direct archive URL suitable for Reachable and Download.
type Provider interface {
	Supports(source string) bool
	Resolve(ctx context.Context, source string) (string, error)
}

// Resolver tries providers in order. Direct URLs and local archives pass
// through when no provider claims them. A claimed source's failure is returned
// without trying another provider or downloading the discovery page.
type Resolver struct {
	Providers []Provider
}

func (r Resolver) Resolve(ctx context.Context, source string) (string, error) {
	for _, provider := range r.Providers {
		if provider.Supports(source) {
			return provider.Resolve(ctx, source)
		}
	}
	return source, nil
}

// Resolve uses the built-in providers. Resolve once before checking or
// downloading so both steps use the same dated archive and cache key.
func Resolve(ctx context.Context, client *http.Client, source string) (string, error) {
	return (Resolver{Providers: []Provider{PolkachuProvider{Client: client}}}).Resolve(ctx, source)
}
