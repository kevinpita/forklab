package profile

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FieldError is a problem with one field, named by its path, such as
// binaries[9.0.0].build or fork_patches[1].
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// Errors is every problem found in a profile, one per line.
type Errors []FieldError

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// Decode strictly parses YAML into a Document. Unknown fields and type
// mismatches are errors that name the line.
func Decode(data []byte) (Document, error) {
	var d Document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		if errors.Is(err, io.EOF) {
			return d, errors.New("profile is empty")
		}
		return d, err
	}
	if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return d, errors.New("profile must be a single YAML document")
	}
	return d, nil
}

// Marshal renders a Document as YAML.
func (d Document) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
