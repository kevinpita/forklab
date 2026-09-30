# Profiles

A profile is a YAML file that describes one chain. Chain differences live in
profiles, so the forklab core has no chain specific code. forklab rejects
unknown fields.

```sh
forklab profile list
forklab profile show xrplevm
forklab profile create mychain --from simd --chain-id mychain-1
forklab profile edit mychain --binary "1.0.0=path:/usr/local/bin/simd"
forklab profile validate ./mychain.yaml
```

`profile create` and `profile edit` set every field with a flag. Run
`forklab profile create --help` for the list.

## Where profiles live

User profiles live in `~/.config/forklab/profiles/` (or
`$XDG_CONFIG_HOME/forklab/profiles`, or `$FORKLAB_CONFIG_DIR/profiles`). A
user profile with the name of a built-in profile shadows it, and editing a
built-in profile saves a user copy.

## Built-in profiles

- `simd` is the Cosmos SDK example chain. It builds cosmos-sdk v0.53.8 from
  source (see [Binary sources](#binary-sources)).
- `xrplevm` is XRPL EVM, binary `exrpd`. It downloads the 11.1.1 and 11.2.0
  release binaries, forks from the `polkachu` snapshot, and keeps the mainnet
  chain id `xrplevm_1440000-1`.

Both set a 30s voting period and a 20s expedited voting period. Run
`forklab profile show <name>` to read one in full.

## Fields

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

## fresh_patches and fork_patches

A fresh genesis comes from the chain's own `init` and `genesis` commands, so it
lacks chain setup that mainnet has, such as denom metadata or module params.
`fresh_patches` add that setup. A forked genesis already has it from mainnet
state, and `fork_patches` fix only what the takeover rewrite leaves
inconsistent for that chain. forklab applies the patches in order, after its
own rewrite and the gov patch.

## Binary sources

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

> [!NOTE]
> `git` and `src` sources need `git` and Go on the machine.

forklab runs `<bin> version` on every resolved binary and fails if the output
differs from the requested version. `binary fetch --no-verify` accepts such
a binary. A binary that prints no version is accepted and listed as
`unknown`. forklab records the reported version of a `src` build instead of
checking it. Binaries are cached under `~/.forklab/bin/<chain>/<version>/`.
