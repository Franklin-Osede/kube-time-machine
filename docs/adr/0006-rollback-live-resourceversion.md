# 6. Rollback obtains the live ResourceVersion at apply time

Date: 2026-05-20

## Status

Accepted.

## Context

`ktm rollback <kind>/<namespace>/<name> --to <snapshot-id>` must apply the historical state of a resource back to the cluster. Kubernetes enforces optimistic concurrency control via `metadata.resourceVersion`: every `Update` call must carry the resourceVersion the client expects the resource to be at, and the API server rejects the update with `409 Conflict` if the server-side value has since moved.

Our snapshots intentionally do **not** carry `resourceVersion` — the marshaller in `internal/agent/marshal.go` strips it as part of sanitisation (see ADR-0002 and the inline rationale in `sanitiseMeta`). That gives us deterministic, low-noise snapshots, but it also means the snapshot alone cannot construct a safe rollback `Update`.

Three options were considered to bridge the gap.

## Decision

The CLI fetches the live `ResourceVersion` from the cluster at rollback time and uses **exactly that** value for the subsequent `Update`. The rollback flow is:

1. Reconstruct the target snapshot from storage.
2. `Get` the current object from the cluster (also yields the live `ResourceVersion`).
3. Render a preview diff between the live object and the target payload.
4. Prompt the user for confirmation.
5. `Update` using the same `ResourceVersion` captured in step 2 — **no second `Get`**.

Step 5 deliberately reuses the `ResourceVersion` from step 2 rather than re-fetching after the prompt. If we re-fetched, we would silently absorb any change that happened between the preview and the apply, undermining the user's informed consent. The whole point of optimistic locking here is that the user approves a specific transition between two known states; if that transition can no longer happen unchanged, the apply must fail and the user must re-evaluate.

If step 2 returns `404 Not Found`, the resource no longer exists. The rollback falls back to `Create`. Before calling `Create` we strip server-owned fields the snapshot may still carry indirectly (none today, given sanitisation, but defensively: `ResourceVersion`, `UID`, `CreationTimestamp`, `Generation`, `ManagedFields`). Note: with [ADR-0005](0005-declarative-state-recorder.md), `.status` is also stripped upstream in `internal/agent/marshal.go`, so the create path no longer has to deal with it.

> **Amendment (2026-08-25):** recreation on `404` is opt-in. Without `--allow-create` the rollback stops with an error instead of calling `Create`: deleting a resource is usually deliberate, and undoing a deletion is a different operation from rolling a resource back, so it must not happen as a side effect. With `--allow-create` the flow is as described above. The recreated object is new — it gets a fresh UID — so anything that referenced the old one, such as an ownerReference, does not reattach.

> **Amendment (2026-11-08):** the object sent to the API server is no longer the raw snapshot payload. The recorder drops controller-owned annotations (Helm, Argo CD, Flux, `last-applied-configuration`; see `marshal.go`), so the snapshot says nothing about them, and a full `Update` with that payload deleted them from the live object without the preview showing it. Rollback now keeps the live value of every annotation the recorder does not store, strips KTM's own `ktm.io/*` annotations on both `Update` and `Create`, and renders the preview from the object it is about to send, so the user confirms exactly what is written.

## Alternatives considered

1. **Stop stripping `ResourceVersion` in `marshal.go`.** Rejected: reintroduces the per-flush metadata churn that produced spurious "modified" deltas before sanitisation existed. ADR-0002 explicitly accepted this trade-off and we are not unwinding it.

2. **Persist `ResourceVersion` in a separate metadata field on each `SnapshotMeta`.** Rejected for MVP: forces a change to the on-disk and wire formats (every snapshot dir now carries a `map[Key]ResourceVersion`), and the stored value is potentially stale by hours by the time a rollback runs — the rollback would still need a live read to validate it. The cost of the format change buys no real safety.

3. **Live fetch at apply time (this decision).** Accepted. No storage changes, sanitisation stays intact, the optimistic lock is enforced natively by the Kubernetes API server, the value is always fresh, and the only edge case (deleted resource → 404) is small and localised.

## Consequences

**Easier**
- `internal/storage`, `pkg/types`, and `marshal.go` are untouched.
- No on-disk format migration.
- The "did the resource change?" check is delegated to the API server, which is the canonical source of truth.

**Harder**
- The CLI now needs a Kubernetes client. The `buildKubeConfig` helper that lives in `cmd/agent/main.go` will be extracted into `internal/kubeclient/` so both the agent and the CLI share the same kubeconfig resolution order (explicit flag → in-cluster → `$KUBECONFIG` → `~/.kube/config`).
- The 404 path requires us to issue `Create` instead of `Update`, with a small extra sanitisation step on the snapshot payload to remove server-owned fields that would otherwise make the create call fail.
- The rollback flow must NOT re-fetch the resource after the user confirms. A second `Get` would corrupt the optimistic-lock semantics by absorbing changes the user never saw. This is documented as an invariant inline in the rollback code.

## Related

- [ADR-0001](0001-record-architecture-decisions.md) — established ADR practice.
- [ADR-0002](0002-incremental-deltas-with-reference-snapshots.md) — the sanitisation that makes step 2 (live `ResourceVersion`) necessary.
- [ADR-0005](0005-declarative-state-recorder.md) — strips `.status` upstream in `marshal.go`, so the rollback apply path doesn't need to filter it.
