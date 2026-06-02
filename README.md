# kube-time-machine

> **Git blame & time-travel for your Kubernetes cluster.**
> See what changed, when, and roll it back — in seconds.

[![Status](https://img.shields.io/badge/status-pre--MVP-orange.svg)](#roadmap)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev/)

> **v0.1.0 shipped.** Multi-arch agent image, Helm chart, and CLI binaries are published — see [docs/install.md](docs/install.md) and the [releases page](https://github.com/Franklin-Osede/kube-time-machine/releases). [Roadmap](#roadmap) tracks what comes next.

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

`kube-time-machine` fills the gap between *backup tools* (Velero) and *GitOps history* (ArgoCD): it gives you a queryable timeline of every declarative change, regardless of who or what made it.

v0.1.0 is built for incident response on your laptop: run `ktm-agent` to record, then `ktm` to list, diff, blame, and roll back — both use the same local `--storage-dir`. For continuous recording in the cluster, install the Helm chart; querying that history still means copying the PVC data to your machine (see [Quick start](#quick-start)).

## What it does

KTM records the declarative state that lived in your Kubernetes API server over time. It captures changes to Deployment `spec` and ConfigMap `data`/`metadata`, whether they came from GitOps, `kubectl`, or an operator. It lets you diff, blame, and roll back that state. It does **not** capture rollout health, Pod runtime state, or controller-owned `.status` — observability tooling (Prometheus, kube-state-metrics) covers that.

- **Captures** the declarative surface of Deployments and ConfigMaps via `client-go` informers, with no external dependencies. Delta-compressed; periodic reference snapshots cap reconstruction cost.
- **Diffs** any two points in time — by namespace, by resource, or across the whole cluster.
- **Rolls back** a single resource to any prior state, with native optimistic-concurrency safety (live `ResourceVersion`).

## Quick start

KTM ships two binaries that share a `--storage-dir`. The agent writes there; the CLI reads from there.

### Mode A — local-first (recommended for v0.1.0)

Run the agent against your kubeconfig, then query with the CLI from the same machine. This is the path the launch demo uses.

```bash
# Download the v0.1.0 binaries for your platform from the Releases page,
# or build from source with `make build`.
ktm-agent --kubeconfig ~/.kube/config --storage-dir /tmp/ktm \
          --interval 10s --full-every 3 &

# In another terminal:
ktm --storage-dir /tmp/ktm snapshot list
ktm --storage-dir /tmp/ktm diff --from <id> --to <id>
ktm --storage-dir /tmp/ktm blame deployment/default/api
ktm --storage-dir /tmp/ktm rollback deployment/default/api --to <id>
```

### Mode B — continuous in-cluster recorder (Helm chart)

For longer-running deployments, install the chart and let the agent record continuously. The CLI does **not** speak to the agent over the API server — to query the history from this mode, you currently have to copy the PVC contents to your laptop and point the CLI at the local copy.

```bash
helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine \
  --version 0.1.0 \
  --namespace ktm-system --create-namespace
```

See [docs/install.md](docs/install.md) for the full chart parameters, the recipe to extract PVC contents, and the security notes about the data the PVC holds.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the full pipeline diagram, on-disk layout, and ADR cross-references.

Three components:

1. **`ktm-agent`** — watches Deployments and ConfigMaps via `client-go` informers and persists incremental deltas to a `--storage-dir`. Runs either on your laptop (Mode A) or in a Pod backed by a PersistentVolume (Mode B). Reference snapshots are taken every N deltas to bound reconstruction cost (Docker-layer pattern).
2. **`ktm` (CLI)** — local binary. Read commands (`snapshot list/show`, `diff`, `blame`) read directly from the local `--storage-dir`; they do **not** talk to the agent or to the API server. `rollback` is the only command that touches the cluster: it Gets the live object via the API server, shows a preview diff, then Updates with the captured `ResourceVersion`.
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
