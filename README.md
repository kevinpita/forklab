# forklab

forklab is a local multi-validator playground for Cosmos SDK chains. It starts a
lab of N local validators, from a fresh genesis or forked from a mainnet
snapshot, and makes node control, governance, and software upgrades easy. It
ships as one Go binary with an agent-first CLI and a TUI built on that CLI.

Status: early development. See [the design spec](docs/specs/2026-09-29-forklab-design.md).

## CLI contract

- Every command accepts `--json` and then prints one envelope on stdout:
  `{"ok":true,"data":{...}}` or `{"ok":false,"error":{"code":"usage","message":"..."}}`.
- Exit codes: `0` ok, `1` error, `2` usage, `3` lab not running.
- Help (`forklab`, `forklab help`, `--help`) always prints human text and is
  exempt from `--json`.

## Development

The toolchain (Go 1.27, gofumpt, golangci-lint) comes from [devenv](https://devenv.sh).
Enter it with `devenv shell`, or with direnv (`direnv allow`), or prefix a
single command with `devenv shell --`.

```sh
devenv shell -- make build
./bin/forklab version
```

| Target | What it does |
|--------|--------------|
| `make build` | Build `bin/forklab` with the version from `git describe` |
| `make test` | Run the tests |
| `make test-race` | Run the tests with the race detector |
| `make lint` | Run golangci-lint |
| `make fmt` | Format with gofumpt |
| `make fmt-check` | Fail if any file is not gofumpt-formatted |
