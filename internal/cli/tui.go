package cli

import (
	"context"
	"os"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd(a *app) *cobra.Command {
	var theme string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the terminal UI (the same as forklab with no command)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(a, cmd, theme)
		},
	}
	cmd.Flags().StringVar(&theme, "theme", "", "ansi, tokyonight, catppuccin, or gruvbox (default $FORKLAB_THEME, else ansi)")
	return cmd
}

// openTUI runs the terminal UI until the user quits; tests replace it.
var openTUI = func(ctx context.Context, theme string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return tui.Run(ctx, tui.Runner{Exe: exe}, theme)
}

func runTUI(a *app, cmd *cobra.Command, theme string) error {
	if a.json {
		return output.Usagef("the terminal UI is interactive and has no --json output; name a command")
	}
	if theme == "" {
		theme = os.Getenv("FORKLAB_THEME")
	}
	return openTUI(cmd.Context(), theme)
}
