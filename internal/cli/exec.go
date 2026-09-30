package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

type execView struct {
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

func (v execView) WriteHuman(io.Writer) error { return nil }

func newExecCmd(a *app) *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "exec -- <bin args>",
		Short: "Run the lab's chain binary against a live node and the lab keyring",
		Long: "Run node0's chain binary with --home, and for q, tx, keys, and status also\n" +
			"--node, --chain-id, and the lab's test keyring, unless the args set them.\n" +
			"Output and exit code pass through; --json wraps them in the envelope, with\n" +
			"ok false when the binary exits non-zero.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := openLab(ref)
			if err != nil {
				return err
			}
			// Commands that need no node, such as keys, still run on a
			// stopped lab and point at node0.
			i, err := e.live(cmd.Context())
			if err != nil && chain.ExecNeedsNode(args) {
				return err
			}
			args = e.cli(i).ExecArgs(args)
			run := exec.CommandContext(cmd.Context(), e.bin, args...)
			run.Stdin = cmd.InOrStdin()
			var stdout, stderr bytes.Buffer
			if a.json {
				run.Stdout, run.Stderr = &stdout, &stderr
			} else {
				run.Stdout, run.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			}
			code := 0
			if err := run.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					return fmt.Errorf("run %s: %w", e.bin, err)
				}
				code = exit.ExitCode()
			}
			if a.json {
				v := execView{Args: args, ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
				var err error
				if code == 0 {
					err = a.print(cmd, v)
				} else {
					err = output.PrintFailedJSON(cmd.OutOrStdout(), v)
				}
				if err != nil {
					return err
				}
			}
			if code != 0 {
				return output.ExitStatus(code)
			}
			return nil
		},
	}
	labRefFlag(cmd, &ref)
	return cmd
}
