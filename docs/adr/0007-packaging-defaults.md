# 7. Packaging defaults for the in-cluster agent

Date: 2026-05-20

## Status

Accepted.

## Context

Etapa 6 ships the artefacts that take `ktm-agent` from "compiles on my laptop" to "installable in a real cluster": a container image, a Helm chart, the RBAC the agent needs, a NetworkPolicy, and a minimal CI pipeline. The roadmap is explicit on scope-lock: this etapa must not pull in features beyond packaging the MVP we already have.

Six packaging decisions need to be made up-front because each one is hard to reverse once published.

## Decision

### 1. Base image: `gcr.io/distroless/static-debian12:nonroot`

The agent is a statically-linked Go binary (`CGO_ENABLED=0`). Distroless static gives us CA certificates for TLS to the kube-apiserver, runs as a non-root user out of the box, and has no shell or package manager. Total image footprint is ~2 MB plus the binary.

Rejected: `scratch` (no CA certs — TLS to the apiserver would need manual cert copy), `alpine` (musl/glibc edge cases, larger surface, root by default).

### 2. Only the agent is published as an image

`ktm` (the CLI) runs outside the cluster against the user's local kubeconfig. It is distributed as a Go binary via GitHub Releases when we cut one — there is no operational scenario where `docker run ktm` is the natural entry point, because the CLI needs the user's credentials and a path the user controls. Maintaining a second image would double the publication surface for zero added value at MVP.

### 3. RBAC: a single read-only `ClusterRole`

The agent's `ServiceAccount` is bound to a `ClusterRole` with `get`, `list`, `watch` on `deployments.apps` and `configmaps`. Nothing more.

Rollback writes to the cluster — but [ADR-0006](0006-rollback-live-resourceversion.md) determined that rollback is executed by the CLI under the user's own kubeconfig, never by the agent. The agent therefore needs **no** write verbs, ever. Adding `patch`/`update` "for the future" would be a dead permission from day one.

Cluster-scoped (not namespace-scoped) because the product value is "see the whole cluster" — a namespace-restricted agent contradicts the use case.

### 4. NetworkPolicy: deny ingress, allow DNS, allow all egress; gated by `networkPolicy.enabled` (default `true`)

The agent exposes no ports, so `Ingress: []` (deny-all) is unconditionally correct.

> **Amendment (2026-06-10):** the agent now exposes a single health port for Kubernetes liveness/readiness probes (`agent.health`, default `:8080`). Ingress is therefore no longer unconditional deny-all — the policy allows that one port (and nothing else) so probes succeed on CNIs that filter node→pod traffic (e.g. Cilium), and falls back to deny-all when the health server is disabled. The endpoint serves only `/healthz` and `/readyz`, never snapshot data, so this opens no confidential surface.

Egress is trickier: a strict "only kube-apiserver" rule is portable in theory but brittle in practice — the apiserver endpoint moves between cluster types (ClusterIP in kind/OrbStack, external LB in EKS/GKE), and the chart cannot derive it generically. The MVP ships an explicit allow-DNS + allow-all-egress rule with a comment in the template explaining the trade-off, so the policy is a clear signal-of-intent without breaking on any cluster.

The flag `networkPolicy.enabled` lets users opt out on clusters whose CNI doesn't support NetworkPolicy (vanilla kindnet, flannel without the extension). Tightening egress is a Phase 2 task once we have real deployment data.

### 5. Workload: `Deployment`, `replicas: 1`, `strategy.type: Recreate`, PVC `ReadWriteOnce`

The agent writes JSON files to a local PVC. Two concurrent writers would corrupt the storage. Setting `replicas: 1` is necessary but not sufficient: the default rolling update strategy would briefly run two pods during `helm upgrade`, both racing for the same `ReadWriteOnce` PVC. `Recreate` guarantees the old pod terminates before the new one starts; the ~30 s gap during upgrades is acceptable (events landing during the gap are lost, but the cluster carries on).

A `StatefulSet` with `volumeClaimTemplates` was considered. For a single replica it adds ceremony without buying anything; the PVC becomes harder to inspect under its expected name.

The `values.yaml` documents that `replicas` must remain `1` until a storage backend that supports concurrent writers exists (a Phase 2 P1 item).

### 6. CI in this etapa: only `ci.yml`

`.github/workflows/ci.yml` runs `gofmt`, `go vet`, `go test ./...`, `go test -race`, and `make build` on push/PR to `main`. That is the minimum needed to keep the tree green while we iterate.

`release.yml` (image publication to GHCR, multi-arch binaries, provenance, release-tag triggers) is deferred to Etapa 7. It pairs naturally with the demo recording and launch-post polish work that's already scoped for that etapa, and it requires repository secrets (`GHCR_TOKEN`) that aren't worth wiring up before the chart and Dockerfile are validated end-to-end.

## Consequences

**Easier**

- One image to publish, one workload type to support, one RBAC bundle to reason about.
- The chart works on any conformant cluster (kind, OrbStack, EKS, GKE) without per-provider tweaking.
- Least-privilege RBAC: a compromised agent pod can read two resource kinds and nothing else.

**Harder**

- No automatic image publication yet — smoke-testing the chart against a real cluster requires `docker build` locally and `helm install` with `image.tag` pointing at the local image.
- Helm upgrades cause a ~30 s capture gap because of `Recreate`. Worth a line in the README when we write the install instructions.
- Tightening egress in the NetworkPolicy is a known follow-up; until then the policy is more "signal" than "shield" on its egress side.

## Related

- [ADR-0001](0001-record-architecture-decisions.md) — establishes ADR practice.
- [ADR-0006](0006-rollback-live-resourceversion.md) — fixes that rollback is CLI-side, which is what lets the agent's RBAC stay read-only.
