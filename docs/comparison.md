# Comparison with existing tools

`kube-time-machine` exists because the tools listed below are good at what they do, but none of them answer **"what changed in this cluster, and can I roll back just that?"** in seconds.

KTM records the *declarative* state that persisted in the API server — `spec` plus user/tool-owned metadata, after admission — and excludes controller-owned `.status` (see [ADR-0005](adr/0005-declarative-state-recorder.md)). That framing is what most cleanly separates KTM from each tool below: it is not a backup (Velero), not a GitOps source of truth (Argo CD / Flux), not an event log (`kubectl events`), and not an observability stack (Prometheus / kube-state-metrics).

## vs Velero

[Velero](https://velero.io/) is a *backup* tool. It snapshots cluster state and persistent volumes for disaster recovery: the cluster died, restore it.

| | Velero | kube-time-machine |
|---|---|---|
| Primary goal | Disaster recovery | Forensics & selective rollback |
| What is captured | Full objects + persistent volumes | Declarative surface only (no `.status`, no PVs) |
| Captures `kubectl edit` | At next backup interval | Within the next flush window |
| Granularity of restore | Whole namespace / label selector | Single resource |
| Storage | Cloud object store (mandatory) | Local PVC (MVP); cloud in Phase 2 |
| Setup cost | Significant (object store + IAM) | `helm install` |
| Diff between two points in time | No first-class support | First-class |

**Use Velero when** you need to restore an entire cluster after a failure.
**Use kube-time-machine when** you need to know *what specifically* changed and undo it surgically.

## vs ArgoCD / Flux history

GitOps controllers like [ArgoCD](https://argo-cd.readthedocs.io/) and [Flux](https://fluxcd.io/) track the history of the *Applications / Kustomizations they manage*. They share a conceptual model with KTM — both separate declarative state from controller-owned status — but their system of record is Git, not the API server. If a Deployment lives outside the GitOps controller, or if someone bypasses it with `kubectl edit`, that change is invisible to GitOps history.

| | ArgoCD / Flux history | kube-time-machine |
|---|---|---|
| Source of truth | Git | The API server |
| Sees GitOps-driven changes | ✅ | ✅ |
| Sees `kubectl apply` outside GitOps | ❌ | ✅ |
| Sees `kubectl edit` / `kubectl patch` | ❌ | ✅ |
| Per-resource blame | ⚠️ (Application-level) | ✅ |
| Per-resource rollback | ⚠️ (Application-level revert) | ✅ |

**Use ArgoCD / Flux history when** you operate strictly via GitOps and want to know which sync introduced a change.
**Use kube-time-machine when** you need a ground-truth record regardless of who or what made the change.

## vs `kubectl get events`

Events are the closest thing Kubernetes ships natively to a change log — but they expire (typically after one hour) and they describe *what happened*, not *what state existed before and after*.

| | `kubectl get events` | kube-time-machine |
|---|---|---|
| Persistence | ~1 hour by default | Bounded only by storage |
| Captures declarative resource state | ❌ | ✅ |
| Diffable | ❌ | ✅ |
| Rollback capability | ❌ | ✅ |

**Use events when** you need real-time signal during an incident.
**Use kube-time-machine when** the incident is more than an hour old, or when you need to compare states.

## vs Prometheus / kube-state-metrics

[Prometheus](https://prometheus.io/) and [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics) are the canonical answer for *runtime* questions: "did the rollout finish?", "what was the available replica count five minutes ago?", "which Pod was in `CrashLoopBackOff`?". They report on the *observed* side of every object — the `.status` block, condition transitions, container restart counts. None of that is in scope for KTM.

| | kube-state-metrics + Prometheus | kube-time-machine |
|---|---|---|
| What is captured | Controller-owned status, runtime metrics | Declarative surface (`spec`, user metadata, ConfigMap `data`) |
| Answers "did the rollout fail?" | ✅ | ❌ |
| Answers "what was the image five minutes ago?" | ❌ | ✅ |
| Granularity | Per-condition, per-counter | Per-resource declarative state |
| Retention | Defined by Prometheus storage policy | Defined by KTM storage policy |

**Use Prometheus / kube-state-metrics when** you need rollout health, condition transitions, or any other observed-side signal.
**Use kube-time-machine when** you need to know what intent was written into the cluster, by whom or what, and over what window.

The two are complementary: kube-state-metrics tells you the cluster's runtime behaviour; KTM tells you the declarative inputs that produced that behaviour.
