# 2. Use incremental deltas with periodic reference snapshots

Date: 2026-05-16

## Status

Accepted.

## Context

`kube-time-machine` needs to persist a long-running history of cluster state. The naive approach is to take a full snapshot of every watched resource at every flush interval and write it to disk. On a modest 200-resource cluster flushed every 5 minutes, that produces ~105,000 snapshots per year — most of them near-duplicates of the previous one, because clusters change infrequently.

Storage growing linearly with *snapshot rate* rather than *change rate* is unacceptable for a tool that is meant to live in-cluster on a PersistentVolume. Without a solution the agent forces operators to choose between short retention windows (lose forensic value) or unbounded disk consumption (lose operational viability).

## Decision

The agent persists **incremental deltas** between consecutive snapshots, anchored by **full reference snapshots taken every N deltas** (`snapshot.fullEvery` in `values.yaml`, default 12).

The delta format is set-difference based (Added / Modified / Removed), implemented in [`internal/delta`](../../internal/delta/delta.go). For a resource that did not change between two snapshots, nothing is written. For a resource that changed, the entire new payload is stored under `Modified` — we do **not** attempt field-level diffing within a single resource (see Consequences below for the trade-off).

Reconstruction of any historical state proceeds as:

```
state(t) = Apply(referenceSnapshot, delta₁, delta₂, ..., deltaₖ)
```

where the reference snapshot is the most recent full snapshot at or before `t`, and `k ≤ snapshot.fullEvery`. This bounds reconstruction cost by configuration rather than by elapsed time.

## Alternatives considered

1. **Full snapshots every interval.** Rejected: storage scales with snapshot rate, not change rate.
2. **Single full snapshot + deltas forever (no periodic full anchors).** Rejected: reconstruction cost grows unboundedly. A bug in any delta poisons all subsequent reconstructions with no recovery point.
3. **Field-level diffing within each modified resource.** Rejected for MVP: complex, slow (requires structured parsing), and yields modest gains since the dominant storage cost is *number of resources changed*, not *bytes per change*. Revisit in Phase 2 if delta sizes become a problem on real workloads.

## Consequences

**Easier**
- Storage cost tracks change rate. A quiet cluster produces near-empty delta files.
- Reconstruction cost is bounded by `snapshot.fullEvery`. Worst case is reading one full snapshot + N deltas.
- Reference snapshots act as recovery points: a corrupt delta only affects the window from the previous reference to the next.

**Harder**
- More code to get right (compute, apply, chain reconstruction). Mitigated by exhaustive unit tests and a fuzz test on the round-trip invariant in [`internal/delta/delta_test.go`](../../internal/delta/delta_test.go) and [`internal/delta/fuzz_test.go`](../../internal/delta/fuzz_test.go). Coverage is 100% of statements.
- Storing full payload on Modified leaves bytes on the table for very large, frequently-edited resources (e.g. a ConfigMap with a 1MB JSON value mutated in one field). Acceptable for MVP scope (Deployments and ConfigMaps, typical sizes < 10KB). Revisit if real-world telemetry says otherwise.
- `snapshot.fullEvery` is a tuning parameter operators have to understand. Documented in [`values.yaml`](../../deploy/helm/values.yaml).

## Related

- ADR-0001 (record architecture decisions).
