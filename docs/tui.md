# TUI

`forklab` with no command (or `forklab tui`) opens a keyboard-driven terminal
UI with panels for nodes, consensus, proposals, upgrades, accounts, labs,
profiles, and binaries. Every action runs a `forklab ... --json` command, and
the TUI shows that command before it runs.

![Nodes panel with the live log of node0](../.github/assets/tui-main.png)

## First run

With no lab on disk, `forklab` opens a seven-step wizard that builds the
`lab create` command as you answer (profile, version, validators, fresh or
forked genesis, snapshot, chain id, and name), then offers to bring the lab
up. Close it and press `n` to open it again while no lab exists, or `u` to
start a lab that is down.

![The first-run wizard on its profile step](../.github/assets/wizard.png)

## Panels

The left column holds every panel, and `1`-`8` jump to one. The main pane
shows the selected item.

- **Nodes.** Each node's state, and the live log of the selected node.
- **Consensus.** The round in progress, vote bars against the 2/3 mark, each
  validator's votes, and the peer map.
- **Proposals.** Proposals with their status, voting time left, and tally.
- **Upgrades.** The scheduled plan and each node's version and swap state.
- **Accounts.** Lab keys, addresses, and balances.
- **Labs**, **Profiles**, and **Binaries.** Everything on disk.

![Consensus view with prevotes, precommits, and the last commit](../.github/assets/tui-consensus.png)

![A passed text proposal with its tally](../.github/assets/tui-proposals.png)

## Keys

Press `?` for the keys of the focused panel.

| Panel | Keys |
|-------|------|
| Any | `1`-`8` jump to a panel, `Tab`/`Shift+Tab` next or previous panel, `Ctrl+K` palette, `:` run a command, `c` command preview, `x` exec, `Ctrl+R` refresh, `T` next theme, `V` version, `?` help, `q` quit |
| Runbooks and inspection | `Ctrl+E` create or edit, `Ctrl+T` run, `Ctrl+V` validate, `Ctrl+O` show, `Ctrl+P` pause, `Ctrl+S` resume, `Ctrl+B` read raw store bytes |
| Panel list | `j`/`k` move, `g`/`G` first or last, `Enter` focus the main pane |
| Main pane | `j`/`k` scroll, `PgUp`/`PgDn` page, `g`/`G` top or bottom, `Esc` back to the list |
| Nodes | `s` stop, `S` start, `K` kill, `r` restart, `R` restart on a version, `l` follow logs, `w` wrap |
| Proposals | `n` new proposal, `v` vote |
| Upgrades | `u` schedule, `X` cancel |
| Accounts | `s` send |
| Labs | `n` new, `u` up, `d` down, `R` reset, `D` delete, `m` keys and mnemonics |
| Profiles | `n` new, `e` edit, `v` validate, `D` delete |
| Binaries | `f` fetch, `b` build |

## Forms

Forms cover a new lab, a new or edited profile, an upgrade schedule, a send, a
proposal, a vote, a restart on a version, and a binary fetch or build. Every
form shows the exact command it will run and updates it as you type. `Tab`
and `Shift+Tab` move between fields, `Left` and `Right` change a choice,
`Enter` runs the command, and `Esc` cancels it or keeps a running command in
the background.

This is `u` on the Upgrades panel:

![The upgrade schedule form with its command line](../.github/assets/upgrade-form.png)

## Create a runbook

Press `Ctrl+E`, enter a YAML path, and choose **Create new** or **Edit existing**.
The builder lists the recipe's steps and opens a form for each action.

| Builder key | Action |
|-------------|--------|
| `j` / `k` | Select a step |
| `a` | Add a step |
| `e` or `Enter` | Edit the selected step |
| `d` | Remove the selected step |
| `J` / `K` | Move the selected step down or up |
| `v` | Add or update a variable |
| `s` | Validate and save the YAML |
| `r` | Choose a lab and run the saved recipe |
| `Esc` | Close the builder |

Choose a transaction, query, assertion, height wait, pause, manual hold,
resume, raw store lookup, or local script. The form shows the fields for
that action. Command arguments support quotes and Go templates, such as
`bank balances '{{ .accounts.test0.address }}'`. The builder saves arguments
as YAML lists without implicit shell parsing. Save changes before running.
Creating or loading another file keeps an unsaved draft unless you explicitly
choose **Discard and open file**.

`Ctrl+T`, `Ctrl+V`, and `Ctrl+O` open file forms for run, validate, and show.
Both run actions show a lab selector and pass the chosen lab explicitly.
`Ctrl+P` selects a committed height to pause the selected lab, `Ctrl+S`
resumes it, and `Ctrl+B` opens a raw store form. A pause keeps query RPC
available and remains until resume, including after a recipe error.
See [Runbooks](runbooks.md) for supported SDK versions, timeout behavior,
the YAML format, and examples.

## Command palette

`Ctrl+K` searches every action and shows the command it runs. `:` runs any
forklab command you type.

![Command palette filtered to node actions](../.github/assets/tui-palette.png)

## Command preview

`c` lists the forklab commands behind the panel. `Enter` runs the selected
one.

![Command preview for the Nodes panel](../.github/assets/tui-preview.png)

## Themes

`--theme` (or `FORKLAB_THEME`) picks `ansi`, `tokyonight`, `catppuccin`, or
`gruvbox`, and `T` cycles them. The default is `ansi`. The screenshots use
`tokyonight`.
