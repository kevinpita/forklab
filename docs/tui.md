# TUI

`forklab` with no command (or `forklab tui`) opens a keyboard-driven terminal
UI with panels for nodes, consensus, proposals, upgrades, accounts, labs,
profiles, and binaries. Every action runs a `forklab ... --json` command, and
the TUI shows that command before it runs.

![Nodes panel with the live log of node0](../.github/assets/tui-main.png)

## First run

With no lab on disk, `forklab` opens a wizard that builds the `lab create`
command as you answer. Choose a profile, then a configured version, an existing
executable, a download URL, a Git repository, or a local checkout. Custom sources
ask for the expected binary version. Git builds also ask for a ref; Git and local
builds ask for a build command and an output path relative to the checkout.
The custom source is saved in the lab's profile snapshot. The shared profile
stays unchanged.

Choose validators, fresh or forked genesis, an optional chain ID, and a name.
For a fork, pick a configured profile snapshot or enter a URL or local archive.
The chain ID field shows the selected profile's default when left empty.
The wizard shows a review before creating the lab, then offers to bring it up.
Close it and press `n` to open it again while no lab exists, or `u` to start a
lab that is down.

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
| Proposals | `Enter` scroll details, `n` new proposal, `v` vote, `s` export record, `C` clone, `E` reopen draft, `S` submit saved file |
| Upgrades | `u` schedule, `X` cancel |
| Accounts | `s` send |
| Labs | `n` new, `u` up, `d` down, `R` reset, `D` delete, `m` keys and mnemonics |
| Profiles | `n` new, `e` edit, `v` validate, `D` delete |
| Binaries | `f` fetch, `b` build |

## Forms

Forms cover a new lab, a new or edited profile, an upgrade schedule, a send, a
proposal, a vote, a restart on a version, and a binary fetch or build. Every
form shows the exact command it will run and updates it as you type. `Tab`
and `Shift+Tab` move between fields. Arrow keys change a choice; left and right
move the cursor in text fields. In the first-run wizard, `Enter` and `Tab`
validate the answer and move forward. `Shift+Tab` goes back, and `Enter` on
review creates the lab. `Tab` on review keeps the review open. Other forms run
on `Enter`. `Esc` cancels an idle form or keeps a running command in the
background.

Running forms replace the input fields with an activity indicator, the current
operation, elapsed time, and recent completed steps. Downloads show bytes
transferred and a percentage when the server supplies a total. Cached binaries
and snapshots show reuse instead of download progress.

Upgrade scheduling shows proposal confirmation, the voting deadline, each node's
restart state, and block recovery. After `Esc`, the command continues in the
background and the status line shows its current operation.

The TUI requests `--progress=json` for `lab create` and `upgrade schedule`.
This option writes versioned JSON progress records to stderr; stdout still
contains one final result. Without this option, `--json` remains quiet until
the result.

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

Proposal details include wrapped summaries, full dates, metadata, and message JSON.
Press `Enter` to focus the details, then use the scrolling keys to read the whole proposal.
`C` clones the selected proposal into a retained JSON editor with the current minimum deposit.
Edit any submission field, then press `Ctrl+s` to choose a file. `Esc` closes the editor and
`E` reopens the draft. Saving never submits a transaction. Existing files require the explicit
Overwrite option. New proposal forms default to saving a file; choose Submit to chain to broadcast.
`s` exports the selected chain record including its tally, while `S` submits a saved draft after review.

The equivalent CLI commands are `forklab gov export <id> <file.json>`,
`forklab gov draft <id> --json`, and `forklab gov write <file.json> --document '<JSON>'`.
Use `forklab gov submit --template text --output <file.json>` to save a new template,
and `forklab gov submit <file.json>` to submit a saved draft. File commands refuse to
overwrite existing files unless `--force` is supplied.
