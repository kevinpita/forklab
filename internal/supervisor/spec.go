// Package supervisor runs a lab's node processes from a detached daemon that
// serves a JSON API over a unix socket in the lab directory.
package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// NodeSpec describes how to run one node. Binary is an absolute path; Args
// are passed verbatim, so they must include the chain's start subcommand and
// --home. Home must appear in Args: adoption matches a live pid on it.
type NodeSpec struct {
	Index   int      `json:"index"`
	Name    string   `json:"name"`
	Binary  string   `json:"binary"`
	Args    []string `json:"args"`
	Home    string   `json:"home"`
	LogPath string   `json:"log_path"`
	PidPath string   `json:"pid_path"`
}

// Paths locates the supervisor's files in a lab directory.
type Paths struct{ Dir string }

func (p Paths) Nodes() string { return filepath.Join(p.Dir, "nodes.json") }
func (p Paths) Lock() string  { return filepath.Join(p.Dir, "supervisor.lock") }
func (p Paths) Sock() string  { return filepath.Join(p.Dir, "supervisor.sock") }
func (p Paths) Log() string   { return filepath.Join(p.Dir, "supervisor.log") }

// LoadNodes reads nodes.json. Indexes must be 0..n-1 in order.
func LoadNodes(labDir string) ([]NodeSpec, error) {
	path := Paths{labDir}.Nodes()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var specs []NodeSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, s := range specs {
		switch {
		case s.Index != i:
			return nil, fmt.Errorf("%s: node %d has index %d", path, i, s.Index)
		case s.Binary == "" || s.Home == "" || s.LogPath == "" || s.PidPath == "":
			return nil, fmt.Errorf("%s: node %d needs binary, home, log_path, and pid_path", path, i)
		case !slices.Contains(s.Args, s.Home):
			return nil, fmt.Errorf("%s: node %d args do not mention its home %s", path, i, s.Home)
		}
	}
	if len(specs) == 0 {
		return nil, errors.New(path + ": no nodes")
	}
	return specs, nil
}

// SaveNodes writes nodes.json atomically.
func SaveNodes(labDir string, specs []NodeSpec) error {
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(Paths{labDir}.Nodes(), append(data, '\n'))
}
