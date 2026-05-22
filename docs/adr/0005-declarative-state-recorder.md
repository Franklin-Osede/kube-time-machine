# 5. KTM records declarative state, not observed state

Date: 2026-05-21

## Status

Accepted.

## Context

The conceptual split between declarative state and observed state is native to Kubernetes itself: for resources with a `spec`, `spec` represents desired/declarative state, while `status` represents observed state written by controllers. Some resources, like `ConfigMap`, expose their declarative surface through spec-equivalent fields such as `data` and `binaryData`. KTM is not inventing this division — it is recording one side of it. GitOps tools like Argo CD and Flux operate on the same split: they reconcile desired state from a source of truth and use `.status` for health and condition signalling, not as the system of record. KTM follows the same conceptual split, applied to the system-of-record side: it persists the declarative surface that effectively lived in the API server.

A KTM snapshot represents one moment in the life of a Kubernetes resource. The question this ADR settles is: **what is "one moment" — the full server-stored object, or a curated subset?**

Three forces apply.

1. **Diff and blame usability.** After a `kubectl set image`, the diff of two consecutive snapshots naturally shows four hunks: one meaningful (the image change) and three derived from the rollout — `observedGeneration`, `status.conditions[*].lastUpdateTime`, the ReplicaSet hash. Useful in some debugging contexts, but pure noise in the audit/intent-tracking context that motivates KTM. This was observed first-hand during the Etapa 2.3 smoke test on 2026-05-20 and is documented in [PROGRESS.md](../PROGRESS.md) as the status-noise risk.

2. **Storage economy.** `.status` blocks on Deployments dominate the per-snapshot size in the steady state. Across a cluster of 100 Deployments captured every 5 minutes, status payload alone accounts for the majority of bytes written.

3. **Product clarity.** What does "`ktm rollback`" mean if the snapshot includes `.status`? Status is controller-owned: a rollback that tried to restore it would either be ignored by the controller or fight it. The ergonomics force a filter at apply time. If we have to filter status out to use the data, why record it in the first place?

The earlier sanitisation in [ADR-0002](0002-incremental-deltas-with-reference-snapshots.md) (`metadata.resourceVersion`, `metadata.managedFields`, `metadata.generation`) was the first round of removing fields that the user does not own. `.status` is the same decision, scaled up to the whole controller-owned block.

## Decision

KTM records the **persisted declarative surface** of supported Kubernetes resources: user/tool-owned `metadata` (labels, annotations, name/namespace) plus `spec` or spec-equivalent fields (notably ConfigMap `data` and `binaryData`), **after** API-server admission/defaulting/mutating webhooks have run, **excluding** server-owned metadata and the top-level `.status` block.

"User/tool-owned" rather than "user-owned": KTM treats labels and annotations written by an operator or a GitOps controller exactly the same as those typed by a human at a terminal. They are all part of the declarative surface the API server persists.

Every persisted snapshot is the result of:

