# 4. The on-disk index is a cache, not the source of truth

Date: 2026-05-23

## Status

Accepted. (Post-hoc — the decision was taken when `internal/storage/local.go` was written; this ADR captures the rationale and pins the invariant.)

## Context

The local filesystem store needs a fast way to answer "what snapshots exist, in what order?" — every CLI command (`ktm snapshot list`, `ktm diff`, `ktm blame`) starts with that question. Walking the snapshot directory on every call is O(n) and produces stat-storms on PVCs with hundreds of entries.

The natural answer is to maintain an index file alongside the per-snapshot directories. The question this ADR settles is: **can the index disagree with the directories, and if so, who wins?**

The on-disk layout is:

```
<root>/
  index.json
  snapshots/
    <id>/
      meta.json
      full.json    -- iff meta.kind == "full"
      delta.json   -- iff meta.kind == "delta"
```

Each `meta.json` carries the full `SnapshotMeta` (id, kind, timestamp, prevId for deltas). The payload — `full.json` or `delta.json` — sits next to it. Two of these three artefacts are required to read a snapshot; the index is required by none.

## Decision

`index.json` is a **cache** of the per-snapshot `meta.json` files. The per-snapshot files are the source of truth. If `index.json` goes missing, becomes corrupt, or falls out of sync, the store remains semantically intact: every snapshot is self-describing inside its own directory.

Read paths reflect this hierarchy:

- `Local.List(ctx)` consults the in-memory copy of `index.json`. This is the hot path for the CLI and runs in O(1) per call (plus a copy of the index slice).
- `Local.Get(ctx, id)` opens `snapshots/<id>/meta.json` and the payload file directly. **The index is not consulted.** A snapshot whose entry has been lost from the index is still fully readable by id.

Write paths maintain the invariant that the per-snapshot files are durable before the index is updated:

1. `writeSnapshot` creates the directory and writes `meta.json` and the payload via `atomicWriteJSON` (tempfile + rename) in sequence.
2. `appendIndex` then takes the in-memory lock, appends the new meta, sorts by timestamp, and writes the updated `index.json` atomically.

If the agent crashes between (1) and (2), the snapshot is on disk but the index is one entry short. A startup-time rebuild would close that gap. **The rebuild routine is not implemented today** — the format is designed to support it, and the consequence of not running it is purely cosmetic (a snapshot is invisible to `List` but reachable by id).

## Alternatives considered

1. **Index is the source of truth.** Rejected. A corrupted index file would silently make the store appear empty; a partial write of `index.json` would silently truncate history. The per-snapshot artefacts already exist on disk regardless — making the index authoritative would throw away durable data on a recoverable failure.

2. **No index — walk `snapshots/` on every `List`.** Rejected. Every CLI command would pay O(n) directory IO and one stat per entry. On a year-old PVC with thousands of snapshots this becomes noticeable, and the cost is paid even for the trivial `ktm snapshot list | head` case. The cache exists because the bound on store size grows with time.

3. **Distributed index — one `meta.json` per snapshot and nothing else.** This is essentially what we already do, minus the cache. The store would have to walk and parse every `meta.json` on every `List`. The cache wins on the hot read path; we keep the distributed `meta.json` files because they are also what makes the index rebuildable.

## Consequences

**Easier**

- `ktm rollback` (see [ADR-0006](0006-rollback-live-resourceversion.md)) and any other "open snapshot by id" path bypasses the index entirely, so an out-of-sync index never breaks rollback.
- A future garbage-collection or compaction routine can manipulate `snapshots/` and rebuild the index from scratch at the end, with no risk of leaving the store in a half-state.
- Snapshots are content-addressable by directory name. Manual triage (`ls snapshots/`, `cat snapshots/<id>/meta.json`) works without any KTM tooling.

**Harder**

- Two writes per snapshot persist (per-snapshot files + index). Each is atomic on its own; the pair is not. A crash between writes leaves the index one entry short of the directory listing — recoverable but only by a routine we have not written yet.
- No fsync is performed (see comment on `atomicWriteJSON`). Rename gives us crash-consistency at the page-cache level but not full durability against kernel crashes. Acceptable for MVP: the agent re-snapshots on restart, so a lost trailing snapshot reappears within one interval.

## Non-decision

The rebuild routine itself — when it runs (startup? on-demand subcommand?), what it logs, how it handles `meta.json` files that fail to parse — is deliberately left open. The format supports it; the operational shape can wait until we have evidence it is needed.

## Related

- [ADR-0002](0002-incremental-deltas-with-reference-snapshots.md) — defines `Snapshot` and `Delta`, the payloads each snapshot directory holds.
- [ADR-0006](0006-rollback-live-resourceversion.md) — relies on `Get(id)` being independent of the index.
