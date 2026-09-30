# Development

Snapshot discovery implements `snapshot.Provider` (`Supports` and `Resolve`)
in a separate file per provider. `snapshot.Resolver` selects the first
provider that supports a source; direct archive URLs and local files pass
through when none matches. Polkachu's implementation is in
`internal/snapshot/polkachu.go`. Additional providers can be supplied to a
`Resolver`; register built-in providers in `snapshot.Resolve`.

Genesis corrections implement `genesis.Patcher`. The built-in `JQPatcher`
runs the filters selected by the profile's optional `patches_file`, then
its inline filters. An empty filter list performs no custom corrections.
Keep chain-specific filters in one YAML file; the normal validator takeover
and accounting stay in the shared fork flow. See [profiles](profiles.md).

## Toolchain

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

Pushing a `v*` tag runs GoReleaser, which publishes the release archives.

## End-to-end tests

End-to-end tests run real chains and use the `e2e` build tag:

```sh
go test -tags e2e ./internal/cli ./internal/genesis
```

Each test skips unless the environment variables it needs point at local
chain binaries or homes:

| Variable | Used for |
|----------|----------|
| `FORKLAB_SIMD` | A `simd` v0.53.8 binary (cli and genesis tests) |
| `FORKLAB_EXRPD` | An `exrpd` v11.1.1 binary (cli and genesis tests) |
| `FORKLAB_EXRPD_OLD`, `FORKLAB_EXRPD_NEW` | `exrpd` v11.1.1 and v11.2.0 binaries for the upgrade tests |
| `FORKLAB_SIMD_MAINNET`, `FORKLAB_EXRPD_MAINNET` | A stopped node home (`data/` and `config/genesis.json`) that the fork tests pack into a snapshot |
| `FORKLAB_RPC` | The CometBFT RPC of a live node, for `go test -tags e2e ./internal/chain` |

> [!TIP]
> Live runs bind the lab ports (26656 and up). To run several at once without
> collisions, give each run its own network namespace. The namespace has no
> network, so the chain binaries must be local files:
>
> ```sh
> unshare -rn sh -c 'ip link set lo up && go test -tags e2e ./internal/cli'
> ```

## Screenshots

The README screenshots and GIF come from real labs driven by
[VHS](https://github.com/charmbracelet/vhs) tapes in
[`.github/assets/tapes`](../.github/assets/tapes). The script builds forklab,
points the `simd` and `xrplevm` profiles at local binaries, and records every
tape inside a private network namespace. From the repository root, in a shell
with Go (`devenv shell`):

```sh
export FORKLAB_SIMD=/path/to/simd            # simd v0.53.8
export FORKLAB_EXRPD_OLD=/path/to/exrpd-old  # exrpd v11.1.1
export FORKLAB_EXRPD_NEW=/path/to/exrpd-new  # exrpd v11.2.0
nix shell nixpkgs#vhs nixpkgs#gifsicle -c unshare -rn sh -c 'ip link set lo up && .github/assets/tapes/render.sh'
```

The header of [`render.sh`](../.github/assets/tapes/render.sh) has the
details.
