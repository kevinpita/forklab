package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

// newSupervisorCmd is the hidden plumbing behind lab up and down: run is
// what EnsureRunning spawns, exit and down talk to a running supervisor.
func newSupervisorCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "supervisor",
		Short:  "Run or control a lab supervisor process",
		Hidden: true,
	}
	cmd.AddCommand(newSupervisorRunCmd(), newSupervisorExitCmd(a), newSupervisorDownCmd(a))
	return cmd
}

func labFlag(cmd *cobra.Command, labDir *string) {
	cmd.Flags().StringVar(labDir, "lab", "", "lab directory")
	_ = cmd.MarkFlagRequired("lab")
}

func newSupervisorRunCmd() *cobra.Command {
	var labDir string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the supervisor in the foreground until SIGTERM, down, or exit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := supervisor.Run(cmd.Context(), supervisor.Options{LabDir: labDir, Log: cmd.ErrOrStderr(), RecordVersion: recordVersion(labDir)})
			if errors.Is(err, supervisor.ErrAlreadyRunning) {
				return nil
			}
			return err
		},
	}
	labFlag(cmd, &labDir)
	return cmd
}

// dial wraps a missing supervisor as exit code 3.
func dial(labDir string) (*supervisor.Client, error) {
	c, err := supervisor.Dial(labDir)
	if errors.Is(err, supervisor.ErrNotRunning) {
		return nil, fmt.Errorf("%w: %w", output.ErrLabNotRunning, err)
	}
	return c, err
}

func newSupervisorExitCmd(a *app) *cobra.Command {
	var labDir string
	cmd := &cobra.Command{
		Use:   "exit",
		Short: "Stop the supervisor and leave the nodes running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(labDir)
			if err != nil {
				return err
			}
			if err := c.Exit(); err != nil {
				return err
			}
			return a.print(cmd, nil)
		},
	}
	labFlag(cmd, &labDir)
	return cmd
}

func newSupervisorDownCmd(a *app) *cobra.Command {
	var labDir string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop every node, then the supervisor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(labDir)
			if err != nil {
				return err
			}
			nodes, err := c.Down(timeout)
			if err != nil {
				return err
			}
			return a.print(cmd, nodeList(nodes))
		},
	}
	labFlag(cmd, &labDir)
	cmd.Flags().DurationVar(&timeout, "timeout", supervisor.DefaultStopTimeout, "how long to wait for SIGTERM before SIGKILL")
	return cmd
}

func ensureSupervisor(ctx context.Context, labDir string) (*supervisor.Client, error) {
	return supervisor.EnsureRunning(ctx, labDir, supervisor.ForklabSpawner)
}
