package tui

import (
	_ "embed"
	"slices"
	"strings"
)

// cliCommands lists every command path the CLI has, one per line. The cli
// package's tests keep it equal to the cobra tree.
//
//go:embed cli_commands.txt
var cliCommands string

var commandPaths = func() [][]string {
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(cliCommands), "\n") {
		out = append(out, strings.Fields(l))
	}
	return out
}()

// knownCommand reports whether args start with a forklab command, or name a
// command group whose help the CLI prints, so the palette never offers to
// run a word that is not a command.
func knownCommand(args []string) bool {
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		words = append(words, a)
	}
	if len(words) == 0 {
		return false
	}
	for _, p := range commandPaths {
		n := min(len(p), len(words))
		if slices.Equal(p[:n], words[:n]) && (n == len(p) || n == len(words)) {
			return true
		}
	}
	return false
}
