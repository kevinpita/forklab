---
name: forklab
description: Operate the forklab CLI to inspect or manage local Cosmos SDK labs, fork snapshots, submit governance proposals, rehearse or recover upgrades, and run state-verification recipes.
---

# Operate forklab

1. Read [the agent guide](../../../docs/agents.md), relative to this skill's
   directory. In a release archive, keep the supplied directory layout so
   the guide and its topic links resolve.
2. Identify the executable with `command -v forklab`, or use the checkout's
   `bin/forklab` after building it as described in the development guide.
   Run its `version` and command help. Finish this step with a known
   executable and the exact flags for the requested operation.
3. Identify the requested lab with `lab list --json` and `lab show <lab>
   --json`. Establish the user's intended lab before changing state if the
   target is ambiguous. Use an explicit positional lab or `--lab` on each
   lab operation. For lab creation, establish the destination name and profile.
4. Read the guide's linked topic for the task. Inspect current state and
   choose the operation that matches the request, including whether a
   proposal needs a draft, an export, or submission. For recovery, read
   persisted upgrade state even when RPC is unavailable. Finish with the
   target, operation, and relevant current state established.
5. Execute the requested work using `--json` where supported. Parse the
   envelope or stream as the guide specifies, and inspect the process exit
   status. For runbooks, validate first and retain the execution report.
   Consult command help when an error identifies missing or invalid input.
6. Verify the requested result against lab or chain state using the guide's
   verification section. Report the target lab, observed outcome, useful
   artifact paths, and any pause or upgrade that remains pending.
