# Comparison with existing tools

`kube-time-machine` exists because the tools below are good at what they do, but none of them is built around one question: **"what declarative state did this resource have over time, whoever changed it, and can I put just this one back?"** Several of them overlap with parts of that question, and the sections below say where.

KTM records the *declarative* state that persisted in the API server — `spec` plus user/tool-owned metadata, after admission — and excludes controller-owned `.status` (see [ADR-0005](adr/0005-declarative-state-recorder.md)). That framing is what most cleanly separates KTM from each tool below: it is not a backup (Velero), not a GitOps source of truth (Argo CD / Flux), not an event log (`kubectl events`), and not an observability stack (Prometheus / kube-state-metrics).

## vs Velero

[Velero](https://velero.io/) is a *backup* tool. It snapshots cluster state and persistent volumes for disaster recovery: the cluster died, restore it.

| | Velero | kube-time-machine |
|---|---|---|
| Primary goal | Disaster recovery | Forensics & selective rollback |
| What is captured | Full objects + persistent volumes | Declarative surface only (no `.status`, no PVs) |
| Captures `kubectl edit` | At the next scheduled backup | At the next flush (sampled) |
| Granularity of restore | Namespace, resource type or label selector; a single object can be selected by label | Single resource by kind, namespace and name |
| Storage | Cloud object store (mandatory) | Local PVC (MVP); cloud in Phase 2 |
| Setup cost | Significant (object store + IAM) | `helm install` |
| Diff between two points in time | No first-class support | First-class |

**Use Velero when** you need to restore a cluster, a namespace or a set of resources, including volumes, after a failure.
**Use kube-time-machine when** you need to see how one resource's declarative state changed between sampled points and put that one resource back.

## vs ArgoCD / Flux history

GitOps controllers like [ArgoCD](https://argo-cd.readthedocs.io/) and [Flux](https://fluxcd.io/) track the history of the *Applications / Kustomizations they manage*. They share a conceptual model with KTM — both separate declarative state from controller-owned status — but their system of record is Git, not the API server. Argo CD does detect live drift: a `kubectl edit` on a managed resource marks the Application OutOfSync and shows the live-versus-desired diff, and self-heal can revert it. What it does not keep is the sequence of live states the drift went through, and resources outside any Application are not tracked at all.

| | ArgoCD / Flux history | kube-time-machine |
|---|---|---|
| Source of truth | Git | The API server |
| Sees GitOps-driven changes | Yes | Yes |
| Detects live drift from Git on managed resources | Yes | No (it has no desired state to compare with) |
| Keeps the history of drifted live states | No | Yes, sampled |
| Tracks resources outside any Application | No | Yes |
| History granularity | Per sync and Git revision | Per resource, per sampled snapshot |
| Rollback | Re-sync to an earlier Git revision, per Application | One resource to an earlier snapshot |

**Use ArgoCD / Flux history when** you operate strictly via GitOps and want to know which sync introduced a change.
**Use kube-time-machine when** you need the sampled live state of a resource regardless of who or what changed it, including changes GitOps later reverted or never managed.

## vs `kubectl get events`

Events are the closest thing Kubernetes ships natively to a change log — but they expire (typically after one hour) and they describe *what happened*, not *what state existed before and after*.

| | `kubectl get events` | kube-time-machine |
|---|---|---|
| Persistence | ~1 hour by default | Bounded only by storage |
| Captures declarative resource state | No | Yes |
| Diffable | No | Yes |
| Rollback capability | No | Yes |

**Use events when** you need real-time signal during an incident.
**Use kube-time-machine when** the incident is more than an hour old, or when you need to compare states.

## vs Kubernetes audit logs

Kubernetes audit logs answer who called which API endpoint, with request and
response metadata depending on the cluster's audit policy. They are the right
source for security investigations, denied requests, and pre-admission request
bodies. They are not enabled or retained uniformly across providers, and
reconstructing one resource's effective state over time requires joining and
replaying API records.

| | Kubernetes audit logs | kube-time-machine |
|---|---|---|
| Primary goal | API security and compliance trail | Declarative-state forensics |
| Captures actor and request metadata | Yes | Partial (field managers observed, not proven authors) |
| Captures denied requests | Yes | No |
| Captures post-admission effective state | Policy-dependent | Yes |
| Per-resource diff/blame | Requires log processing | First-class |
| Selective rollback | No | Yes |

Use audit logs when the question is who called the API, including failed or
rejected requests. Use KTM when the question is which declarative state
actually persisted and how to inspect or restore it. In a mature production
environment, retain both.

## vs `kubectl rollout undo`

`kubectl rollout undo` asks a workload controller to return to a previous
ReplicaSet revision. It is fast and built in, but applies only to rollout-aware
workloads and does not provide a general history for ConfigMaps or arbitrary
declarative fields.

| | `kubectl rollout undo` | kube-time-machine |
|---|---|---|
| Deployment image rollback | Yes | Yes |
| ConfigMap rollback | No | Yes |
| Diff of declarative fields between two points | No | Yes |
| Per-resource change timeline | ReplicaSet revisions only | Yes, sampled; managers involved, not proven authors |
| Captures direct edits outside a rollout revision | Partial | Yes |
| Optimistic lock between preview and update | No | Yes |

Use `kubectl rollout undo` when a Deployment revision is known and reversing
the rollout is sufficient. Use KTM when you first need to discover what
changed, when multiple declarative fields changed, or when the target is not a
rollout revision.

## vs Prometheus / kube-state-metrics

[Prometheus](https://prometheus.io/) and [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics) are the canonical answer for *runtime* questions: "did the rollout finish?", "what was the available replica count five minutes ago?", "which Pod was in `CrashLoopBackOff`?". They report on the *observed* side of every object — the `.status` block, condition transitions, container restart counts. None of that is in scope for KTM.

| | kube-state-metrics + Prometheus | kube-time-machine |
|---|---|---|
| What is captured | Controller-owned status, runtime metrics | Declarative surface (`spec`, user metadata, ConfigMap `data`) |
| Answers "did the rollout fail?" | Yes | No |
| Answers "which image were the Pods running five minutes ago?" | Yes, from `kube_pod_container_info`, if retained | Not directly; it records the Deployment spec, not Pods |
| Keeps full Deployment specs and ConfigMap data over time | No | Yes, sampled |
| Granularity | Per-condition, per-counter | Per-resource declarative state |
| Retention | Defined by Prometheus storage policy | Defined by KTM storage policy |

**Use Prometheus / kube-state-metrics when** you need rollout health, condition transitions, or any other observed-side signal.
**Use kube-time-machine when** you need the declarative state that was written into the cluster, which field managers were involved, and how to restore one resource.

The two are complementary: kube-state-metrics tells you the cluster's runtime behaviour; KTM tells you the declarative inputs that produced that behaviour.
