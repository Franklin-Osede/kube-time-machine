# kube-time-machine

> **Git blame & time-travel for your Kubernetes cluster.**
> See what changed, when, and roll it back — in seconds.

[![Status](https://img.shields.io/badge/status-pre--MVP-orange.svg)](#roadmap)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8.svg)](https://go.dev/)

> **Heads-up.** This repository is in active early development. The MVP is not yet shippable — see [Roadmap](#roadmap) for what works today.

---

## The 3 AM problem

It's 3 AM. Your API is down. The first question every SRE asks is the same: **"what changed?"**

Today, answering it means stitching together:
- ArgoCD history (if you use it)
- Commits across config repositories
- GitHub Actions / GitLab CI logs
- Cluster events — which expire after about an hour
- `last-applied-configuration` annotations on every resource
- App logs, on the off chance someone ran `kubectl edit` by hand

Half an hour gone before you have a hypothesis. Meanwhile the app is still down.

And if someone *did* `kubectl edit deployment` directly, that change is **invisible** in any centralized history. No trail. No git blame.

`kube-time-machine` fills the gap between *backup tools* (Velero) and *GitOps history* (ArgoCD): it gives you a queryable, in-cluster timeline of every change, regardless of who or what made it.

## What it does

KTM records the declarative state that lived in your Kubernetes API server over time. It captures changes to Deployment `spec` and ConfigMap `data`/`metadata`, whether they came from GitOps, `kubectl`, or an operator. It lets you diff, blame, and roll back that state. It does **not** capture rollout health, Pod runtime state, or controller-owned `.status` — observability tooling (Prometheus, kube-state-metrics) covers that.

- **Captures** the declarative surface of Deployments and ConfigMaps via `client-go` informers, with no external dependencies. Delta-compressed; periodic reference snapshots cap reconstruction cost.
- **Diffs** any two points in time — by namespace, by resource, or across the whole cluster.
- **Rolls back** a single resource to any prior state, with native optimistic-concurrency safety (live `ResourceVersion`).

## Quick start

> **Coming in MVP.** Helm chart and CLI binaries are not published yet. Track progress in [Roadmap](#roadmap).

Once shipped, the flow will be:

```bash
helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine
ktm snapshot list
ktm diff --from <id> --to <id>
ktm rollback deployment/api
```

## Architecture

> Diagram coming with the MVP — see [docs/architecture.md](docs/architecture.md).

Three components:

1. **`ktm-agent`** — runs in-cluster, watches resources via `client-go` informers, persists incremental deltas to a PersistentVolume on a configurable interval. Reference snapshots are taken every N deltas to bound reconstruction cost (Docker-layer pattern).
2. **`ktm` (CLI)** — local binary, talks to the agent via the Kubernetes API server (kubectl-style). All read commands plus single-resource rollback with mandatory confirmation.
3. **Web visor** — *not in MVP.* Gated on real-world traction. See [Roadmap](#roadmap).

## Comparison

| | kube-time-machine | Velero | ArgoCD history | `kubectl get events` |
|---|---|---|---|---|
| Purpose | Forensics & selective rollback | Backup & disaster recovery | GitOps deployment history | Real-time event log |
| Captures `kubectl edit` changes | ✅ | ✅ (in next backup) | ❌ | ✅ (briefly) |
| Per-resource history | ✅ | ❌ (whole-cluster snapshots) | ✅ (only ArgoCD-managed) | ❌ |
| Selective rollback | ✅ (single resource) | ⚠️ (per-resource possible but coarse) | ✅ (Application-level) | N/A |
| Retention beyond ~1h | ✅ | ✅ | ✅ | ❌ |
| Survives without GitOps | ✅ | ✅ | ❌ | ✅ |

See [docs/comparison.md](docs/comparison.md) for the long-form discussion.

## Roadmap

**Phase 1 — MVP.** Single-resource forensics for Deployments and ConfigMaps with local PVC storage, CLI, RBAC-minimal Helm install. Tracked in [docs/roadmap.md](docs/roadmap.md).

**Phase 2.** Triggered only on real traction (50+ stars, genuine user requests). Adds extended resource support, cloud storage, web visor, metrics, multi-cluster, Slack integration.

## Contributing

The project is pre-MVP. Issues are welcome; PRs are easier to land once the core architecture stabilizes (~end of Phase 1). If you have feedback on the design, [open an issue](https://github.com/Franklin-Osede/kube-time-machine/issues) — that's the most useful contribution right now.

## License

MIT — see [LICENSE](LICENSE).

## About the author

Built by **Franklin Osede** — backend engineer transitioning into Platform Engineering, learning Go in public.
[LinkedIn](https://www.linkedin.com/in/franklinosede/) · [GitHub](https://github.com/Franklin-Osede)
