// Package profile loads, validates, and stores chain profiles.
//
// A profile has two forms. Document is the YAML and JSON wire shape that users
// write and forklab stores. Profile is the validated, typed form the rest of
// forklab consumes; Document.Profile is the only way to build one.
package profile

import (
	"strings"
	"time"
)

// Document is a profile in its YAML shape. Fields are raw strings so that a
// round trip through edit keeps values as the user wrote them, such as 1m
// rather than 1m0s. Comments are not kept.
type Document struct {
	Name         string                    `yaml:"name" json:"name"`
	BinaryName   string                    `yaml:"binary_name" json:"binary_name"`
	ChainID      string                    `yaml:"chain_id" json:"chain_id"`
	Bech32Prefix string                    `yaml:"bech32_prefix" json:"bech32_prefix"`
	KeyAlgo      string                    `yaml:"key_algo" json:"key_algo"`
	BondDenom    string                    `yaml:"bond_denom" json:"bond_denom"`
	FeeDenom     string                    `yaml:"fee_denom" json:"fee_denom"`
	GasPrices    string                    `yaml:"gas_prices" json:"gas_prices"`
	BlockTime    string                    `yaml:"block_time" json:"block_time"`
	ExportArgs   []string                  `yaml:"export_args,omitempty" json:"export_args,omitempty"`
	ExtraPorts   map[string]map[string]int `yaml:"extra_ports,omitempty" json:"extra_ports,omitempty"`
	Gov          GovDocument               `yaml:"gov" json:"gov"`
	UpgradeName  string                    `yaml:"upgrade_name" json:"upgrade_name"`
	Binaries     map[string]BinaryDocument `yaml:"binaries" json:"binaries"`
	Snapshots    map[string]string         `yaml:"snapshots,omitempty" json:"snapshots,omitempty"`
	PatchesFile  string                    `yaml:"patches_file,omitempty" json:"patches_file,omitempty"`
	FreshPatches []string                  `yaml:"fresh_patches,omitempty" json:"fresh_patches,omitempty"`
	ForkPatches  []string                  `yaml:"fork_patches,omitempty" json:"fork_patches,omitempty"`
}

type GovDocument struct {
	VotingPeriod          string `yaml:"voting_period" json:"voting_period"`
	ExpeditedVotingPeriod string `yaml:"expedited_voting_period,omitempty" json:"expedited_voting_period,omitempty"`
}

// BinaryDocument sets exactly one of URL, Path, Git, Src. Ref, Build, Out,
// and Env belong to the source kinds that build.
type BinaryDocument struct {
	URL   string            `yaml:"url,omitempty" json:"url,omitempty"`
	Path  string            `yaml:"path,omitempty" json:"path,omitempty"`
	Git   string            `yaml:"git,omitempty" json:"git,omitempty"`
	Src   string            `yaml:"src,omitempty" json:"src,omitempty"`
	Ref   string            `yaml:"ref,omitempty" json:"ref,omitempty"`
	Build string            `yaml:"build,omitempty" json:"build,omitempty"`
	Out   string            `yaml:"out,omitempty" json:"out,omitempty"`
	Env   map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// Profile is a validated chain profile.
type Profile struct {
	Name         string
	BinaryName   string
	ChainID      string
	Bech32Prefix string
	KeyAlgo      string
	BondDenom    string
	FeeDenom     string
	GasPrices    Coin
	BlockTime    time.Duration
	ExportArgs   []Template
	// ExtraPorts maps a config file name to config keys and their base port.
	ExtraPorts map[string]map[string]int
	Gov        Gov
	// UpgradeName is the on-chain plan name for upgrading to a version.
	UpgradeName Template
	Binaries    map[string]Source
	Snapshots   map[string]Template
	// FreshPatches and ForkPatches include resolved file filters followed by
	// inline filters, applied last to fresh and forked genesis respectively.
	FreshPatches []string
	ForkPatches  []string
}

type Gov struct {
	VotingPeriod time.Duration
	// ExpeditedVotingPeriod is zero when the chain has no expedited proposals.
	ExpeditedVotingPeriod time.Duration
}

// Coin is a decimal amount of one denom, such as 0.025stake.
type Coin struct {
	Amount string
	Denom  string
}

func (c Coin) String() string { return c.Amount + c.Denom }

// Source is where a binary version comes from: URLSource, PathSource,
// GitSource, or SrcSource.
type Source interface {
	isSource()
}

// URLSource downloads a binary or an archive containing it.
type URLSource struct {
	URL Template
}

// PathSource uses an existing binary on disk.
type PathSource struct {
	Path Template
}

// GitSource clones Repo at Ref, runs Build in the checkout, and takes Out.
type GitSource struct {
	Repo  Template
	Ref   Template
	Build string
	Out   Template
	Env   map[string]string
}

// SrcSource runs Build in a local checkout at Dir and takes Out.
type SrcSource struct {
	Dir   Template
	Build string
	Out   Template
	Env   map[string]string
}

func (URLSource) isSource()  {}
func (PathSource) isSource() {}
func (GitSource) isSource()  {}
func (SrcSource) isSource()  {}

// Template is a string that may reference {version}, {os}, {Os}, {arch},
// and {chain_id}. {Os} is {os} capitalized, as in Linux or Darwin. Validation guarantees it references no other variable.
type Template string

// Vars are the values substituted into a Template.
type Vars struct {
	Version string
	OS      string
	Arch    string
	ChainID string
}

var templateVars = []string{"version", "os", "Os", "arch", "chain_id"}

func (t Template) Expand(v Vars) string {
	return strings.NewReplacer(
		"{version}", v.Version,
		"{os}", v.OS,
		"{Os}", capitalize(v.OS),
		"{arch}", v.Arch,
		"{chain_id}", v.ChainID,
	).Replace(string(t))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
