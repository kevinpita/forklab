package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/spf13/cobra"
)

type proposalDocument struct{ chain.ProposalFile }

func (d proposalDocument) WriteHuman(w io.Writer) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(d.ProposalFile)
}

type proposalSaved struct {
	Path string `json:"path"`
}

func (s proposalSaved) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintln(w, "Saved", s.Path)
	return err
}

func writeProposalFile(path string, value any, force bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if force {
		f, err := os.CreateTemp(filepath.Dir(path), ".proposal-*")
		if err != nil {
			return err
		}
		defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
		if _, err = f.Write(data); err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		return os.Rename(f.Name(), path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("save proposal: %w (use --force to overwrite)", err)
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func newGovWriteCmd(a *app) *cobra.Command {
	var document string
	var force bool
	cmd := &cobra.Command{Use: "write <file.json>", Short: "Validate and save a proposal draft without submitting", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		d, err := chain.ParseProposalDocument([]byte(document))
		if err != nil {
			return output.Usage(err)
		}
		if err = writeProposalFile(args[0], d, force); err != nil {
			return err
		}
		return a.print(cmd, proposalSaved{args[0]})
	}}
	cmd.Flags().StringVar(&document, "document", "", "submission document as JSON (required)")
	_ = cmd.MarkFlagRequired("document")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing file atomically")
	return cmd
}

func newGovFileCmd(a *app, ref *string, clone bool) *cobra.Command {
	var force bool
	use, short := "export <id> <file.json>", "Save a proposal record with its tally"
	count := 2
	if clone {
		use, short, count = "draft <id>", "Clone a proposal as an editable submission document", 1
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := proposalID(args[0])
		if err != nil {
			return err
		}
		e, err := openLab(*ref)
		if err != nil {
			return err
		}
		c, _, err := e.liveCLI(cmd.Context())
		if err != nil {
			return err
		}
		p, err := c.Proposal(cmd.Context(), id)
		if err != nil {
			return err
		}
		if clone {
			deposit, err := minDeposit(cmd.Context(), c, p.Expedited)
			if err != nil {
				return err
			}
			d, err := p.Draft(deposit)
			if err != nil {
				return err
			}
			return a.print(cmd, proposalDocument{d})
		}
		v := proposalView{Proposal: p, Tally: p.FinalTally}
		if p.Status == "VOTING_PERIOD" {
			v.Tally, err = c.Tally(cmd.Context(), id)
			if err != nil {
				return err
			}
		}
		if err = writeProposalFile(args[1], v, force); err != nil {
			return err
		}
		return a.print(cmd, proposalSaved{args[1]})
	}}
	if !clone {
		cmd.Flags().BoolVar(&force, "force", false, "replace an existing file atomically")
	}
	return cmd
}
