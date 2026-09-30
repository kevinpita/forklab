# Fork mode

A forked lab takes over mainnet state. forklab downloads and extracts a
snapshot, exports its state with the chain binary, and rewrites the export.
The rewrite gives your local validators about 90% of the voting power,
creates a `gov` account with a delegation so that you can pass proposals, and
funds test accounts.

```sh
forklab lab create xrp-fork --profile xrplevm --version 11.2.0 --validators 2 --fork polkachu
```

## Snapshots

`--fork` takes one of these:

- A snapshot name from the profile's `snapshots` map, such as `polkachu`.
- A URL to a `.tar.lz4`, `.tar.gz`, or `.tar.zst` archive.
- A local archive file in one of those formats.

`--version` must be a binary version that can run the snapshot's state.

The built-in `polkachu` source resolves the archive link from Polkachu's
XRPL EVM snapshot page each time you create a lab. The cache uses that
dated archive URL, so a new daily snapshot does not reuse yesterday's
export. Polkachu's displayed node version can be stale; choose the binary
version currently running the chain.

The stock XRPL EVM 11.2.0 binary successfully exported Polkachu's snapshot
at height 7925443 and started a two-validator fork. XRPL EVM's export needs
the profile's genesis corrections, including matching ERC20 allowance
contract addresses to their token pairs. Fork mode replaces existing
validators' consensus keys and keeps `gen_txs` empty, avoiding the conflicting
validator updates produced by adding gentxs to an exported staking state.
No SDK changes or custom chain build are needed for this tested restore.

After the rewrite, the profile's `fork_patches` fix what the takeover leaves
inconsistent for that chain. See
[fresh_patches and fork_patches](profiles.md#fresh_patches-and-fork_patches).

## Disk space and the cache

> [!WARNING]
> Snapshots of a real chain are large. The disk must hold the archive, its
> extracted data, and the JSON export at the same time.

forklab caches the download and the export under `~/.forklab/snapshots/`, so
a failed or repeated `lab create` does not download again. It deletes the
extracted data after the export unless you pass `--keep-snapshot-work`.

## Chain id

A forked lab keeps the profile's chain id, which for `xrplevm` is the mainnet
id. Keep it for a faithful rehearsal, because upgrade handlers can branch on
the chain id. Pass `--chain-id` only when you want to change that.
