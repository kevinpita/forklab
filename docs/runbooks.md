# Runbooks

A runbook is a versioned YAML recipe for transactions, queries, assertions,
and chain control. Steps run in file order against the lab selected by
`--lab`. The engine stops at the first failed step.

The [bank transfer example](../examples/runbooks/bank-transfer.yaml) uses
funded `test0` and `test1` lab keys and the profile's fee denom. Module
commands, response fields, and transaction codes depend on the chain.
Amounts are strings in native token units, without decimal conversion.

## Commands

| Command | Behavior |
|---------|----------|
| `runbook validate <file.yaml>` | Checks YAML fields, step structure, IDs, durations, and jq syntax without contacting a chain |
| `runbook show <file.yaml>` | Prints the validated document as YAML, or as JSON with `--json` |
| `runbook write <file.yaml> --document '<json>'` | Validates a JSON document and saves YAML, with `--force` required to replace a file |
| `runbook run <file.yaml> --lab <name>` | Executes the recipe and saves a JSON report |
| `lab pause <name> --height <H>` | Freezes committed application state at height H and keeps query RPC available |
| `lab resume <name>` | Restores the original node arguments and verifies that commits resume |
| `store <module> <hex-key> --lab <name>` | Reads raw KV store bytes through ABCI |

For example, these commands validate and execute the bank transfer recipe:

```sh
forklab runbook validate examples/runbooks/bank-transfer.yaml
forklab runbook run examples/runbooks/bank-transfer.yaml --lab demo --report ./report.json
```

