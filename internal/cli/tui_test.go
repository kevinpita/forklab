package cli

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func stubTUI(t *testing.T) *[]string {
	t.Helper()
	var themes []string
	prev := openTUI
	openTUI = func(_ context.Context, theme string) error {
		themes = append(themes, theme)
		return nil
	}
	t.Cleanup(func() { openTUI = prev })
	return &themes
}

func TestBareForklabOpensTheTUI(t *testing.T) {
	opened := stubTUI(t)
	t.Setenv("FORKLAB_THEME", "gruvbox")
	if code, stdout, stderr := run(); code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("forklab: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if code, _, _ := run("tui", "--theme", "catppuccin"); code != 0 {
		t.Fatalf("forklab tui: code %d", code)
	}
	if strings.Join(*opened, ",") != "gruvbox,catppuccin" {
		t.Fatalf("TUI opened with themes %q, want gruvbox then catppuccin", *opened)
	}
}

func TestBareForklabRefusesJSONAndStillHelps(t *testing.T) {
	opened := stubTUI(t)
	code, stdout, _ := run("--json")
	if code != 2 || !strings.Contains(stdout, `"code":"usage"`) {
		t.Fatalf("forklab --json: code %d stdout %q, want a usage envelope", code, stdout)
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		code, stdout, _ := run(args...)
		if code != 0 || !strings.Contains(stdout, "Run forklab with no command to open the terminal UI") || !strings.Contains(stdout, "Available Commands") {
			t.Errorf("forklab %s: code %d, stdout %q", args[0], code, stdout)
		}
	}
	if code, _, _ := run("nope"); code != 2 {
		t.Errorf("forklab nope: code %d, want 2", code)
	}
	if len(*opened) != 0 {
		t.Fatalf("TUI opened %d times; --json, help, and a bad command must not open it", len(*opened))
	}
}

var update = flag.Bool("update", false, "rewrite the TUI's command list fixture")

// commandList is the TUI's fixture: every command a user can run, which
// the TUI's reachability test checks it offers.
const commandList = "../tui/cli_commands.txt"

// TestCLICommandListFixture keeps the fixture equal to the cobra tree, so a
// new command fails the TUI's reachability test until the TUI offers it.
func TestCLICommandListFixture(t *testing.T) {
	var paths []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" {
			return
		}
		if c.Runnable() && c.HasParent() {
			paths = append(paths, strings.TrimPrefix(c.CommandPath(), "forklab "))
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd(&app{}))
	sort.Strings(paths)
	want := strings.Join(paths, "\n") + "\n"
	if *update {
		if err := os.WriteFile(filepath.FromSlash(commandList), []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.FromSlash(commandList))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale; run go test ./internal/cli -run TestCLICommandListFixture -update\ngot:\n%s\nwant:\n%s", commandList, got, want)
	}
}
