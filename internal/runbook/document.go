// Package runbook executes chain-independent transaction and inspection recipes.
package runbook

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/itchyny/gojq"
	"github.com/kevinpita/forklab/internal/chain"
	"go.yaml.in/yaml/v3"
)

// Document uses argv lists for commands: no implicit shell parsing.
type Document struct {
	Version int            `yaml:"version" json:"version"`
	Name    string         `yaml:"name,omitempty" json:"name,omitempty"`
	Vars    map[string]any `yaml:"vars,omitempty" json:"vars,omitempty"`
	Steps   []Step         `yaml:"steps" json:"steps"`
}

type Step struct {
	ID         string              `yaml:"id,omitempty" json:"id,omitempty"`
	AtHeight   int64               `yaml:"at_height,omitempty" json:"at_height,omitempty"`
	Timeout    string              `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Tx         *Tx                 `yaml:"tx,omitempty" json:"tx,omitempty"`
	Query      []string            `yaml:"query,omitempty" json:"query,omitempty"`
	Assert     string              `yaml:"assert,omitempty" json:"assert,omitempty"`
	WaitHeight *int64              `yaml:"wait_height,omitempty" json:"wait_height,omitempty"`
	Pause      *int64              `yaml:"pause,omitempty" json:"pause,omitempty"`
	Hold       bool                `yaml:"hold,omitempty" json:"hold,omitempty"`
	Resume     bool                `yaml:"resume,omitempty" json:"resume,omitempty"`
	Store      *chain.StoreRequest `yaml:"store,omitempty" json:"store,omitempty"`
	Script     []string            `yaml:"script,omitempty" json:"script,omitempty"`
}

type Tx struct {
	From       string   `yaml:"from" json:"from"`
	Args       []string `yaml:"args" json:"args"`
	ExpectCode uint32   `yaml:"expect_code,omitempty" json:"expect_code,omitempty"`
}

func Decode(r io.Reader) (Document, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var d Document
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return d, fmt.Errorf("runbook must contain one YAML document")
	}
	return d, d.Validate()
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (d Document) Validate() error {
	if d.Version != 1 {
		return fmt.Errorf("runbook version must be 1")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("runbook needs at least one step")
	}
	ids := map[string]bool{}
	for i, s := range d.Steps {
		if s.ID != "" {
			if !identifier.MatchString(s.ID) || ids[s.ID] {
				return fmt.Errorf("step %d: id must be unique and contain letters, numbers, or underscores", i+1)
			}
			ids[s.ID] = true
		}
		if s.AtHeight < 0 {
			return fmt.Errorf("step %d: at_height must not be negative", i+1)
		}
		if s.Timeout != "" {
			if t, err := time.ParseDuration(s.Timeout); err != nil || t <= 0 {
				return fmt.Errorf("step %d: timeout must be a positive duration", i+1)
			}
		}
		actions := []bool{s.Tx != nil, s.Query != nil, s.Assert != "", s.WaitHeight != nil, s.Pause != nil, s.Hold, s.Resume, s.Store != nil, s.Script != nil}
		n := 0
		for _, set := range actions {
			if set {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("step %d: specify exactly one action", i+1)
		}
		if s.Tx != nil && (s.Tx.From == "" || len(s.Tx.Args) == 0) {
			return fmt.Errorf("step %d: tx requires from and args", i+1)
		}
		if s.Query != nil && len(s.Query) == 0 || s.Script != nil && len(s.Script) == 0 {
			return fmt.Errorf("step %d: command argv must not be empty", i+1)
		}
		if s.WaitHeight != nil && *s.WaitHeight < 1 || s.Pause != nil && *s.Pause < 1 {
			return fmt.Errorf("step %d: height must be positive", i+1)
		}
		if s.Store != nil {
			request := *s.Store
			if strings.Contains(request.Name, "{{") {
				request.Name = "store"
			}
			if strings.Contains(request.KeyHex, "{{") {
				request.KeyHex = "00"
			}
			if err := request.Validate(); err != nil {
				return fmt.Errorf("step %d store: %w", i+1, err)
			}
		}
		if s.Assert != "" {
			q, err := gojq.Parse(s.Assert)
			if err == nil {
				_, err = gojq.Compile(q)
			}
			if err != nil {
				return fmt.Errorf("step %d assertion: %w", i+1, err)
			}
		}
	}
	return nil
}
