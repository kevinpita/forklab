package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X ...cli.version=<v>".
var version = "dev"

type versionInfo struct {
	Version  string `json:"version"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func (v versionInfo) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "forklab %s (%s %s)\n", v.Version, v.Go, v.Platform)
	return err
}

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the forklab version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.print(cmd, versionInfo{
				Version:  version,
				Go:       runtime.Version(),
				Platform: runtime.GOOS + "/" + runtime.GOARCH,
			})
		},
	}
}
