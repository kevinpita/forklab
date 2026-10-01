package cli

import (
	"fmt"
	"io"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

type shell string

const (
	bash       shell = "bash"
	zsh        shell = "zsh"
	fish       shell = "fish"
	powershell shell = "powershell"
)

func shellArgs(helpers bool) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return output.Usagef("expected one shell name")
		}
		switch shell(args[0]) {
		case bash, zsh, fish:
			return nil
		case powershell:
			if !helpers {
				return nil
			}
		}
		return output.Usagef("unsupported shell %q", args[0])
	}
}

func newCompletionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:       "completion <bash|zsh|fish|powershell>",
		Short:     "Print a shell completion script",
		Args:      shellArgs(false),
		ValidArgs: []string{string(bash), string(zsh), string(fish), string(powershell)},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.json {
				return output.Usagef("completion prints shell code and does not support --json")
			}
			return writeCompletion(cmd.Root(), cmd.OutOrStdout(), shell(args[0]))
		},
	}
}

func newShellCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "shell", Short: "Integrate forklab with your shell"}
	cmd.AddCommand(&cobra.Command{
		Use:       "init <bash|zsh|fish>",
		Short:     "Print completions and fl, flab, flogs helpers",
		Args:      shellArgs(true),
		ValidArgs: []string{string(bash), string(zsh), string(fish)},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.json {
				return output.Usagef("shell init prints shell code and does not support --json")
			}
			sh := shell(args[0])
			if err := writeCompletion(cmd.Root(), cmd.OutOrStdout(), sh); err != nil {
				return err
			}
			_, err := fmt.Fprint(cmd.OutOrStdout(), shellHelpers(sh))
			return err
		},
	})
	return cmd
}

func writeCompletion(root *cobra.Command, w io.Writer, sh shell) error {
	switch sh {
	case bash:
		return root.GenBashCompletionV2(w, true)
	case zsh:
		return root.GenZshCompletion(w)
	case fish:
		return root.GenFishCompletion(w, true)
	case powershell:
		return root.GenPowerShellCompletionWithDesc(w)
	}
	return output.Usagef("unsupported shell %q", sh)
}

func shellHelpers(sh shell) string {
	if sh == fish {
		return `
function fl --wraps forklab
    command forklab $argv
end
function flab
    if test (count $argv) -lt 1; or test -z "$argv[1]"
        printf '%s\n' 'usage: flab LAB [COMMAND ARGS...]' >&2
        return 2
    end
    set -l lab $argv[1]
    set -e argv[1]
    if test (count $argv) -eq 0
        set argv status
    end
    command forklab --lab="$lab" $argv
end
function flogs
    if test (count $argv) -lt 2; or test -z "$argv[1]"; or test -z "$argv[2]"
        printf '%s\n' 'usage: flogs LAB NODE [ARGS...]' >&2
        return 2
    end
    set -l lab $argv[1]
    set -l node $argv[2]
    set -e argv[1..2]
    command forklab node logs --lab="$lab" "$node" $argv
end
`
	}
	helpers := `
fl() { command forklab "$@"; }
flab() {
    if [ "$#" -lt 1 ] || [ -z "$1" ]; then
        printf '%s\n' 'usage: flab LAB [COMMAND ARGS...]' >&2
        return 2
    fi
    local lab="$1"
    shift
    if [ "$#" -eq 0 ]; then set -- status; fi
    command forklab --lab="$lab" "$@"
}
flogs() {
    if [ "$#" -lt 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
        printf '%s\n' 'usage: flogs LAB NODE [ARGS...]' >&2
        return 2
    fi
    local lab="$1" node="$2"
    shift 2
    command forklab node logs --lab="$lab" "$node" "$@"
}
`
	if sh == bash {
		return helpers + "complete -o default -F __start_forklab fl\n"
	}
	return helpers + "compdef _forklab fl\n"
}