Validation does not resolve templates or check whether a chain supports the
module commands. `runbook write` requires the destination directory to exist.
The [TUI builder](tui.md#create-a-runbook) creates and edits the same YAML.

## Document and step fields

The document has `version: 1`, an optional `name`, an optional `vars` map,
and a nonempty `steps` list. Unknown fields and multiple YAML documents are
errors. Each step has exactly one action.

| Field | Meaning |
|-------|---------|
| `id` | Optional unique name for the output. Starts with a letter or underscore, followed by letters, numbers, or underscores |
| `at_height` | Minimum committed application height before the action starts |
| `timeout` | Positive duration such as `30s` or `2m`, covering the height wait and the action |
| `tx` | Signing key in `from`, command argv in `args`, and optional `expect_code` |
| `query` | Chain query argv, with JSON output stored as the step result |
| `assert` | jq expression over the execution context, required to produce exactly one `true` |
| `wait_height` | Positive committed application height to wait for |
| `pause` | Positive committed application height to freeze |
| `hold: true` | Waits until another command resumes the lab |
| `resume: true` | Removes the pause and waits for new commits |
| `store` | Raw KV lookup with `name`, `key_hex`, optional `height`, and optional `prefix` |
| `script` | Local executable and arguments as an argv list |

Ordinary steps default to one minute. `runbook run --step-timeout` changes
that default, and a step's `timeout` overrides it. A `hold` without an
explicit YAML `timeout` waits indefinitely until manual resume or cancellation.

`at_height: H` waits for at least H before starting a step. It does not
guarantee transaction inclusion in block H. Waiting for an already reached
height returns immediately. A lab paused below the requested height returns
an error instead of waiting for blocks that cannot arrive.

### Transactions and assertions

`tx.args` starts with the module and message, such as `bank send`.
`query` starts with the module query, such as `bank balances`.
forklab supplies the chain binary, home, RPC address, and JSON output flags.
For transactions, it also supplies the signing key, chain ID, keyring, gas,
and gas prices from the lab. Explicit `--gas`, `--gas-adjustment`, and
`--gas-prices` override those defaults; `--fees` replaces default gas prices.
For an expected execution failure, supply a fixed `--gas` so gas simulation
does not reject the transaction before broadcast.

An accepted transaction waits for its committed result before the next step.
The output contains `hash`, `height`, and `code`, with `codespace` and `log`
when present. `expect_code` defaults to `0`. An expected nonzero code can
match a CheckTx rejection or a committed execution failure. A CheckTx
rejection has no committed height. Signing and transport errors still fail
the step.

Assertions see the whole context, including named step outputs. For example,
`assert: '.steps.send.code == 0'` checks a transaction result. False, empty,
multiple, or nonboolean results fail. jq runs inside forklab and does not
require a separate `jq` executable.

## Template context

Go templates render transaction signing keys and command arguments, query
arguments, script arguments, store names, and store keys. Missing template
values fail the step. Numeric height fields and assertions are not templates.

| Value | Example |
|-------|---------|
| Recipe variables | `{{ .vars.amount }}` |
| Lab account address | `{{ .accounts.val0.address }}` |
| Chain ID | `{{ .chain_id }}` |
| Profile denoms | `{{ .fee_denom }}` and `{{ .bond_denom }}` |
| Earlier named output | `{{ .steps.send.hash }}` |

An account is available only if that key exists in the lab. Forked labs do
not have validator operator keys. `test0` and `test1` are suitable for the
bank example. In assertions, the same values use jq syntax, such as
`.steps.send.code` or `.accounts.test0.address`.

## Exact-height inspection

The [pause and inspect example](../examples/runbooks/pause-inspect.yaml)
pauses at H, reads state, and holds for manual debugging. Its H is `20`, so
the example applies only while no node has committed beyond that height.

The pause strategy supports stock Cosmos SDK 0.50 and 0.53 binaries that
expose `--halt-height`. forklab restarts the nodes with a halt at H+1,
before `FinalizeBlock`, and verifies that every node's committed application
height is H. CometBFT's block store may already contain block H+1. That
block store height does not mean the application committed H+1.

An unsupported SDK or missing halt capability returns a clear error.
Ordinary transaction and query steps remain usable without pause support.
Pause also rejects a pending upgrade, existing halt arguments, an existing
pause, or a target that a node has already passed. It does not rewind state.

Queries and raw store reads remain available while paused. Transactions
require resume. A pause remains after a recipe ends, fails, or is cancelled.
An interrupted pause retains recovery state once armed. `lab resume` restores
the original node arguments and checks that block production resumes.

`hold: true` lets another terminal or the TUI issue `lab resume`. The recipe
then continues. Without a retained pause, `hold` returns immediately.
`lab pause` and `lab resume` each default to a two-minute command timeout.

## Raw stores and optional scripts

`store` returns `height`, `key_hex`, `value_hex`, and `value_base64`.
Keys are nonempty hexadecimal bytes. Store names and key layouts depend on
the chain. Values remain bytes without chain-specific protobuf decoding.
`height: 0`, or an omitted height, reads the latest committed state.
Historical reads depend on pruning. `prefix: true`, or CLI `--prefix`,
requires the chain's ABCI subspace query support.

Scripts are optional local checks. The
[script context example](../examples/runbooks/script-context.yaml) requires
Python 3 and reads the execution context as JSON on stdin. A script runs in
the recipe's directory, so `script: ["python3", "./check.py"]` refers to a
file beside the YAML. argv lists have no implicit shell parsing.

The script inherits the environment and receives `FORKLAB_LAB_DIR`,
`FORKLAB_CHAIN_ID`, and `FORKLAB_BINARY`. Its output contains `stdout`,
`stderr`, and `exit_code`. If stdout is valid JSON, `json` contains the
decoded value. A nonzero exit fails the step and preserves its captured
output in the report.

## Reports

The default report path is `<lab directory>/runbooks/<timestamp>.json`.
`--report <path>` selects a different file. forklab checks that the directory
is writable before executing steps and saves the report even when execution
fails. Validation or lab setup errors occur before execution and produce no
execution report.

Each report records the recipe name and each attempted step's index, ID,
action, duration, output, and error. A failure also sets the report's `error`.
With `--json`, an execution failure returns `ok: false` with the report in
`data` and exits with code `1`. Progress and the report path go to stderr.
