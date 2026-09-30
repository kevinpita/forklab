package profile

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/itchyny/gojq"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	keyRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	prefixRe  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	chainIDRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,50}$`)
	denomRe   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9/:._-]{2,127}$`)
	coinRe    = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([a-zA-Z][a-zA-Z0-9/:._-]{2,127})$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	varRe     = regexp.MustCompile(`\{([^{}]*)\}`)
)

var configFiles = []string{"app.toml", "client.toml", "config.toml"}

type checker struct {
	errs Errors
}

func (c *checker) fail(path, format string, args ...any) {
	c.errs = append(c.errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (c *checker) required(path, v string) bool {
	if v == "" {
		c.fail(path, "required")
		return false
	}
	return true
}

func (c *checker) text(path, v string) string {
	c.required(path, v)
	return v
}

func (c *checker) match(path, v string, re *regexp.Regexp, what string) string {
	if c.required(path, v) && !re.MatchString(v) {
		c.fail(path, "%q is not a valid %s", v, what)
	}
	return v
}

func (c *checker) duration(path, v string) time.Duration {
	if !c.required(path, v) {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		c.fail(path, "%q is not a positive duration such as 1s or 500ms", v)
		return 0
	}
	return d
}

func (c *checker) template(path, v string) Template {
	for _, m := range varRe.FindAllStringSubmatch(v, -1) {
		if !slices.Contains(templateVars, m[1]) {
			c.fail(path, "unknown template variable {%s}; known: {%s}", m[1], strings.Join(templateVars, "} {"))
		}
	}
	return Template(v)
}

// validate checks the document's inline fields without resolving patch files.
func (d Document) validate() (Profile, error) {
	var c checker
	p := Profile{
		Name:         c.match("name", d.Name, nameRe, "name (lowercase letters, digits, - and _)"),
		BinaryName:   c.match("binary_name", d.BinaryName, keyRe, "binary name"),
		ChainID:      c.match("chain_id", d.ChainID, chainIDRe, "chain ID (up to 50 letters, digits, _ . -)"),
		Bech32Prefix: c.match("bech32_prefix", d.Bech32Prefix, prefixRe, "bech32 prefix"),
		KeyAlgo:      c.text("key_algo", d.KeyAlgo),
		BondDenom:    c.match("bond_denom", d.BondDenom, denomRe, "denom"),
		FeeDenom:     c.match("fee_denom", d.FeeDenom, denomRe, "denom"),
		GasPrices:    c.gasPrices(d.GasPrices, d.FeeDenom),
		BlockTime:    c.duration("block_time", d.BlockTime),
		ExtraPorts:   d.ExtraPorts,
		FreshPatches: d.FreshPatches,
		ForkPatches:  d.ForkPatches,
	}

	for i, a := range d.ExportArgs {
		p.ExportArgs = append(p.ExportArgs, c.template(fmt.Sprintf("export_args[%d]", i), a))
	}

	for _, file := range slices.Sorted(maps.Keys(d.ExtraPorts)) {
		path := fmt.Sprintf("extra_ports[%s]", file)
		if !slices.Contains(configFiles, file) {
			c.fail(path, "unknown config file; known: %s", strings.Join(configFiles, ", "))
		}
		for _, key := range slices.Sorted(maps.Keys(d.ExtraPorts[file])) {
			if port := d.ExtraPorts[file][key]; port < 1 || port > 65535 {
				c.fail(fmt.Sprintf("%s[%s]", path, key), "port %d is outside 1-65535", port)
			}
		}
	}

	p.Gov.VotingPeriod = c.duration("gov.voting_period", d.Gov.VotingPeriod)
	if d.Gov.ExpeditedVotingPeriod != "" {
		p.Gov.ExpeditedVotingPeriod = c.duration("gov.expedited_voting_period", d.Gov.ExpeditedVotingPeriod)
		if p.Gov.ExpeditedVotingPeriod > 0 && p.Gov.VotingPeriod > 0 && p.Gov.ExpeditedVotingPeriod >= p.Gov.VotingPeriod {
			c.fail("gov.expedited_voting_period", "must be shorter than gov.voting_period")
		}
	}

	if c.required("upgrade_name", d.UpgradeName) {
		p.UpgradeName = c.template("upgrade_name", d.UpgradeName)
	}

	if len(d.Binaries) == 0 {
		c.fail("binaries", "at least one binary version is required")
	}
	p.Binaries = make(map[string]Source, len(d.Binaries))
	for _, v := range slices.Sorted(maps.Keys(d.Binaries)) {
		path := fmt.Sprintf("binaries[%s]", v)
		if !keyRe.MatchString(v) {
			c.fail(path, "%q is not a valid version (letters, digits, . _ + -)", v)
		}
		if s := c.source(path, d.Binaries[v]); s != nil {
			p.Binaries[v] = s
		}
	}

	p.Snapshots = make(map[string]Template, len(d.Snapshots))
	for _, name := range slices.Sorted(maps.Keys(d.Snapshots)) {
		path := fmt.Sprintf("snapshots[%s]", name)
		if !keyRe.MatchString(name) {
			c.fail(path, "%q is not a valid snapshot name", name)
		}
		if c.required(path, d.Snapshots[name]) {
			p.Snapshots[name] = c.template(path, d.Snapshots[name])
		}
	}

	c.patches("fresh_patches", d.FreshPatches)
	c.patches("fork_patches", d.ForkPatches)

	if len(c.errs) > 0 {
		return Profile{}, c.errs
	}
	return p, nil
}

func (c *checker) patches(path string, patches []string) {
	for i, patch := range patches {
		if _, err := gojq.Parse(patch); err != nil {
			c.fail(fmt.Sprintf("%s[%d]", path, i), "invalid jq: %v", err)
		}
	}
}

func (c *checker) gasPrices(v, feeDenom string) Coin {
	if !c.required("gas_prices", v) {
		return Coin{}
	}
	m := coinRe.FindStringSubmatch(v)
	if m == nil {
		c.fail("gas_prices", "%q is not a coin such as 0.025stake", v)
		return Coin{}
	}
	if feeDenom != "" && m[2] != feeDenom {
		c.fail("gas_prices", "denom %q does not match fee_denom %q", m[2], feeDenom)
	}
	return Coin{Amount: m[1], Denom: m[2]}
}

func (c *checker) source(path string, b BinaryDocument) Source {
	var kinds []string
	for _, k := range []struct{ name, value string }{{"url", b.URL}, {"path", b.Path}, {"git", b.Git}, {"src", b.Src}} {
		if k.value != "" {
			kinds = append(kinds, k.name)
		}
	}
	if len(kinds) != 1 {
		got := "none"
		if len(kinds) > 0 {
			got = strings.Join(kinds, ", ")
		}
		c.fail(path, "exactly one of url, path, git, src is required (got %s)", got)
		return nil
	}
	kind := kinds[0]
	builds := kind == "git" || kind == "src"
	notFor := func(field, value string) {
		if value != "" {
			c.fail(path+"."+field, "not valid with %s", kind)
		}
	}
	if !builds {
		notFor("build", b.Build)
		notFor("out", b.Out)
		if len(b.Env) > 0 {
			c.fail(path+".env", "not valid with %s", kind)
		}
	}
	if kind != "git" {
		notFor("ref", b.Ref)
	}
	for _, k := range slices.Sorted(maps.Keys(b.Env)) {
		if !envKeyRe.MatchString(k) {
			c.fail(fmt.Sprintf("%s.env[%s]", path, k), "%q is not a valid environment variable name", k)
		}
	}

	switch kind {
	case "url":
		return URLSource{URL: c.template(path+".url", b.URL)}
	case "path":
		return PathSource{Path: c.template(path+".path", b.Path)}
	case "git":
		c.required(path+".ref", b.Ref)
		c.required(path+".build", b.Build)
		c.required(path+".out", b.Out)
		return GitSource{
			Repo:  c.template(path+".git", b.Git),
			Ref:   c.template(path+".ref", b.Ref),
			Build: b.Build,
			Out:   c.template(path+".out", b.Out),
			Env:   b.Env,
		}
	default:
		c.required(path+".build", b.Build)
		c.required(path+".out", b.Out)
		return SrcSource{
			Dir:   c.template(path+".src", b.Src),
			Build: b.Build,
			Out:   c.template(path+".out", b.Out),
			Env:   b.Env,
		}
	}
}
