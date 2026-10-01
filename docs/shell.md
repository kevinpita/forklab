# Shell integration

forklab generates completions for bash, zsh, fish, and PowerShell. Bash,
zsh, and fish can also load `fl`, `flab`, and `flogs` helpers.

## Load completions and helpers

Put the `forklab` executable on `PATH`, then run the command for your shell.
It affects the current session only. From a checkout, run `just build` and
`export PATH="$PWD/bin:$PATH"` first.

Bash requires the `bash-completion` package. Load its `bash_completion`
file before the integration script if your shell startup has not already
loaded it. Common locations include `/usr/share/bash-completion/bash_completion`
and `/etc/profile.d/bash_completion.sh`. Then run:

```bash
source <(forklab shell init bash)
```

Zsh needs its completion system initialized before sourcing the script:

```zsh
autoload -Uz compinit
compinit
source <(forklab shell init zsh)
```

Fish:

```fish
forklab shell init fish | source
```

To load these on future sessions, add the matching source command yourself
to `~/.bashrc`, `~/.zshrc`, or `~/.config/fish/config.fish`. In zsh, put it
after your existing `compinit` call. forklab does not edit shell startup
files, install packages, or select a lab.

## Helpers

| Helper | Equivalent command |
|--------|--------------------|
| `fl ARGS...` | `forklab ARGS...` |
| `flab LAB` | `forklab --lab=LAB status` |
| `flab LAB COMMAND ARGS...` | `forklab --lab=LAB COMMAND ARGS...` |
| `flogs LAB NODE ARGS...` | `forklab node logs --lab=LAB NODE ARGS...` |

`flab` requires an explicit lab name or directory on every call. Use it
with commands that accept `--lab`, such as `status`, `node`, `account`,
`gov`, `upgrade`, and `exec`. Lab lifecycle commands take a positional lab
name, so use `fl lab up demo` or `fl lab down demo`.

```sh
fl lab list
flab demo
flab demo node restart 1
flab demo exec -- query bank balances 'cosmos1...'
flogs demo 0 --tail 30
flogs demo 0 -f
```

`flogs` finishes after printing the log unless you pass `-f` or `--follow`.
Helpers preserve argument boundaries and the command's exit status.
They pass `--` through to `exec`. `fl` shares forklab's tab completion;
`flab` and `flogs` do not add their own completion rules.

## Completions only

Replace `shell init` with `completion` in the source commands above to
load completions without helpers. You can also save the generated script
and source that file from your startup file:

```sh
forklab completion bash > forklab.bash
forklab completion zsh > forklab.zsh
forklab completion fish > forklab.fish
forklab completion powershell > forklab.ps1
```

In PowerShell, dot-source the saved script with `. ./forklab.ps1`.

Commands and flags come from the CLI command tree. Dynamic choices include
local lab and profile names, profile versions, and node indexes. `all` is
offered only for node commands that accept it. Version suggestions for
upgrade and node restart use the selected lab's saved profile.

Completion reads local metadata only. It does not contact nodes or the
supervisor, load keys, create files, or build or download binaries. If you
omit `--lab`, dependent suggestions use the only lab on disk. With multiple
labs, pass `--lab` to get node and version suggestions. Invalid or missing
metadata produces no dependent suggestions. Lab directory references and
profile validation file paths retain filesystem completion.

`completion` and `shell init` write raw shell code to stdout. They reject
`--json` with a usage error envelope and exit code 2. See the
[CLI reference](cli.md) for the output contract.
