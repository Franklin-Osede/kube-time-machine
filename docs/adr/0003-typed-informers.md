# 3. Typed informers over dynamic informers for MVP

Date: 2026-05-23

## Status

Accepted. (Post-hoc — the decision was taken when `internal/agent/informers.go` was written; this ADR captures the rationale.)

## Context

`client-go` offers two informer flavours for watching Kubernetes resources.

1. **Typed informers** via `informers.NewSharedInformerFactory(client, resync)`, accessed through generated subfactories per group/version: `factory.Apps().V1().Deployments().Informer()`, `factory.Core().V1().ConfigMaps().Informer()`. Event handlers receive `*appsv1.Deployment` / `*corev1.ConfigMap`.

2. **Dynamic informers** via `dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, resync)`. Event handlers receive `*unstructured.Unstructured` and the watched GVRs are configured at runtime.

The MVP watches two well-known kinds (Deployments, ConfigMaps). Phase 2 P0 includes extended resource support (Services, Ingress, Secrets, StatefulSets, DaemonSets, NetworkPolicies, RBAC objects) plus a plugin hook for CRDs — see [roadmap.md](../roadmap.md). The choice between typed and dynamic is most relevant for Phase 2; for MVP we pick the one that ships faster with the least sharp edges.

## Decision

`internal/agent/informers.go` uses typed informers. The factory is constructed once per agent process; one informer is registered per kind, with handlers that type-assert against the concrete pointer types (`*appsv1.Deployment`, `*corev1.ConfigMap`).

The boundary between typed Kubernetes objects and the internal `delta.State` model lives in `internal/agent/marshal.go`. A comment at the top of that file marks it as the only place that needs to change if we ever swap typed for dynamic informers — the rest of the codebase speaks the typed-`*appsv1.Deployment` ↔ `delta.Key`/`delta.State` boundary, not the informer plumbing.

## Alternatives considered

1. **Dynamic informers from day one.** Rejected for MVP. Dynamic returns `*unstructured.Unstructured`, which means every read of a field is `obj.Object["spec"].(map[string]any)["replicas"]` — boilerplate, runtime type assertions, and no compile-time check that the field path is real. The product would compile fine and break at runtime if the API surface evolved. For two kinds, the typed path is strictly cheaper.

2. **Hybrid: typed for the two MVP kinds + dynamic registered for future CRDs.** Rejected for MVP. Adds two informer-factory implementations to maintain when Phase 2 hasn't started. If and when CRD support lands, the cost of adding a dynamic factory alongside the typed one is well-bounded; doing it pre-emptively is YAGNI.

3. **Polling via `List` on a timer instead of informers.** Rejected: gives up the cache, gives up the event semantics (no add/update/delete distinction), and re-implements what informers already do well. The agent's only advantage over a polling scraper is informer-backed change detection — abandoning it would erode the value proposition.

## Consequences

**Easier**

- Type-safe event handlers. `obj.(*appsv1.Deployment)` either succeeds or the runtime panics loudly; a path that compiles is a path whose field accesses are real.
- `MarshalDeployment` / `MarshalConfigMap` work on typed pointers, so the sanitisation in `sanitiseMeta` is a normal field assignment (`m.ResourceVersion = ""`) instead of a `delete(obj.Object["metadata"].(map[string]any), "resourceVersion")` dance.
- `DeepCopy` is generated for each typed kind. With dynamic, we'd either lose the deep-copy guarantee or pay extra to round-trip through JSON.
- No `RESTMapper` / `DiscoveryClient` is needed. Typed informers know their GVRs at compile time.

**Harder**

- Phase 2 extended resource support means either generating typed informers for each new kind (extra dependencies on the relevant API packages) or migrating to dynamic. The `marshal.go` boundary is the deliberate seam: if we migrate, only that file changes.
- CRD support (Phase 2 plugin hook) cannot be served by typed informers alone — typed informers require Go types known at compile time, and CRDs are by definition runtime-discovered. When the time comes, the natural shape is a dynamic informer factory operating in parallel with the typed one, sharing the same `Buffer`.

## Related

- [ADR-0002](0002-incremental-deltas-with-reference-snapshots.md) — the delta model that sits downstream of the marshal boundary.
- [ADR-0005](0005-declarative-state-recorder.md) — the sanitisation policy applied at the marshal boundary; the typed-vs-dynamic question doesn't change what is stripped, but typed makes the strip cheaper.
