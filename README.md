# forklab

forklab is a local multi-validator playground for Cosmos SDK chains. It starts a
lab of N local validators, from a fresh genesis or forked from a mainnet
snapshot, and makes node control, governance, and software upgrades easy. It
ships as one Go binary with an agent-first CLI and a TUI built on that CLI.

Status: early development.

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
devenv shell -- just build
just run version
```

| Recipe | What it does |
|--------|--------------|
| `just build` | Build `bin/forklab` with the version from `git describe` |
| `just run <args>` | Build, then run `bin/forklab <args>` |
| `just test` | Run the tests with the race detector |
| `just lint` | Run golangci-lint |
| `just fmt` | Format with gofumpt |
| `just check` | fmt, lint, test and build, the same gate as CI |
