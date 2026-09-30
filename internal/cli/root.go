// Package cli wires the cobra command tree to the output contract.
package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

// app is the state every command shares.
type app struct {
	json bool
}

func (a *app) print(cmd *cobra.Command, r output.Result) error {
	return output.Print(cmd.OutOrStdout(), a.json, r)
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "forklab",
		Short: "Local multi-validator playground for Cosmos SDK chains",
		Long: "Local multi-validator playground for Cosmos SDK chains.\n\n" +
			"Run forklab with no command to open the terminal UI. Every action the UI takes is\n" +
			"one of the commands below, which scripts and agents run directly with --json.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(a, cmd, "")
		},
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print a JSON envelope instead of human output")
	root.AddCommand(newVersionCmd(a), newProfileCmd(a), newBinaryCmd(a), newSnapshotCmd(a), newLabCmd(a), newNodeCmd(a), newSupervisorCmd(a),
		newStatusCmd(a), newConsensusCmd(a), newAccountCmd(a), newExecCmd(a), newGovCmd(a), newUpgradeCmd(a), newRunbookCmd(a), newStoreCmd(a), newTUICmd(a))
	return root
}

// Run executes forklab with args and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	a := &app{}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRootCmd(a)
	root.SetContext(ctx)
	return execute(a, root, args, stdout, stderr)
}

func execute(a *app, root *cobra.Command, args []string, stdout, stderr io.Writer) int {
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	started := false
	markStarted(root, &started)

	err := root.Execute()
	if err == nil {
		return int(output.CodeOK)
	}
	if status := output.ExitStatus(0); errors.As(err, &status) {
		return int(status)
	}
	// Bad commands, args, and flags (including required flags and flag groups)
	// are rejected before the first user hook runs, so any error from that
	// phase is a usage error.
	if !started {
		err = output.Usage(err)
	}
	asJSON := a.json || (!started && jsonRequested(args))
	return int(output.PrintError(stdout, stderr, asJSON, err))
}

// markStarted wraps every user hook so the first one to run records that the
// command started. Cobra checks required flags and flag groups only after the
// pre-run hooks, so the wrapper checks them first.
func markStarted(c *cobra.Command, started *bool) {
	wrap := func(hook func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
		if hook == nil {
			return nil
		}
		return func(cmd *cobra.Command, args []string) error {
			if !*started {
				if err := cmd.ValidateRequiredFlags(); err != nil {
					return output.Usage(err)
				}
				if err := cmd.ValidateFlagGroups(); err != nil {
					return output.Usage(err)
				}
				*started = true
			}
			return hook(cmd, args)
		}
	}
	c.PersistentPreRunE = wrap(c.PersistentPreRunE)
	c.PreRunE = wrap(c.PreRunE)
	c.RunE = wrap(c.RunE)
	for _, sub := range c.Commands() {
		markStarted(sub, started)
	}
}

// jsonRequested finds --json when flag parsing failed before reaching it.
func jsonRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--json" {
			return true
		}
		if v, ok := strings.CutPrefix(arg, "--json="); ok {
			b, err := strconv.ParseBool(v)
			return err == nil && b
		}
	}
	return false
}
