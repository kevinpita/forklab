# Fork mode

A forked lab takes over mainnet state. forklab downloads and extracts a
snapshot, exports its state with the chain binary, and rewrites the export.
The rewrite gives your local validators about 90% of the voting power,
creates a `gov` account with a delegation so that you can pass proposals, and
funds test accounts.

```sh
forklab lab create xrp-fork --profile xrplevm --version 11.1.1 --fork polkachu
```

## Snapshots

`--fork` takes one of these:

- A snapshot name from the profile's `snapshots` map, such as `polkachu`.
- A URL to a `.tar.lz4`, `.tar.gz`, or `.tar.zst` archive.
- A local archive file in one of those formats.

`--version` must be a binary version that can run the snapshot's state.

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
