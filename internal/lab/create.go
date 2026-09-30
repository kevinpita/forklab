package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kevinpita/forklab/internal/genesis"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/supervisor"
)

// CreateInput describes a lab to build.
type CreateInput struct {
	Name    string
	Profile profile.Entry
	Version string
	// Binary resolves the chain binary path. It runs after the cheap checks,
	// since it may download or build.
	Binary       func(context.Context) (string, error)
	Validators   int
	TestAccounts int
	// ChainID is the lab chain ID. A fork export carries whatever chain ID
	// its scratch home was initialised with, so the lab sets it explicitly
	// in both modes.
	ChainID string
	// Fork forks exported chain state instead of building a fresh genesis.
	Fork *ForkInput
}

// ForkInput is the chain state a fork lab takes over.
type ForkInput struct {
	// Source is the --fork argument as given.
	Source string
	// Export fetches the snapshot and exports its state with the chain
	// binary, returning the archive and the exported genesis path. It runs
	// after Binary, since it may download and export gigabytes.
	Export func(ctx context.Context, bin string) (archive, exported string, err error)
}

// Genesis amounts. The stake gives every validator real voting power for a
// power reduction from 1e6 up to 1e18 while the total stays far below
// CometBFT's voting power limit; balances cover any lab fees. The fork gov
// delegation only has to exist: the gov account controls the chain through
// the boost delegations Takeover injects.
var (
	validatorStake    = pow10(21)
	bondBalance       = pow10(22)
	feeBalance        = pow10(27)
	forkGovDelegation = pow10(6)
)

func pow10(n int64) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil) }

const partialSuffix = ".partial"

// Create builds a fresh lab in <labs>/<name>.partial and renames it into
// place once complete, so <labs>/<name> exists only as a whole lab. A
// failed build leaves the partial directory and its create.log for
// inspection; the next Create of that name starts over.
func (l Labs) Create(ctx context.Context, in CreateInput) (Config, string, error) {
	dir, err := l.Path(in.Name)
	if err != nil {
		return Config{}, "", err
	}
	if _, err := os.Stat(dir); err == nil {
		return Config{}, "", fmt.Errorf("lab %s already exists at %s", in.Name, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Config{}, "", err
	}
	if err := (supervisor.Paths{Dir: dir}).CheckSock(); err != nil {
		return Config{}, "", fmt.Errorf("lab %s: %w (or set FORKLAB_HOME to a shorter path)", in.Name, err)
	}
	if in.Validators < 1 {
		return Config{}, "", fmt.Errorf("lab %s: need at least one validator", in.Name)
	}
	ports, err := allocatePorts(in.Validators, in.Profile.Profile.ExtraPorts)
	if err != nil {
		return Config{}, "", fmt.Errorf("lab %s: %w", in.Name, err)
	}
	if err := checkFree(ports); err != nil {
		return Config{}, "", fmt.Errorf("lab %s: %w", in.Name, err)
	}
	bin, err := in.Binary(ctx)
	if err != nil {
		return Config{}, "", err
	}
	var exported *exportedState
	if in.Fork != nil {
		if exported, err = loadExport(ctx, bin, *in.Fork); err != nil {
			return Config{}, "", fmt.Errorf("lab %s: %w", in.Name, err)
		}
	}

	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return Config{}, "", err
	}
	unlock, err := lockName(filepath.Join(l.Dir, "."+in.Name+".lock"))
	if err != nil {
		return Config{}, "", fmt.Errorf("lab %s: %w", in.Name, err)
	}
	defer unlock()
	partial := dir + partialSuffix
	if err := os.RemoveAll(partial); err != nil {
		return Config{}, "", err
	}
	if err := os.Mkdir(partial, 0o755); err != nil {
		return Config{}, "", err
	}
	logPath := filepath.Join(partial, "create.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Config{}, "", err
	}
	defer func() { _ = logFile.Close() }()

	b := builder{in: in, p: in.Profile.Profile, dir: dir, partial: partial, ports: ports, exported: exported, cli: chainCLI{ctx: ctx, bin: bin, log: logFile}}
	c, err := b.build()
	if err != nil {
		return Config{}, "", fmt.Errorf("lab %s: %w (log: %s)", in.Name, err, logPath)
	}
	if err := os.Rename(partial, dir); err != nil {
		return Config{}, "", err
	}
	return c, dir, nil
}

