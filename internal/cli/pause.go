package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/control"
	"github.com/spf13/cobra"
)

type pauseView struct {
	Height int64  `json:"height"`
	Phase  string `json:"phase"`
}

func (v pauseView) WriteHuman(w io.Writer) error {
	if v.Phase == "resumed" {
		_, err := fmt.Fprintln(w, "chain resumed; block production verified")
		return err
	}
	_, err := fmt.Fprintf(w, "chain %s at committed height %d; queries remain available\n", v.Phase, v.Height)
	return err
}

func newLabPauseCmd(a *app) *cobra.Command {
	var height int64
	var timeout time.Duration
	cmd := &cobra.Command{Use: "pause <name>", Short: "Freeze at an exact committed height while keeping query RPC available", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if height < 1 || timeout <= 0 {
			return output.Usagef("--height and --timeout must be positive")
		}
		e, err := openLab(args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		r := recipeRuntime{env: e}
		c, err := r.controller(ctx)
		if err != nil {
			return err
		}
		st, err := c.Pause(ctx, height)
		if err != nil {
			return err
		}
		return a.print(cmd, pauseView{Height: st.Height, Phase: st.Phase})
	}}
	cmd.Flags().Int64Var(&height, "height", 0, "exact committed height to freeze (required)")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "time limit to reach and verify the pause")
	_ = cmd.MarkFlagRequired("height")
	return cmd
}

func newLabResumeCmd(a *app) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{Use: "resume <name>", Short: "Remove a pause barrier and verify block production resumes", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 {
			return output.Usagef("--timeout must be positive")
		}
		e, err := openLab(args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		if err := (recipeRuntime{env: e}).Resume(ctx); err != nil {
			return err
		}
		return a.print(cmd, pauseView{Phase: "resumed"})
	}}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "time limit to restart and observe a new commit")
	return cmd
}

type storeView struct{ chain.StoreResult }

func (v storeView) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "height: %d\nkey (hex): %s\nvalue (hex): %s\nvalue (base64): %s\n", v.Height, v.KeyHex, v.ValueHex, v.ValueBase64)
	return err
}

func newStoreCmd(a *app) *cobra.Command {
	var ref string
	var request chain.StoreRequest
	cmd := &cobra.Command{Use: "store <module> <hex-key>", Short: "Inspect raw KV store bytes, including while paused", Long: "Read a raw key through ABCI, or --prefix for a subspace query when the chain supports it.\nValues are bytes, without chain-specific protobuf decoding.", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		request.Name, request.KeyHex = args[0], args[1]
		if err := request.Validate(); err != nil {
			return output.Usage(err)
		}
		e, err := openLab(ref)
		if err != nil {
			return err
		}
		i, err := e.live(cmd.Context())
		if err != nil {
			return err
		}
		result, err := e.clients[i].Store(cmd.Context(), request)
		if err != nil {
			return err
		}
		return a.print(cmd, storeView{result})
	}}
	labRefFlag(cmd, &ref)
	cmd.Flags().Int64Var(&request.Height, "height", 0, "historical state height (0: latest, subject to pruning)")
	cmd.Flags().BoolVar(&request.Prefix, "prefix", false, "query all keys under this prefix when the chain supports subspace queries")
	return cmd
}

func requireUnpaused(dir string) error {
	st, err := control.Load(dir)
	if err != nil {
		return err
	}
	if st != nil {
		return fmt.Errorf("lab has a pause at %d (%s); use forklab lab resume instead", st.Height, st.Phase)
	}
	return nil
}
