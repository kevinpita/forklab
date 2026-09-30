// Package lab builds and reads labs: a directory of node homes, a shared
// test keyring, a supervisor nodes.json, and lab.yaml describing it all.
//
//	<labs>/<name>/
//	    lab.yaml
//	    nodes.json
//	    keys/            test keyring and mnemonics.json
//	    node0..N-1/      node home, node.log, node.pid
package lab

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
	"go.yaml.in/yaml/v3"
)

// ErrNotFound means no lab directory has the name.
var ErrNotFound = errors.New("lab not found")

type Mode string

const ModeFresh Mode = "fresh"

// Config is lab.yaml. Profile is a copy of the profile the lab was created
// with, so later edits to the profile do not change an existing lab.
type Config struct {
	Name       string           `yaml:"name" json:"name"`
	Mode       Mode             `yaml:"mode" json:"mode"`
	ChainID    string           `yaml:"chain_id" json:"chain_id"`
	Version    string           `yaml:"version" json:"version"`
	Validators int              `yaml:"validators" json:"validators"`
	CreatedAt  time.Time        `yaml:"created_at" json:"created_at"`
	Profile    profile.Document `yaml:"profile" json:"profile"`
	Nodes      []Node           `yaml:"nodes" json:"nodes"`
	Accounts   []Account        `yaml:"accounts" json:"accounts"`
}

// Node is one validator node. Its home is <lab>/<Name>.
type Node struct {
	Index int    `yaml:"index" json:"index"`
	Name  string `yaml:"name" json:"name"`
	// Version is the profile binary version the node runs.
	Version string `yaml:"version" json:"version"`
	NodeID  string `yaml:"node_id" json:"node_id"`
	// Validator is the key name of the node's operator account.
	Validator string `yaml:"validator" json:"validator"`
	Ports     []Port `yaml:"ports" json:"ports"`
}

// Port is a listen address the node owns: Key in the config File, such as
// rpc.laddr in config.toml.
type Port struct {
	File string `yaml:"file" json:"file"`
	Key  string `yaml:"key" json:"key"`
	Port int    `yaml:"port" json:"port"`
	// optional ports are set only when the chain's config file has the key.
	optional bool
}

// Account is a key in the lab keyring.
type Account struct {
	Name    string `yaml:"name" json:"name"`
	Address string `yaml:"address" json:"address"`
}

// Mnemonic is one entry of keys/mnemonics.json.
type Mnemonic struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Mnemonic string `json:"mnemonic"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// CheckName rejects names that are not one lowercase path segment.
func CheckName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a valid lab name (lowercase letters, digits, - and _)", name)
	}
	return nil
}

// Labs is the directory holding every lab, normally $FORKLAB_HOME/labs.
type Labs struct {
	Dir string
}

// Path is the directory of the lab named name, whether or not it exists.
func (l Labs) Path(name string) (string, error) {
	if err := CheckName(name); err != nil {
		return "", err
	}
	return filepath.Join(l.Dir, name), nil
}

const configFile = "lab.yaml"

func KeysDir(dir string) string          { return filepath.Join(dir, "keys") }
func MnemonicsPath(dir string) string    { return filepath.Join(KeysDir(dir), "mnemonics.json") }
func NodeHome(dir string, n Node) string { return filepath.Join(dir, n.Name) }
func nodeName(i int) string              { return fmt.Sprintf("node%d", i) }

func portIn(ports []Port, key string) int {
	for _, p := range ports {
		if p.Key == key {
			return p.Port
		}
	}
	return 0
}

// RPCPort is the node's CometBFT RPC port.
func (n Node) RPCPort() int { return portIn(n.Ports, "rpc.laddr") }

// Get loads the lab named name.
func (l Labs) Get(name string) (Config, string, error) {
	dir, err := l.Path(name)
	if err != nil {
		return Config{}, "", err
	}
	c, err := Load(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, "", fmt.Errorf("lab %s: %w", name, ErrNotFound)
	}
	return c, dir, err
}

// Load reads <dir>/lab.yaml. Unknown fields are errors.
func Load(dir string) (Config, error) {
	path := filepath.Join(dir, configFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Nodes) != c.Validators {
		return Config{}, fmt.Errorf("%s: %d validators but %d nodes", path, c.Validators, len(c.Nodes))
	}
	return c, nil
}

// Save writes <dir>/lab.yaml.
func Save(dir string, c Config) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, configFile), data, 0o644)
}

// Listing is one row of List. Error is set when lab.yaml cannot be read.
type Listing struct {
	Name   string  `json:"name"`
	Dir    string  `json:"dir"`
	Config *Config `json:"config,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// List returns every lab sorted by name, skipping directories being built.
func (l Labs) List() ([]Listing, error) {
	entries, err := os.ReadDir(l.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Listing
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), partialSuffix) || CheckName(e.Name()) != nil {
			continue
		}
		row := Listing{Name: e.Name(), Dir: filepath.Join(l.Dir, e.Name())}
		if c, err := Load(row.Dir); err != nil {
			row.Error = err.Error()
		} else {
			row.Config = &c
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b Listing) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ErrRunning means the lab's supervisor answers or one of its nodes runs.
var ErrRunning = errors.New("lab is running")

// Delete removes the lab named name and returns its directory. A lab whose
// supervisor answers, or whose nodes outlived a killed supervisor, is
// refused. A broken lab.yaml does not stop it.
func (l Labs) Delete(name string) (string, error) {
	dir, err := l.Path(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("lab %s: %w", name, ErrNotFound)
	} else if err != nil {
		return "", err
	}
	if _, err := supervisor.Dial(dir); err == nil {
		return "", fmt.Errorf("lab %s: %w", name, ErrRunning)
	}
	live, err := supervisor.LiveNodes(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("lab %s: %w", name, err)
	}
	if len(live) > 0 {
		return "", fmt.Errorf("lab %s: %w (nodes %v have no supervisor)", name, ErrRunning, live)
	}
	return dir, os.RemoveAll(dir)
}