// lockName holds an exclusive flock so two creates of one name cannot
// share the partial directory. Unlock removes the file. A process that
// opened it before the removal ends up locking an unlinked file, so the
// lock only counts while path still names the locked file.
func lockName(path string) (func(), error) {
	busy := errors.New("another forklab process is creating this lab")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, busy
	}
	held, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if named, err := os.Stat(path); err != nil || !os.SameFile(held, named) {
		_ = f.Close()
		return nil, busy
	}
	return func() {
		_ = os.Remove(path)
		_ = f.Close()
	}, nil
}

// builder runs the recipe inside partial. Paths written into files that
// outlive the build (nodes.json) use the final dir.
type builder struct {
	in      CreateInput
	p       profile.Profile
	dir     string
	partial string
	ports   [][]Port
	// exported is the parsed chain state in fork mode.
	exported *exportedState
	cli      chainCLI
}

// exportedState is the exported genesis a fork lab takes over.
type exportedState struct {
	genesis *genesis.Genesis
	info    Fork
}

// loadExport runs the fork export and parses its result.
func loadExport(ctx context.Context, bin string, in ForkInput) (*exportedState, error) {
	archive, path, err := in.Export(ctx, bin)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	g, err := genesis.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("exported genesis %s: %w", path, err)
	}
	height, err := g.InitialHeight()
	if err != nil {
		return nil, fmt.Errorf("exported genesis %s: %w", path, err)
	}
	return &exportedState{genesis: g, info: Fork{Source: in.Source, Archive: archive, Height: height.Int64()}}, nil
}

func (b *builder) home(i int) string { return filepath.Join(b.partial, nodeName(i)) }
func (b *builder) keys() string      { return KeysDir(b.partial) }

func (b *builder) build() (Config, error) {
	c := Config{
		Name:       b.in.Name,
		Mode:       ModeFresh,
		ChainID:    b.in.ChainID,
		Version:    b.in.Version,
		Validators: b.in.Validators,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
		Profile:    b.in.Profile.Doc,
	}
	if b.exported != nil {
		c.Mode = ModeFork
		c.Fork = &b.exported.info
	}
	for i := range b.in.Validators {
		if _, err := b.cli.run("init "+nodeName(i), "init", nodeName(i), "--chain-id", c.ChainID, "--home", b.home(i)); err != nil {
			return Config{}, err
		}
		id, err := nodeID(b.home(i))
		if err != nil {
			return Config{}, err
		}
		n := Node{Index: i, Name: nodeName(i), Version: b.in.Version, NodeID: id}
		if c.Mode == ModeFresh {
			n.Validator = fmt.Sprintf("val%d", i)
		}
		c.Nodes = append(c.Nodes, n)
	}

	mnemonics, err := b.addKeys(c.Mode)
	if err != nil {
		return Config{}, err
	}
	for _, m := range mnemonics {
		c.Accounts = append(c.Accounts, Account{Name: m.Name, Address: m.Address})
	}

	var g *genesis.Genesis
	patches := b.p.FreshPatches
	if c.Mode == ModeFork {
		g, err = b.forkGenesis(c)
		patches = b.p.ForkPatches
	} else {
		g, err = b.freshGenesis(c)
	}
	if err != nil {
		return Config{}, err
	}
	if err := b.writeGenesis(g, string(c.Mode)+"_patches", patches); err != nil {
		return Config{}, err
	}
	if err := b.configure(c.Nodes); err != nil {
		return Config{}, err
	}
	if err := supervisor.SaveNodes(b.partial, b.nodeSpecs(c.Nodes)); err != nil {
		return Config{}, err
	}
	return c, Save(b.partial, c)
}

// addKeys creates every lab key in the shared test keyring and saves the
// mnemonics next to it. Fresh validators get an operator key each; fork
// validators keep their mainnet operators.
func (b *builder) addKeys(mode Mode) ([]Mnemonic, error) {
	if err := os.Mkdir(b.keys(), 0o700); err != nil {
		return nil, err
	}
	algoFlag := "--key-type"
	if !b.cli.helpHas([]string{"keys", "add"}, "--key-type") {
		algoFlag = "--algo"
	}
	var names []string
	if mode == ModeFresh {
		for i := range b.in.Validators {
			names = append(names, fmt.Sprintf("val%d", i))
		}
	}
	names = append(names, "gov")
	for i := range b.in.TestAccounts {
		names = append(names, fmt.Sprintf("test%d", i))
	}
	var out []Mnemonic
	for _, name := range names {
		res, err := b.cli.runSecret("add key "+name, "keys", "add", name, "--keyring-backend", "test", "--keyring-dir", b.keys(),
			"--home", b.home(0), "--output", "json", algoFlag, b.p.KeyAlgo)
		if err != nil {
			return nil, err
		}
		var m Mnemonic
		// Older SDKs print the key JSON on stderr.
		raw := res.stdout
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = res.stderr
		}
		if err := json.Unmarshal(raw, &m); err != nil || m.Address == "" || m.Mnemonic == "" {
			return nil, fmt.Errorf("add key %s: keys add printed no key JSON", name)
		}
		m.Name = name
		out = append(out, m)
	}
	_, _ = fmt.Fprintf(b.cli.log, "keys added: %s\n", strings.Join(names, ", "))
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, os.WriteFile(MnemonicsPath(b.partial), append(data, '\n'), 0o600)
}

