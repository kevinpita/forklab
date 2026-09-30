# forklab

forklab runs a local network of N validators for any Cosmos SDK chain. A lab
starts from a fresh genesis or from mainnet state forked out of a snapshot.
forklab then gives you node control, governance, and software upgrades from
one command line, so you can rehearse a chain upgrade on your machine before
it happens for real.

forklab runs stock chain binaries and never patches the chain. It does its
work by rewriting genesis JSON and by driving the chain binary and the
CometBFT RPC. Chain differences live in profiles, so the core has no chain
specific code. Two profiles ship built in: `simd` (the Cosmos SDK example
chain) and `xrplevm` (XRPL EVM, binary `exrpd`).

forklab runs one lab at a time. Many labs and profiles can exist on disk.

## Install

With Go 1.27 or later:

```sh
go install github.com/kevinpita/forklab/cmd/forklab@latest
```

A `go install` build reports its version as `dev`.

Or download a release archive for Linux or macOS (amd64 or arm64) from the
[releases page](https://github.com/kevinpita/forklab/releases), check it
against `checksums.txt`, and put `forklab` on your `PATH`:

```sh
tar xzf forklab_0.1.0_linux_amd64.tar.gz
install forklab ~/.local/bin/
forklab version
```

Profiles with `git` or `src` binaries build the chain from source, so they
need `git` and Go on the machine.

## Quick start

### A fresh simd lab

The `simd` profile builds `simd` v0.53.8 from the cosmos-sdk repository the
first time you use it.

```sh
forklab lab create demo --profile simd --version 0.53.8 --validators 2
forklab lab up demo
forklab status
forklab node stop 1
forklab node start 1
forklab account list
forklab account send test0 test1 1000
forklab gov submit --template text --title "hello" --auto-vote
forklab gov list
forklab lab down demo
```

Commands that act on a running lab take `--lab <name>`. Without it they use
the running lab, or the only lab on disk.

### An upgrade rehearsal on xrplevm

The `xrplevm` profile downloads `exrpd` release binaries. This lab starts on
11.1.1 and upgrades to 11.2.0:

```sh
forklab lab create xrp --profile xrplevm --version 11.1.1 --validators 2 --chain-id xrplevm_1449999-1
forklab lab up xrp
forklab upgrade schedule 11.2.0 --in 45
forklab upgrade status
```

`upgrade schedule` fetches the new binary, submits a software upgrade
proposal, votes it through, and waits. When the nodes halt at the plan
height, the supervisor restarts each one on 11.2.0, and the command returns
once blocks are produced past that height. The plan height must be after the
end of the voting period, so `--in` must cover the voting period in blocks
plus a small margin. forklab refuses a height that is too close and names the
smallest one that works.

Use `--no-auto-swap` to leave the halted nodes alone and restart them yourself
with `forklab node restart --binary 11.2.0 <i>`, for example to test mixed
versions or a restart order. `forklab upgrade cancel` cancels a scheduled plan
through governance.

This fresh lab needs `--chain-id xrplevm_1449999-1`. The XRPL EVM v11.2.0
upgrade handler does escrow work only on the mainnet chain id
`xrplevm_1440000-1`, and that work fails on a fresh genesis that lacks
mainnet state.

## Fork mode

A forked lab takes over mainnet state. forklab downloads and extracts a
snapshot, exports its state with the chain binary, and rewrites the export.
The rewrite gives your local validators about 90% of the voting power,
creates a `gov` account with a delegation so that you can pass proposals, and
funds test accounts.

```sh
forklab lab create xrp-fork --profile xrplevm --version 11.1.1 --fork polkachu
```

`--fork` takes one of these:

- A snapshot name from the profile's `snapshots` map, such as `polkachu`.
- A URL to a `.tar.lz4`, `.tar.gz`, or `.tar.zst` archive.
- A local archive file in one of those formats.

`--version` must be a binary version that can run the snapshot's state.

Snapshots of a real chain are large. The disk must hold the archive, its
extracted data, and the JSON export at the same time. forklab caches the
download and the export under `~/.forklab/snapshots/`, so a failed or repeated
`lab create` does not download again. It deletes the extracted data after the
export unless you pass `--keep-snapshot-work`.

A forked lab keeps the profile's chain id, which for `xrplevm` is the mainnet
id. Keep it for a faithful rehearsal, because upgrade handlers can branch on
the chain id. Pass `--chain-id` only when you want to change that.

## Profiles

A profile is a YAML file that describes one chain. forklab rejects unknown
fields. User profiles live in `~/.config/forklab/profiles/` (or
`$XDG_CONFIG_HOME/forklab/profiles`, or `$FORKLAB_CONFIG_DIR/profiles`). A
user profile with the name of a built-in profile shadows it.

```sh
forklab profile list
forklab profile show xrplevm
forklab profile create mychain --from simd --chain-id mychain-1
forklab profile edit mychain --binary "1.0.0=path:/usr/local/bin/simd"
forklab profile validate ./mychain.yaml
```

`profile create` and `profile edit` set every field with a flag. Run
`forklab profile create --help` for the list.

| Field | Meaning |
|-------|---------|
| `name` | Profile name |
| `binary_name` | Chain binary, such as `simd` or `exrpd` |
| `chain_id` | Chain id for fresh labs and for forks without `--chain-id` |
| `bech32_prefix` | Account address prefix, such as `cosmos` |
| `key_algo` | Keyring algorithm, such as `secp256k1` or `eth_secp256k1` |
| `bond_denom` | Staking denom for fresh labs |
| `fee_denom` | Fee denom, and the denom of a bare number amount in `account send` |
| `gas_prices` | Gas price for lab transactions, such as `0.025stake` |
| `block_time` | Target block time, such as `1s` |
| `export_args` | Extra arguments for `<bin> export` in fork mode |
| `extra_ports` | Additional config keys that need a port per node, by file |
| `gov` | `voting_period` and `expedited_voting_period` for the lab |
| `upgrade_name` | Upgrade plan name template, such as `v{version}` |
| `binaries` | Binary source per version (see below) |
| `snapshots` | Snapshot name to URL, for `--fork <name>` |
| `fresh_patches` | gojq expressions applied last to a fresh genesis |
| `fork_patches` | gojq expressions applied last to a forked genesis |

Templates can use `{version}`, `{os}` (`linux`), `{Os}` (`Linux`), `{arch}`,
and `{chain_id}`.

Node ports start at the CometBFT and SDK defaults (26656, 26657, 1317, 9090,
and the `extra_ports` values, plus pprof on 6060) and add 100 per node.

### fresh_patches and fork_patches

A fresh genesis comes from the chain's own `init` and `genesis` commands, so it
lacks chain setup that mainnet has, such as denom metadata or module params.
`fresh_patches` add that setup. A forked genesis already has it from mainnet
state, and `fork_patches` fix only what the takeover rewrite leaves
inconsistent for that chain. forklab applies the patches in order, after its
own rewrite and the gov patch.

### Binary sources

Each entry in `binaries` has exactly one source:

| Source | Behavior |
|--------|----------|
| `url` | Download, unpack the archive if needed, and find `binary_name`. Cached. |
| `path` | Use an existing binary. |
| `git` | Clone the repository at `ref`, run `build` unmodified, and take `out`. Cached. |
| `src` | Build from a local checkout with `build` and take `out`. Rebuild with `forklab binary build`. |

`git` and `src` take an optional `env` map for the build. The built-in `simd`
profile uses it to build cosmos-sdk v0.53.8 with `GOTOOLCHAIN=go1.23.6` and
`CGO_ENABLED=0`:

```yaml
binaries:
  "0.53.8":
    git: https://github.com/cosmos/cosmos-sdk
    ref: v{version}
    build: cd simapp && go build -o build/simd ./simd
    out: simapp/build/simd
    env: { GOTOOLCHAIN: go1.23.6, CGO_ENABLED: "0" }
  dev:
    src: /home/me/code/cosmos-sdk
    build: cd simapp && go build -o build/simd ./simd
    out: simapp/build/simd
```

forklab runs `<bin> version` on every resolved binary and fails if the output
differs from the requested version. `binary fetch --no-verify` accepts such
a binary. A binary that prints no version is accepted and listed as
`unknown`. forklab records the reported version of a `src` build instead of checking it.
Binaries are cached under `~/.forklab/bin/<chain>/<version>/`.

## CLI reference

| Command | What it does |
|---------|--------------|
| `profile create <name>` | Create a user profile from flags, optionally cloning one with `--from` |
| `profile edit <name>` | Change profile fields; editing a built-in saves a user copy |
| `profile validate <file\|name>` | Validate a profile file or a stored profile |
| `profile list` | List user and built-in profiles |
| `profile show <name>` | Print a profile as YAML |
| `profile delete <name>` | Delete a user profile |
| `binary fetch <version> --profile <p>` | Download or build a binary into the cache |
| `binary build <version> --profile <p>` | Rebuild a `git` or `src` binary |
| `binary list` | List cached binaries |
| `lab create <name> --profile <p> --version <v>` | Create a fresh lab, or a forked one with `--fork` |
| `lab list` | List labs |
| `lab show <name>` | Show nodes, ports, and keys (`--show-mnemonics` for mnemonics) |
| `lab up <name>` | Start the supervisor and nodes and wait for blocks |
| `lab down <name>` | Stop the nodes and the supervisor |
| `lab reset <name>` | Wipe chain data and replay from genesis (`--force` stops the lab first) |
| `lab delete <name>` | Delete a stopped lab |
| `node list` | Show each node's state, pid, uptime, and binary |
| `node start <i\|all>` | Start a node or all nodes |
| `node stop <i\|all>` | Stop with SIGTERM |
| `node kill <i\|all>` | Kill with SIGKILL to simulate a crash |
| `node restart <i\|all>` | Restart, optionally on another binary with `--binary` |
| `node logs <i>` | Print a node's log, `-f` to follow |
| `status` | Node heights, the validator set, and any pending upgrade (`-w` to watch) |
| `consensus` | Height, round, step, proposer, and each validator's votes (`-w` to watch) |
| `account list` | Lab keys, addresses, and balances |
| `account send <from> <to> <amount>` | Send tokens and wait for the block |
| `gov submit [file.json]` | Submit a proposal from a file or `--template text\|upgrade\|params`, `--auto-vote` to vote yes |
| `gov vote <id> <option>` | Vote yes, no, abstain, or veto from lab keys |
| `gov list` | List proposals |
| `gov show <id>` | Show a proposal and its tally |
| `upgrade schedule <version>` | Schedule an upgrade at `--height` or `--in` blocks and swap binaries at the halt |
| `upgrade cancel` | Cancel the scheduled upgrade through governance |
| `upgrade status` | Show the plan and each node's version and swap state |
| `exec -- <bin args>` | Run the chain binary with the lab's home, node, chain id, and keyring |
| `tui` | Open the terminal UI |
| `version` | Print the forklab version |

Run `forklab <command> --help` for every flag.

forklab is built for scripts and agents as well as people:

- Every command accepts `--json` and then prints one envelope on stdout,
  `{"ok":true,"data":{...}}` or
  `{"ok":false,"error":{"code":"usage","message":"..."}}`.
- Streaming commands (`status -w`, `consensus -w`, `node logs`) print one
  JSON object per line with `--json`.
- Commands never prompt. Missing input is an error that names the flag.
- Exit codes are `0` ok, `1` error, `2` usage, and `3` lab not running.
- Progress and warnings go to stderr.
- Help (`forklab`, `forklab help`, `--help`) always prints text.

## TUI

`forklab tui` opens a keyboard-driven terminal UI with panels for nodes,
consensus, proposals, upgrades, accounts, profiles, binaries, and labs. Every
action runs a `forklab ... --json` command, and the TUI shows that command
before it runs. Press `Ctrl+K` for the command palette and `?` for the keys of
the focused panel.

## Files

| Path | Contents |
|------|----------|
| `~/.config/forklab/profiles/` | User profiles |
| `~/.forklab/bin/` | Binary cache |
| `~/.forklab/snapshots/` | Snapshot downloads and exports |
| `~/.forklab/labs/<lab>/` | Lab config, keyring and mnemonics, node homes and logs |

Set `FORKLAB_HOME` to move `~/.forklab`.

## Development

The toolchain (Go 1.27, gofumpt, golangci-lint) comes from
[devenv](https://devenv.sh). Enter it with `devenv shell` or with direnv
(`direnv allow`), or prefix one command with `devenv shell --`.

```sh
devenv shell -- just check
just run lab list
```

| Recipe | What it does |
|--------|--------------|
| `just build` | Build `bin/forklab` with the version from `git describe` |
| `just run <args>` | Build, then run `bin/forklab <args>` |
| `just test` | Run the tests with the race detector |
| `just lint` | Run golangci-lint |
| `just fmt` | Format with gofumpt |
| `just check` | fmt, lint, test, and build, the same gate as CI |
| `just clean` | Remove `bin/` |

End-to-end tests run real chains and use the `e2e` build tag:
`go test -tags e2e ./internal/cli`.

Pushing a `v*` tag runs GoReleaser, which publishes the release archives.

## License

MIT. See [LICENSE](LICENSE).
