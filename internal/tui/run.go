package tui

import (
	"context"
	"os"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Run starts the TUI on the terminal and returns when the user quits.
// themeName picks the theme; the terminal background is read once, here,
// before Bubble Tea owns stdin.
func Run(ctx context.Context, run Runner, themeName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	th := buildTheme(themeName, lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
	_, err := tea.NewProgram(New(ctx, run, th), tea.WithContext(ctx)).Run()
	return err
}