func (b *builder) genesisPath(i int) string {
	return filepath.Join(b.home(i), "config", "genesis.json")
}

// freshGenesis funds every account, collects one gentx per validator (each
// signed in its own node home, so it carries that node's consensus key),
// then sets the profile bond denom, which chain init defaults to stake.
func (b *builder) freshGenesis(c Config) (*genesis.Genesis, error) {
	coins := genesisCoins(b.p.BondDenom, b.p.FeeDenom)
	for _, a := range c.Accounts {
		if _, err := b.cli.run("fund "+a.Name, "genesis", "add-genesis-account", a.Address, coins, "--home", b.home(0)); err != nil {
			return nil, err
		}
	}
	funded, err := os.ReadFile(b.genesisPath(0))
	if err != nil {
		return nil, err
	}
	gentxDir := filepath.Join(b.home(0), "config", "gentx")
	if err := os.MkdirAll(gentxDir, 0o755); err != nil {
		return nil, err
	}
	stake := validatorStake.String() + b.p.BondDenom
	for i, n := range c.Nodes {
		if i > 0 {
			if err := os.WriteFile(b.genesisPath(i), funded, 0o644); err != nil {
				return nil, err
			}
		}
		if _, err := b.cli.run("gentx "+n.Validator, "genesis", "gentx", n.Validator, stake, "--home", b.home(i), "--chain-id", c.ChainID,
			"--keyring-backend", "test", "--keyring-dir", b.keys(), "--gas-prices", "0"+b.p.FeeDenom,
			"--output-document", filepath.Join(gentxDir, n.Validator+".json")); err != nil {
			return nil, err
		}
	}
	if _, err := b.cli.run("collect gentxs", "genesis", "collect-gentxs", "--home", b.home(0), "--gentx-dir", gentxDir); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(b.genesisPath(0))
	if err != nil {
		return nil, err
	}
	g, err := genesis.Parse(data)
	if err != nil {
		return nil, err
	}
	return g, genesis.ApplyPatches(g, []string{".app_state.staking.params.bond_denom = " + strconv.Quote(b.p.BondDenom)})
}

// forkGenesis hands the exported state's largest validators to the lab
// nodes and funds the lab accounts, with the gov account owning the
// controlling delegations. It records each node's mainnet operator.
func (b *builder) forkGenesis(c Config) (*genesis.Genesis, error) {
	g := b.exported.genesis
	bondDenom, err := g.BondDenom()
	if err != nil {
		return nil, err
	}
	var keys []genesis.NodeKey
	for i := range c.Nodes {
		key, err := ReadConsensusKey(b.home(i))
		if err != nil {
			return nil, err
		}
		keys = append(keys, genesis.NodeKey{PubKey: key.PubKey})
	}
	supply, err := g.Supply(bondDenom)
	if err != nil {
		return nil, err
	}
	// Injected bond-denom balances stay a small fraction of the existing
	// supply, so the fork keeps the chain's economics. When the bond denom
	// also pays fees the share is larger to leave room for lab transactions.
	share := big.NewInt(10_000)
	if b.p.FeeDenom == bondDenom {
		share = big.NewInt(100)
	}
	bond := new(big.Int).Quo(supply, share)
	if bond.Sign() == 0 {
		bond = big.NewInt(1)
	}
	balances := []genesis.Coin{{Denom: bondDenom, Amount: bond}}
	if b.p.FeeDenom != bondDenom {
		balances = append(balances, genesis.Coin{Denom: b.p.FeeDenom, Amount: feeBalance})
	}
	var accounts []genesis.Account
	var gov string
	for _, a := range c.Accounts {
		accounts = append(accounts, genesis.Account{Address: a.Address, Balances: balances})
		if a.Name == "gov" {
			gov = a.Address
		}
	}
	report, err := genesis.Takeover(g, genesis.TakeoverInput{
		Validators:   keys,
		Accounts:     accounts,
		GovDelegator: &genesis.GovDelegator{Address: gov, Amount: forkGovDelegation},
		ChainID:      b.in.ChainID,
	})
	if err != nil {
		return nil, err
	}
	for i, v := range report.Validators {
		c.Nodes[i].Operator = v.Operator
		_, _ = fmt.Fprintf(b.cli.log, "%s takes over %s with power %s of %s\n", c.Nodes[i].Name, v.Operator, v.Power, report.TotalPower)
	}
	return g, nil
}

