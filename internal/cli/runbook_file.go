package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/runbook"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

type recipeDocument struct{ runbook.Document }

func (v recipeDocument) WriteHuman(w io.Writer) error { return yaml.NewEncoder(w).Encode(v.Document) }

func newRunbookShowCmd(a *app) *cobra.Command {
	return &cobra.Command{Use: "show <file.yaml>", Short: "Read a runbook as YAML or JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		d, err := readRunbook(args[0])
		if err != nil {
			return err
		}
		return a.print(cmd, recipeDocument{d})
	}}
}

func newRunbookWriteCmd(a *app) *cobra.Command {
	var document string
	var force bool
	cmd := &cobra.Command{Use: "write <file.yaml>", Short: "Validate a JSON document and save it as YAML (used by the visual builder)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		dec := json.NewDecoder(bytes.NewBufferString(document))
		dec.DisallowUnknownFields()
		var d runbook.Document
		if err := dec.Decode(&d); err != nil {
			return output.Usage(err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return output.Usagef("expected one JSON document")
		}
		// YAML's integer decoder preserves numeric variables beyond float64 precision.
		if err := yaml.Unmarshal([]byte(document), &d); err != nil {
			return output.Usage(err)
		}
		if err := d.Validate(); err != nil {
			return output.Usage(err)
		}
		data, err := yaml.Marshal(d)
		if err != nil {
			return err
		}
		path := args[0]
		if force {
			f, err := os.CreateTemp(filepath.Dir(path), ".runbook-*")
			if err != nil {
				return err
			}
			defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
			if _, err := f.Write(data); err != nil {
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if err := os.Rename(f.Name(), path); err != nil {
				return err
			}
		} else {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return fmt.Errorf("save runbook: %w (use --force to overwrite)", err)
			}
			_, writeErr := f.Write(data)
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return a.print(cmd, recipeValidation{Name: d.Name, Steps: len(d.Steps)})
	}}
	cmd.Flags().StringVar(&document, "document", "", "runbook document as JSON (required)")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing recipe")
	_ = cmd.MarkFlagRequired("document")
	return cmd
}
