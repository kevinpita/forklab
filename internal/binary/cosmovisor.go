package binary

import (
	"context"

	"github.com/kevinpita/forklab/internal/profile"
)

// The cosmovisor that labs in cosmovisor mode run their nodes under is not
// a release. It is https://github.com/cosmos/cosmos-sdk/pull/26879: main
// plus a fix for a signal handler that every Launcher.Run of cosmovisor
// v1.7.3 leaves registered. With that handler, a stop after an upgrade
// makes cosmovisor exit 1 at once and abandon the chain binary, which then
// has to be killed instead of shutting down.
//
// The commit is pinned, never the branch, which can be rewritten or deleted.
// It is fetched from the cosmos-sdk repository itself, where
// refs/pull/26879/head keeps it reachable whatever happens to the fork.
//
// Once a cosmovisor release carries the fix, return to its tag: set the ref
// to "tools/cosmovisor/v<version>" and CosmovisorVersion to "<version>".
const (
	cosmovisorCommit = "58bf3aad378754cecfcbc59744aedee39c38893b"

	// CosmovisorVersion names that build wherever it is shown or cached. It
	// is not a cosmovisor version number, on purpose.
	CosmovisorVersion = "pr26879-58bf3aad3787"
)

// Cosmovisor returns the pinned cosmovisor, built on first use and cached
// like any git source. A build from a clone reports "(devel)", so the
// version is not verified: the commit pins it. The build is static, so the
// file also runs in a container.
func (c Cache) Cosmovisor(ctx context.Context, o Options) (Binary, error) {
	o.NoVerify = true
	p := profile.Profile{
		Name:       "cosmovisor",
		BinaryName: "cosmovisor",
		Binaries: map[string]profile.Source{CosmovisorVersion: profile.GitSource{
			Repo:  "https://github.com/cosmos/cosmos-sdk",
			Ref:   cosmovisorCommit,
			Build: "cd tools/cosmovisor && go build -o cosmovisor ./cmd/cosmovisor",
			Out:   "tools/cosmovisor/cosmovisor",
			Env:   map[string]string{"CGO_ENABLED": "0"},
		}},
	}
	return c.Resolve(ctx, p, CosmovisorVersion, o)
}