1. `DeepCopy` of the live informer object (so the informer's shared cache is never mutated).
2. `sanitiseMeta`: clear `ObjectMeta.ResourceVersion`, `ManagedFields`, `Generation` (ADR-0002).
3. `stripStatus`: marshal to JSON, delete the top-level `status` key, re-marshal.

`stripStatus` is implemented via map round-trip rather than struct field zeroing because Go's `encoding/json` `omitempty` tag does not apply to non-pointer struct values: setting `Status: appsv1.DeploymentStatus{}` would still serialise as `"status":{}` in the output. Round-tripping through `map[string]json.RawMessage` and `delete`-ing the key removes it cleanly. The map re-marshal is deterministic because `encoding/json` sorts map keys alphabetically.

### In product terms

- **Captured (Deployment):** `spec` changes — image tags, replicas, resource limits, env, probes, volumes, etc.
- **Captured (ConfigMap):** `data` and `binaryData` changes. ConfigMaps have no `.spec`; their declarative surface lives directly under those fields.
- **Captured (both):** user/tool-owned label and annotation changes — whatever any actor (GitOps, `kubectl`, an operator) wrote into the declarative surface via the API server.
- **Not captured:** rollout health, `observedGeneration`, condition transitions, ReplicaSet hashes, Pod runtime status, any controller-owned `.status` field.

## What this is NOT

Three things the framing might suggest but does not promise.

- **KTM does NOT capture what the user originally typed.** A mutating admission webhook (sidecar injector, image-mutator) modifies the object *before* the agent's watch sees it. KTM captures the post-mutation spec — the spec that effectively persisted. The pre-mutation request body is not recoverable from KTM. For pre-mutation forensics, the cluster's audit log is the right source.

- **KTM does NOT capture observed-state forensics.** "Did this rollout fail?", "Which condition fired when?", "What was the available replica count five minutes ago?" — all out of scope. Standard observability tooling (Prometheus, kube-state-metrics, the events API) is the right tool for those questions, and KTM is intentionally complementary to them.

- **KTM does NOT replace a backup.** KTM is not Velero. The agent does not capture Secrets, PersistentVolumes, CRDs (yet), and excludes the runtime portion of even the resources it does capture.

## Alternatives considered

1. **Record the full object including `.status`.** Rejected. Diff and blame would be dominated by rollout noise; rollback semantics get confused (controllers will overwrite anything user-side tries to set on `.status`); storage cost balloons; and product positioning vs Velero/observability stops being defensible.

2. **Record `.status` but filter it at view time** (e.g. a `--no-status` flag on `ktm diff`). Rejected for MVP. Every future view (blame, web UI in Phase 2, alerts) would have to repeat the filter, and the decision would migrate from "what KTM is" to "how to look at it" — a category error. If the trade-off ever needs to flip, an opt-in observed-state recording mode is cleaner than retroactively un-filtering over-recorded data.

3. **Make status-stripping a runtime flag on the agent.** Rejected: same problem as (2) plus operational confusion. KTM either is or is not a declarative recorder; making it "depends on `--strip-status`" splits the product into two products.

## Consequences

**Easier**

- Diffs focus on intent. The five-minute demo (`kubectl set image` → `ktm diff` → `ktm rollback`) shows one hunk per change, not four.
- Storage cost drops materially in the steady state — `.status` is by far the largest source of post-sanitisation byte volume.
- Product positioning becomes precise: KTM tracks the declarative history of a cluster, complementary to GitOps (which shows the desired state in Git) and to observability (which shows runtime). It does not compete with backup tools.
- The rollback contract from [ADR-0006](0006-rollback-live-resourceversion.md) gets cleaner: the snapshot the user is reverting to is already free of controller-owned state, so the create-on-404 path no longer has to defensively strip `.status` from the snapshot payload (though it must still strip `metadata.uid` and `metadata.creationTimestamp`, which `sanitiseMeta` does not touch yet).

**Harder**

- Snapshots taken before this change are no longer byte-equivalent to snapshots taken after, for two orthogonal reasons: (a) `.status` is no longer present in the persisted JSON, and (b) the top-level key order changes from struct-field-declaration order to alphabetical order because `stripStatus` round-trips through a `map`. The first flush after the upgrade records one spurious `modified` delta per resource as a result. Acceptable: the MVP has no users, and the noise is one-shot. This is intentionally not solved by introducing a canonical-JSON serializer globally — that would be more conceptual surface than the MVP needs.
- The recorded data is strictly less than the full object. Any future feature that wants observed-state forensics will need a deliberate, opt-in extension — it cannot rely on KTM data alone.

## Non-decision

A future observed-state mode may retain `.status` for rollout forensics. The shape of such a feature (per-snapshot, per-resource, chart-level toggle, or separate parallel store) is deliberately not designed here.

## Related

- [ADR-0002](0002-incremental-deltas-with-reference-snapshots.md) — first round of sanitisation (`metadata` fields).
- [ADR-0006](0006-rollback-live-resourceversion.md) — rollback uses the live `ResourceVersion`; this ADR strips `.status` upstream, so the rollback apply path doesn't need to filter `.status` from the payload at apply time.
