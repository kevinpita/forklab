package lab

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kevinpita/forklab/internal/supervisor"
)

// ResetMethod is how a node's chain data was wiped.
type ResetMethod string

const (
	// ResetComet ran the chain's own `comet unsafe-reset-all`.
	ResetComet ResetMethod = "comet"
	// ResetManual removed data/ by hand for binaries without that command.
	ResetManual ResetMethod = "manual"
)

// Reset wipes every node's chain data so the lab replays from genesis on its
// next start. Config, keys, and genesis stay. The lab must be down.
func Reset(ctx context.Context, dir string) ([]ResetMethod, error) {
	specs, err := supervisor.LoadNodes(dir)
	if err != nil {
		return nil, err
	}
	methods := make([]ResetMethod, len(specs))
	for i, s := range specs {
		if methods[i], err = resetNode(ctx, s); err != nil {
			return nil, fmt.Errorf("reset %s: %w", s.Name, err)
		}
	}
	return methods, nil
}

func resetNode(ctx context.Context, s supervisor.NodeSpec) (ResetMethod, error) {
	cli := chainCLI{ctx: ctx, bin: s.Binary, log: io.Discard}
	if cli.helpHas([]string{"comet", "unsafe-reset-all"}, "--keep-addr-book") {
		_, err := cli.run("reset", "comet", "unsafe-reset-all", "--keep-addr-book", "--home", s.Home)
		return ResetComet, err
	}
	return ResetManual, resetData(filepath.Join(s.Home, "data"))
}

// zeroValidatorState is priv_validator_state.json at height 0, which lets the
// validator sign the replayed chain's first blocks again.
const zeroValidatorState = `{
  "height": "0",
  "round": 0,
  "step": 0
}
`

// resetData empties a node's data directory the way unsafe-reset-all does.
func resetData(data string) error {
	if err := os.RemoveAll(data); err != nil {
		return err
	}
	if err := os.Mkdir(data, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(data, "priv_validator_state.json"), []byte(zeroValidatorState), 0o600)
}
