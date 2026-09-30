package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/control"
	"github.com/kevinpita/forklab/internal/runbook"
	"github.com/spf13/cobra"
)

type recipeRuntime struct{ env labEnv }

func (r recipeRuntime) WaitHeight(ctx context.Context, h int64) (int64, error) {
	if st, err := control.Load(r.env.dir); err != nil {
		return 0, err
	} else if st != nil && h > st.Height {
		return 0, fmt.Errorf("chain paused at %d; resume before waiting for %d", st.Height, h)
	}
	i, err := r.env.live(ctx)
	if err != nil {
		return 0, err
	}
	return r.env.clients[i].WaitAppHeight(ctx, h)
}

func (r recipeRuntime) Tx(ctx context.Context, from string, args []string) (runbook.Transaction, error) {
	if st, err := control.Load(r.env.dir); err != nil {
		return runbook.Transaction{}, err
	} else if st != nil {
		return runbook.Transaction{}, fmt.Errorf("chain has a pause at %d; resume before sending transactions", st.Height)
	}
	if _, ok := r.env.account(from); !ok {
		return runbook.Transaction{}, fmt.Errorf("unknown lab key %q; available: %s", from, r.env.accountNames())
	}
	cli, rpc, err := r.env.liveCLI(ctx)
	if err != nil {
		return runbook.Transaction{}, err
	}
	hash, err := cli.Broadcast(ctx, from, args...)
	if err == nil {
		result, waitErr := rpc.WaitTx(ctx, hash)
		if waitErr == nil || errors.As(waitErr, new(*chain.TxError)) {
			return runbook.Transaction{Hash: result.Hash, Height: result.Height, Code: result.Code, Codespace: result.Codespace, Log: result.Log}, nil
		}
		err = waitErr
	}
	var txErr *chain.TxError
	if errors.As(err, &txErr) {
		return runbook.Transaction{Hash: txErr.Hash, Code: txErr.Code, Codespace: txErr.Codespace, Log: txErr.Log}, nil
	}
	return runbook.Transaction{Hash: hash}, err
}

func (r recipeRuntime) Query(ctx context.Context, args []string) (any, error) {
	cli, _, err := r.env.liveCLI(ctx)
	if err != nil {
		return nil, err
	}
	data, err := cli.Query(ctx, args...)
	if err != nil {
		return nil, err
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	err = dec.Decode(&out)
	return out, err
}

func (r recipeRuntime) Store(ctx context.Context, request chain.StoreRequest) (any, error) {
	i, err := r.env.live(ctx)
	if err != nil {
		return nil, err
	}
	return r.env.clients[i].Store(ctx, request)
}

func (r recipeRuntime) controller(ctx context.Context) (control.Controller, error) {
	sup, err := ensureSupervisor(ctx, r.env.dir)
	return control.Controller{Dir: r.env.dir, Supervisor: sup, Clients: r.env.clients}, err
}

func (r recipeRuntime) Pause(ctx context.Context, h int64) (any, error) {
	c, err := r.controller(ctx)
	if err != nil {
		return nil, err
	}
	st, err := c.Pause(ctx, h)
	if st == nil {
		return nil, err
	}
	return map[string]any{"height": st.Height, "phase": st.Phase}, err
}

func (r recipeRuntime) WaitResumed(ctx context.Context) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		st, err := control.Load(r.env.dir)
		if err != nil {
			return err
		}
		if st == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (r recipeRuntime) Resume(ctx context.Context) error {
	c, err := r.controller(ctx)
	if err != nil {
		return err
	}
	return c.Resume(ctx)
}

func readRunbook(path string) (runbook.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return runbook.Document{}, err
	}
	defer func() { _ = f.Close() }()
	d, err := runbook.Decode(f)
	return d, output.Usage(err)
}

type recipeValidation struct {
	Name  string `json:"name"`
	Steps int    `json:"steps"`
}

func (v recipeValidation) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "valid runbook %s: %d steps\n", v.Name, v.Steps)
	return err
}

func newRunbookCmd(a *app) *cobra.Command {
	var ref string
	cmd := &cobra.Command{Use: "runbook", Short: "Run reusable transaction and state verification recipes"}
	labRefFlag(cmd, &ref)
	cmd.AddCommand(&cobra.Command{Use: "validate <file.yaml>", Short: "Validate a recipe without touching a chain", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		d, err := readRunbook(args[0])
		if err != nil {
			return err
		}
		return a.print(cmd, recipeValidation{Name: d.Name, Steps: len(d.Steps)})
	}})
	var reportPath string
	var timeout time.Duration
	run := &cobra.Command{Use: "run <file.yaml>", Short: "Execute steps sequentially, saving a report even on failure", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 {
			return output.Usagef("--step-timeout must be positive")
		}
		d, err := readRunbook(args[0])
		if err != nil {
			return err
		}
		e, err := openLab(ref)
		if err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(e.dir, "runbook.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fmt.Errorf("another runbook is running in this lab: %w", err)
		}
		path, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		accounts := map[string]any{}
		for _, acc := range e.cfg.Accounts {
			accounts[acc.Name] = map[string]any{"address": acc.Address}
		}
		values := map[string]any{"accounts": accounts, "chain_id": e.cfg.ChainID, "bond_denom": e.profile.BondDenom, "fee_denom": e.profile.FeeDenom}
		runner := runbook.Runner{Runtime: recipeRuntime{env: e}, Values: values, Dir: filepath.Dir(path), Env: append(os.Environ(), "FORKLAB_LAB_DIR="+e.dir, "FORKLAB_CHAIN_ID="+e.cfg.ChainID, "FORKLAB_BINARY="+e.bin), StepTimeout: timeout, OnStep: func(s runbook.StepResult) {
			status := "ok"
			if s.Error != "" {
				status = s.Error
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "step %d %s %s: %s\n", s.Index, s.ID, s.Action, status)
		}}
		destination := reportPath
		if destination == "" {
			destination = filepath.Join(e.dir, "runbooks", time.Now().UTC().Format("20060102T150405.000000000")+".json")
		}
		if info, err := os.Lstat(destination); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("report destination must be a regular file: %s", destination)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(destination), ".runbook-*")
		if err != nil {
			return err
		}
		defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
		report, runErr := runner.Run(cmd.Context(), d)
		if err := json.NewEncoder(f).Encode(report); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Rename(f.Name(), destination); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "report: %s\n", destination)
		if runErr != nil {
			if a.json {
				if err := output.PrintFailedJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
				return output.ExitStatus(1)
			}
			_ = report.WriteHuman(cmd.OutOrStdout())
			return runErr
		}
		return a.print(cmd, report)
	}}
	run.Flags().StringVar(&reportPath, "report", "", "report file (default: a timestamped file in the lab's runbooks directory)")
	run.Flags().DurationVar(&timeout, "step-timeout", time.Minute, "default time limit per step (overridden by YAML timeout)")
	cmd.AddCommand(run, newRunbookShowCmd(a), newRunbookWriteCmd(a))
	return cmd
}
