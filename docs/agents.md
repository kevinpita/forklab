# Operate forklab from an agent

forklab runs local Cosmos SDK validator networks with stock chain binaries.
Use this guide to choose the lab, run a command, and verify its result.
The [forklab skill](../.agents/skills/forklab/SKILL.md) provides the workflow.

## Identify the executable and lab

Find the installed `forklab`, or build `bin/forklab` with `just build` in a
checkout. Use that same executable throughout the task. Check `version` and
`--help`, then inspect the requested command's help for its exact flags.
Running forklab without a command opens the TUI.

```sh
forklab version --json
forklab skill
forklab lab list --json
forklab lab show demo --json
forklab status --lab demo --json
```

Replace `demo` with the requested lab name. `lab show` and lab lifecycle
commands take a name as a positional argument. Chain and node commands
use `--lab`, which accepts a name or directory. Supply the target explicitly.
Automatic resolution selects the
running lab, then the only lab on disk, and may differ between sessions.
If the request does not identify a target, inspect the lab list and establish
which lab the user means before changing state. Read-only discovery can
continue while that is unresolved.

`FORKLAB_HOME` changes the lab, binary, and snapshot root, normally
`~/.forklab`. Profiles have a separate configuration root described in
[profile locations](profiles.md#where-profiles-live). Labs contain keys and
mnemonics. `lab show --show-mnemonics` explicitly reveals the mnemonics.

## Parse results

Use `--json` for machine output. Ordinary commands write one envelope to
stdout, such as `{"ok":true,"data":{...}}` or
`{"ok":false,"error":{"code":"usage","message":"..."}}`.
Read both `ok` and the exit status. Progress and warnings go to stderr.

The output contract has these exceptions:

- `status -w`, `consensus -w`, and `node logs` emit NDJSON with `--json`.
  Each line is a result object without an envelope. Watch failures can emit
  `{"error":{"code":"...","message":"..."}}` and continue. Setup
  failures can return the ordinary error envelope.
- `exec --json -- <args>` captures the child process's `stdout`, `stderr`,
  `args`, and `exit_code` inside `data`. Child failure sets `ok: false` with
  `data`, without an `error` field, and passes through the child's exit code.
  Without `--json`, child output and exit status pass through directly.
- `runbook run --json` also returns `ok: false` with report `data` on execution
  failure. Validation and lab setup errors use the ordinary error envelope.
- Help is text even with `--json`. The TUI does not support JSON output.
  `completion` and `shell init` print executable scripts and reject `--json`.
  See [shell integration](shell.md) for completions and helpers.

Ordinary exit codes are 0 for success, 1 for failure, 2 for usage errors, and
3 when the lab is not running. `exec` can return other child exit codes.
Commands report missing input as errors instead of prompting. A chain binary
started through `exec` can have its own interaction requirements.

## Choose the operation

For a new lab, inspect the profile and binary source before `lab create`.
A fresh lab builds genesis. `lab create --fork` imports snapshot state and
replaces the validator set. Follow [profiles](profiles.md) and
[fork mode](fork-mode.md) for chain IDs, snapshots, and genesis corrections.

For governance, inspect `gov --help` and distinguish these operations:

- `gov draft <id>` clones a live proposal into an editable submission
  document on stdout. With `--json`, the document is inside `data`.
- `gov export <id> <file.json>` saves a proposal record with its tally.
  That record is not a submission document.
- `gov write <file.json> --document '<json>'` validates and saves a submission
  document without contacting the chain.
- `gov submit --template <type> --output <file.json>` saves a template without
  submitting or voting. It still reads defaults from the live lab.
- `gov submit <file.json>` broadcasts the proposal. `--auto-vote` also votes.

File writes require `--force` to replace an existing destination. After
submission, use `gov show <id> --lab <lab> --json` to check the actual status
and tally. Successful submission or voting does not mean the proposal passed.
See [CLI reference](cli.md) and [governance in the TUI](tui.md).

For upgrades, read [upgrades](upgrades.md). `upgrade schedule` normally waits
for blocks beyond the upgrade height. `--no-wait` returns after registration,
and `--no-auto-swap` leaves the binary changes to the operator.
`upgrade status --lab <lab> --json` can inspect persisted recovery state even
when the chain is down. `upgrade recover` also supports a stopped chain:
`--previous` switches binaries and skips the failed height, while `--version`
retries with a profile version. Recovery restarts validators and verifies
fresh blocks. It does not restore a database checkpoint.

For repeatable transactions and assertions, read [runbooks](runbooks.md).
Validate the recipe before `runbook run`, then inspect its saved report,
including on failure. Validation checks structure, not chain support or
resolved template values. Script steps execute local programs.

For exact-height inspection, use `lab pause <lab> --height <H>`, then `store`
or queries. Compare committed application heights, since the block store may
already contain H+1. Pause cannot rewind. A pause survives recipe completion,
failure, or cancellation. Resume explicitly with `lab resume <lab>` when the
task calls for blocks to continue. Historical store reads depend on pruning.

## Verify the requested outcome

After a change, query the state that proves the user's result. Use `status`
for committed heights, `node list` for processes and binaries, `gov show`
for proposals, `upgrade status` for upgrade state, or a runbook assertion for
chain-specific state. Check fresh blocks after a restart or recovery.
A successful command alone does not prove the intended chain state.

Report the lab, action, observed result, and any remaining pause or pending
upgrade. Preserve useful report paths and command errors in the handoff.

## Documentation map

- [Documentation index](README.md): overview and topic entry points.
- [CLI reference](cli.md): commands, output, lab resolution, and local files.
- [Shell integration](shell.md): completions and shell helpers.
- [Profiles](profiles.md): configuration, binaries, denoms, and genesis patches.
- [Fork mode](fork-mode.md): snapshot discovery, export, and validator takeover.
- [Upgrades](upgrades.md): schedule, swap, cancel, recover, and reset.
- [Runbooks](runbooks.md): transactions, assertions, pause, stores, scripts, and reports.
- [TUI](tui.md): forms, governance, runbook builder, keys, and command previews.
- [Development](development.md): toolchain, checks, extensions, and end-to-end tests.
- [Project README](../README.md): installation and first lab.
