# Roadmap

Two phases. Phase 2 is gated on real-world traction — see the gate at the bottom.

## Phase 1 — MVP

The MVP is done when the 5-minute demo (install → break a deployment → diff → rollback → app recovers) works end-to-end.

| Etapa | Outcome | Status |
|------:|---------|--------|
| 1 | Repo scaffolded, project layout in place, both binaries compile | ✅ done |
| 2 | `pkg/types` + `internal/storage` (local FS) + `internal/agent` (informers, buffer, snapshot policy) | 🚧 in progress |
| 3 | `internal/delta` package: compute, apply, round-trip invariant, fuzz test, 100% coverage | ✅ done |
| 4 | CLI scaffolding (cobra) with `snapshot list`, `snapshot show`, `diff` | ⏳ |
| 5 | `blame` and `rollback` (single resource, optimistic-locking) | ⏳ |
| 6 | RBAC + ServiceAccount + NetworkPolicy + Helm chart + Dockerfile + minimal CI | ⏳ |
| 7 | README polish, Mermaid diagram, ADRs, demo recording, launch post draft | ⏳ |
| 8 | Public launch (repo public, video published, post live) | ⏳ |

**Strict scope-lock:** Deployments and ConfigMaps only. Local PVC storage only. No web UI. No metrics. No multi-cluster. Anything else goes into [TODO.md](../TODO.md) (created when needed) and is reconsidered post-launch.

## Phase 2 gate

Phase 2 work begins **only** if at least one of these is true after the launch:

- 50+ stars on GitHub
- 500+ likes on the launch post
- Genuine feature requests or issues from real users

If none of that happens, the project stays at v0.x and effort moves to the next experiment.

## Phase 2 — feature backlog (priority order)

When the gate opens, work proceeds feature-by-feature, each shipped in its own release and announced in its own post.

| P | Feature | Headline |
|--:|---------|----------|
| 0 | Extended resource support | Services, Ingress, Secrets (encrypted), StatefulSets, DaemonSets, NetworkPolicies, RBAC objects, plus a plugin hook for CRDs |
| 1 | Cloud storage drivers + lifecycle | S3 / GCS / Azure Blob, KMS at rest, cold-tier policies |
| 2 | Web visor | Go + HTMX (no React). Timeline, side-by-side diff, RBAC-aware auth |
| 3 | Metrics & observability | `/metrics` endpoint, Grafana dashboard in `docs/grafana/` |
| 4 | Multi-cluster | Central controller aggregates per-cluster agents |
| 5 | Slack / Teams alerts | Webhook on suspicious changes (after-hours, secret edits, NetworkPolicy deletes) |
| 6 | Cascading rollback with dependency analysis | Detect Deployment ↔ ConfigMap dependencies, suggest rollback set, mandatory dry-run |
| 7 | Automatic retention | Configurable policy (daily/weekly/monthly), delta compaction |
| 8 | Admission-controller integration | Block non-GitOps changes, label manual edits |
