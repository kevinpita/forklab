# forklab documentation

forklab runs a local network of N validators for any Cosmos SDK chain. A lab
starts from a fresh genesis or from mainnet state forked out of a snapshot.
forklab then gives you node control, governance, and software upgrades from
one command line, so you can rehearse a chain upgrade on your machine before
it happens for real.

forklab runs stock chain binaries and never patches the chain. It does its
work by rewriting genesis JSON and by driving the chain binary and the
CometBFT RPC. Chain differences live in profiles, so the core has no chain
specific code.

forklab runs one lab at a time. Many labs and profiles can exist on disk.

## Pages

- [Agent guide](agents.md). CLI workflow, output handling, recovery, and the
  [repository skill](../.agents/skills/forklab/SKILL.md).
- [Shell integration](shell.md). Completions and shell helpers.

- [TUI](tui.md). The terminal UI: panels, keys, forms, the first-lab wizard,
  themes, the command palette, and the command preview.
- [CLI reference](cli.md). Every command, the `--json` envelope, NDJSON
  streams, exit codes, `--lab` resolution, and the files forklab writes.
- [Profiles](profiles.md). The profile format, `fresh_patches` and
  `fork_patches`, binary sources, and the built-in `simd` and `xrplevm`
  profiles.
- [Fork mode](fork-mode.md). Labs that take over mainnet state from a
  snapshot.
- [Upgrades](upgrades.md). Scheduling an upgrade, automatic and manual binary
  swaps, cancelling, and resetting a lab after an upgrade.
- [Runbooks](runbooks.md). Reusable recipes, assertions, exact-height pause,
  raw stores, and execution reports.
- [Development](development.md). The toolchain, `just` recipes, end-to-end
  tests, and rendering the screenshots.

Back to the [project README](../README.md).