// writeGenesis applies the gov patch and the profile's mode patches, then
// installs the result in every node and validates it.
func (b *builder) writeGenesis(g *genesis.Genesis, patchName string, patches []string) error {
	if err := genesis.GovPatch(g, genesis.GovParams{
		VotingPeriod:          b.p.Gov.VotingPeriod,
		ExpeditedVotingPeriod: b.p.Gov.ExpeditedVotingPeriod,
		MinDeposit:            genesis.Coin{Denom: b.p.FeeDenom, Amount: big.NewInt(1)},
	}); err != nil {
		return err
	}
	if err := genesis.ApplyPatches(g, patches); err != nil {
		return fmt.Errorf("%s: %w", patchName, err)
	}
	data, err := g.Bytes()
	if err != nil {
		return err
	}
	for i := range b.in.Validators {
		if err := os.WriteFile(b.genesisPath(i), data, 0o644); err != nil {
			return err
		}
	}
	validate := []string{"genesis", "validate"}
	if !b.cli.helpHas([]string{"genesis"}, " validate ") {
		validate = []string{"validate-genesis"}
	}
	_, err = b.cli.run("validate genesis", append(validate, "--home", b.home(0))...)
	return err
}

func genesisCoins(bond, fee string) string {
	if bond == fee {
		return bondBalance.String() + bond
	}
	return bondBalance.String() + bond + "," + feeBalance.String() + fee
}

func (b *builder) configure(nodes []Node) error {
	for i := range nodes {
		var peers []string
		for j, other := range nodes {
			if j != i {
				peers = append(peers, fmt.Sprintf("%s@127.0.0.1:%d", other.NodeID, portIn(b.ports[j], "p2p.laddr")))
			}
		}
		ports, err := configure(b.home(i), nodeSettings{ports: b.ports[i], peers: peers, blockTime: b.p.BlockTime, feeDenom: b.p.FeeDenom})
		if err != nil {
			return fmt.Errorf("configure %s: %w", nodes[i].Name, err)
		}
		nodes[i].Ports = ports
	}
	return nil
}

func (b *builder) nodeSpecs(nodes []Node) []supervisor.NodeSpec {
	noColor := b.cli.helpHas([]string{"start"}, "--log_no_color")
	var specs []supervisor.NodeSpec
	for _, n := range nodes {
		home := NodeHome(b.dir, n)
		args := []string{"start", "--home", home}
		if noColor {
			args = append(args, "--log_no_color")
		}
		specs = append(specs, supervisor.NodeSpec{
			Index: n.Index, Name: n.Name, Binary: b.cli.bin, Args: args, Home: home,
			LogPath: filepath.Join(home, "node.log"),
			PidPath: filepath.Join(home, "node.pid"),
		})
	}
	return specs
}

// chainCLI runs the chain binary, appending every command and its output to
// log.
type chainCLI struct {
	ctx context.Context
	bin string
	log io.Writer
}

type cliResult struct {
	stdout, stderr []byte
}

func (c chainCLI) run(step string, args ...string) (cliResult, error) {
	return c.exec(step, true, args...)
}

// runSecret keeps the command's output out of the log, for commands that
// print mnemonics.
func (c chainCLI) runSecret(step string, args ...string) (cliResult, error) {
	return c.exec(step, false, args...)
}

func (c chainCLI) exec(step string, logOutput bool, args ...string) (cliResult, error) {
	cmd := exec.CommandContext(c.ctx, c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	_, _ = fmt.Fprintf(c.log, "$ %s %s\n", filepath.Base(c.bin), strings.Join(args, " "))
	err := cmd.Run()
	if logOutput {
		_, _ = c.log.Write(stdout.Bytes())
		_, _ = c.log.Write(stderr.Bytes())
	}
	if err != nil {
		return cliResult{}, fmt.Errorf("%s: %s %s: %w: %s", step, filepath.Base(c.bin), args[0], err, lastLine(stderr.String()))
	}
	return cliResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}, nil
}

// helpHas reports whether `<bin> <args> --help` mentions needle.
func (c chainCLI) helpHas(args []string, needle string) bool {
	out, _ := exec.CommandContext(c.ctx, c.bin, append(args, "--help")...).CombinedOutput()
	return strings.Contains(string(out), needle)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
