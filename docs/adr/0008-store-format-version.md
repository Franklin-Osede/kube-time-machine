# 8. The store carries a format version

Date: 2026-11-08

## Status

Accepted.

## Context

Nothing on disk records which layout a store was written in. Today that costs nothing, because there has only ever been one layout. It becomes a problem the first time the layout changes after a public release: an older `ktm` pointed at a newer store would not know it is looking at something it cannot read. It would misread it and print wrong history, or, worse, the agent would "repair" it. `NewLocal` rebuilds a missing or corrupt `index.json` on open (ADR-0004), and rebuilding a newer store's index with an older build's idea of the layout corrupts it.

The check has to exist in the first public release. A build without it cannot be taught to refuse anything later, so every store and binary from v0.1.1 onwards has to carry it.

## Decision

Each store has a `format.json` at its root holding a single integer, `{"version": 1}`. `storage.FormatVersion` is the version a build reads and writes.

- **Both open paths check it first.** `NewLocal` and `OpenForRead` refuse a store whose version is higher than `FormatVersion` with `ErrUnsupportedFormat` and a message telling the user to upgrade. The check runs before `loadIndex`, and `NewLocal` creates only the root directory before it, so a refused store is never repaired or otherwise touched. The one file `NewLocal` may create first is the writer lock, which it takes before anything else and which holds no data.
- **A missing marker on a store with data means format 1, whatever this build's version is.** Stores written before the marker existed (v0.1.1-rc.1) are format 1. The writer stamps them with 1, never with `FormatVersion`: stamping old data with the current version would claim a migration that never happened, and no later build could detect it. A missing marker on an empty directory is a fresh store and gets `FormatVersion`. Only the writer adds the marker; `OpenForRead` keeps its guarantee of never writing (invariant 4).
- **An older version is refused until a migration exists.** While `FormatVersion` is 1 this cannot happen, but the check is already in place, so bumping the version cannot silently open older stores as the new layout. Whoever bumps it has to replace that refusal with a migration.
- **An unreadable or invalid marker is refused, not guessed.** It is written with `atomicWriteJSON`, so a torn write is not the explanation, and assuming a version could misread the whole store.
- **Bump the version only for incompatible changes.** Adding a field an older build can safely ignore does not need a bump. Changing the meaning of an existing file, or adding one an older build must understand to read history correctly, does, and that change gets its own ADR describing the migration.

## Alternatives considered

1. **A field in `index.json`.** Rejected: the index is a rebuildable cache (ADR-0004). Deleting it must lose nothing, so it cannot be the only record of the store's version.
2. **A version in every `meta.json` and payload.** Rejected for now: compatibility is a property of the whole store, not of one snapshot, and per-snapshot checks would have to agree on what a mixed store means. If a future migration rewrites snapshots one by one, that ADR can add per-snapshot versions on top of this marker.
3. **Detect the layout from its shape.** Rejected: it works until two layouts look alike, and it gives no clear error to the user.

## Consequences

**Easier**
- A future layout change can be released safely. Older builds refuse the new store with an actionable error instead of corrupting it.
- The rule for when to bump is written down before the first time it matters.

**Harder**
- One more file at the store root, and one more read on every open.
- Downgrading after a format bump requires the store to be migrated back or started fresh. That is the point of the check, but it has to be stated in the release notes of whichever version bumps it.

## Related

- [ADR-0004](0004-rebuildable-index.md) — why the index cannot hold the version, and the repair-on-open that makes refusing early necessary.
