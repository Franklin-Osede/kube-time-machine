# Architecture

> **Status: draft.** This file will hold the canonical architecture writeup for the MVP. The Mermaid diagram lands in Etapa 7.

## Components

```
                         ┌──────────────────────────────┐
                         │     Kubernetes API server    │
                         └──────────────────────────────┘
                                       ▲
                                       │ (1) watch via informers
                                       │
                         ┌──────────────────────────────┐
                         │          ktm-agent           │
                         │  (Deployment, in-cluster)    │
                         │                              │
                         │  informers ─► event buffer ──┼──► (2) flush every Nₛ
                         │                              │       as incremental delta
                         └──────────────┬───────────────┘
                                        │
                                        ▼
                         ┌──────────────────────────────┐
                         │   PersistentVolume (local)   │
                         │   snapshots/  deltas/        │
                         └──────────────┬───────────────┘
                                        │
                                        │ (3) read snapshots via API server
                                        │     (kubectl-style auth)
                                        ▼
                         ┌──────────────────────────────┐
                         │           ktm CLI            │
                         │   list · show · diff ·       │
                         │   blame · rollback           │
                         └──────────────────────────────┘
```

## Data model

- **Snapshot**: a point-in-time view of the watched resources. Two flavors:
  - **Full snapshot** — complete state of every watched resource. Taken every `snapshot.fullEvery` deltas.
  - **Delta snapshot** — only the diff from the previous snapshot (full or delta). Default cadence: every `snapshot.intervalSeconds`.
- Reconstruction at query time = `apply(full ⨁ delta₁ ⨁ delta₂ ⨁ … ⨁ deltaₙ)`. Bounded by `fullEvery`.

## Why deltas instead of full snapshots

Storage cost. A 200-resource cluster snapshotted every 5 minutes = 57,600 full snapshots/year. Deltas keep that bounded by *change rate* rather than *snapshot rate*. Same idea as git pack files or Docker image layers.

Trade-off: more code to get right (delta compute + apply + chain reconstruction). Mitigation: dedicated `internal/delta` package with exhaustive unit tests.

## Out of scope for MVP

- Multi-cluster aggregation
- Cloud-blob storage drivers
- Web visor
- Resource types beyond Deployments and ConfigMaps

See the project brief for the full out-of-scope list.
