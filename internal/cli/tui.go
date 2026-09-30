package cli

import (
	"os"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd(a *app) *cobra.Command {
	var theme string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the terminal UI; every action it takes is a forklab command it shows",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.json {
				return output.Usagef("tui is interactive and has no --json output")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if theme == "" {
				theme = os.Getenv("FORKLAB_THEME")
			}
			return tui.Run(cmd.Context(), tui.Runner{Exe: exe}, theme)
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "", "ansi, tokyonight, catppuccin, or gruvbox (default $FORKLAB_THEME, else ansi)")
	return cmd
}
