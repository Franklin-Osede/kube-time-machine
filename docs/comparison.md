# Comparison with existing tools

`kube-time-machine` exists because the tools listed below are good at what they do, but none of them answer **"what changed in this cluster, and can I roll back just that?"** in seconds.

## vs Velero

[Velero](https://velero.io/) is a *backup* tool. It snapshots cluster state and persistent volumes for disaster recovery: the cluster died, restore it.

| | Velero | kube-time-machine |
|---|---|---|
| Primary goal | Disaster recovery | Forensics & selective rollback |
| Captures `kubectl edit` | At next backup interval | Within the next flush window |
| Granularity of restore | Whole namespace / label selector | Single resource |
| Storage | Cloud object store (mandatory) | Local PVC (MVP); cloud in Phase 2 |
| Setup cost | Significant (object store + IAM) | `helm install` |
| Diff between two points in time | No first-class support | First-class |

**Use Velero when** you need to restore an entire cluster after a failure.
**Use kube-time-machine when** you need to know *what specifically* changed and undo it surgically.

## vs ArgoCD history

[ArgoCD](https://argo-cd.readthedocs.io/) tracks the history of *Applications it manages*. If a deployment lives outside ArgoCD — or if someone bypasses ArgoCD with `kubectl edit` — that change is invisible to it.

| | ArgoCD history | kube-time-machine |
|---|---|---|
| Sees ArgoCD-driven changes | ✅ | ✅ |
| Sees `kubectl apply` outside ArgoCD | ❌ | ✅ |
| Sees `kubectl edit` / `kubectl patch` | ❌ | ✅ |
| Per-resource blame | ⚠️ (Application-level) | ✅ |

**Use ArgoCD history when** you operate strictly via GitOps and want to know which sync introduced a change.
**Use kube-time-machine when** you need a ground-truth record regardless of who or what made the change.

## vs `kubectl get events`

Events are the closest thing Kubernetes ships natively to a change log — but they expire (typically after one hour) and they describe *what happened*, not *what state existed before and after*.

| | `kubectl get events` | kube-time-machine |
|---|---|---|
| Persistence | ~1 hour by default | Bounded only by storage |
| Captures full resource state | ❌ | ✅ |
| Diffable | ❌ | ✅ |
| Rollback capability | ❌ | ✅ |

**Use events when** you need real-time signal during an incident.
**Use kube-time-machine when** the incident is more than an hour old, or when you need to compare states.
