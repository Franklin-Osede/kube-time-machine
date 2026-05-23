# Install

This page covers installing the KTM agent (the in-cluster component) via the bundled Helm chart, and operating the resulting pod. The CLI (`ktm`) is a local binary that does not require installation in the cluster — see [README.md](../README.md) for how to build it.

## Prerequisites

- A Kubernetes cluster (≥ 1.27 tested; OrbStack K8s 1.33 is the development target).
- `kubectl` configured against the target cluster.
- `helm` v3.x.
- An image of `ktm-agent` reachable from the cluster. For local development build it with `docker build -t ktm-agent:dev .` from the repo root; for a real install pull from a registry such as GHCR (publication via `release.yml` is Etapa-7 work and not landed yet).

## Default install

```bash
kubectl create namespace ktm-system
helm install ktm deploy/helm \
  --namespace ktm-system \
  --set image.repository=ghcr.io/franklin-osede/ktm-agent \
  --set image.tag=0.0.1
```

Verify:

```bash
kubectl -n ktm-system get pods,pvc,sa,networkpolicy
kubectl get clusterrole,clusterrolebinding | grep ktm
kubectl -n ktm-system logs deploy/ktm-kube-time-machine
```

The agent logs `informer caches synced` once it is ready to record. From that moment, every Add/Update/Delete on a watched Deployment or ConfigMap reaches the local buffer and is flushed to the PVC at the configured cadence (`snapshot.intervalSeconds`, default 300 s).

## Customising the install

The full schema is in [deploy/helm/values.yaml](../deploy/helm/values.yaml). The most relevant knobs:

| Value | Default | Notes |
|---|---|---|
| `snapshot.intervalSeconds` | `300` | Flush cadence in seconds. Smaller = lower MTTR for forensic queries, more storage. |
| `snapshot.fullEvery` | `12` | Every Nth flush is a full reference snapshot. Bounds chain-reconstruction cost (see [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md)). |
| `storage.size` | `10Gi` | PVC size. Storage scales with change rate, not snapshot rate. |
| `storage.storageClassName` | `""` | Empty means cluster default. |
| `agent.resources.*` | conservative | The agent's working set is dominated by the informer cache; tune up if you watch large clusters. |
| `networkPolicy.enabled` | `true` | Turn off only on clusters whose CNI does not enforce NetworkPolicy. |

There is no `replicaCount` value. The agent writes to a `ReadWriteOnce` PVC and `replicas` is hardcoded to `1` in the template. Two writers would corrupt the storage (see [ADR-0007](adr/0007-packaging-defaults.md)).

## The 30-second capture gap during `helm upgrade`

The Deployment uses `strategy.type: Recreate`, not the default rolling update. This is deliberate and load-bearing: a rolling update would briefly run two pods sharing the same RWO PVC, both racing to write the same files. `Recreate` guarantees the old pod terminates before the new one starts, at the cost of a ~30-second gap during upgrades where no events are captured.

If a change happens to the cluster during that gap it will surface in the next full reference snapshot regardless — KTM detects deletions and additions by comparing the set of keys across consecutive full snapshots, so the data is not lost, only the precise moment of the change is. For routine production use this is acceptable; for incident response while debugging KTM itself, schedule upgrades during quiet windows.

## Inspecting the PVC

The base image is `gcr.io/distroless/static-debian12:nonroot` — there is no shell, no `ls`, no package manager. To inspect the snapshots dir from outside the agent's process namespace, use `kubectl debug` with an ephemeral container:

```bash
POD=$(kubectl -n ktm-system get pod -l app.kubernetes.io/name=kube-time-machine -o jsonpath='{.items[0].metadata.name}')
kubectl -n ktm-system debug "$POD" --image=busybox:latest --target=agent -- \
  sh -c "ls -la /proc/1/root/var/lib/ktm/snapshots/ | tail"
DEBUG=$(kubectl -n ktm-system get pod "$POD" -o jsonpath='{.spec.ephemeralContainers[*].name}' | tr ' ' '\n' | tail -1)
kubectl -n ktm-system logs "$POD" -c "$DEBUG"
```

The `--target=agent` flag attaches the busybox container to the agent's PID/mount namespace; `/proc/1/root/var/lib/ktm/` is the agent's view of the PVC. The output is delivered to the ephemeral container's stdout, which `kubectl logs -c <name>` then surfaces.

For most uses, the same data is available via the CLI (`ktm snapshot list`, `ktm snapshot show <id>`) without needing to enter the cluster.

## RBAC: what the agent can and cannot do

The bundled `ClusterRole` grants `get`, `list`, `watch` on `deployments.apps` and `configmaps` cluster-wide. The agent has no other permissions — it cannot write, cannot read Secrets, cannot watch any other kind.

If you want to scope the agent down further (e.g. to specific namespaces), today the chart does not parameterise the rule subject lists; you would patch the ClusterRole after install. Scoping is a Phase 2 enhancement.

## Uninstall

```bash
helm uninstall ktm -n ktm-system
```

`helm uninstall` also removes the cluster-scoped `ClusterRole` and `ClusterRoleBinding` that the chart created (their names are suffixed with the release namespace to avoid collisions between installs). The PVC is removed along with the namespace; **the snapshots stored on it are deleted**. To preserve them, copy them off the PVC first or set up a backup of the storage class.

## Troubleshooting

- **`ImagePullBackOff`.** Either the image tag is not published yet or your cluster cannot reach GHCR. For local development pass `--set image.repository=ktm-agent --set image.tag=dev --set image.pullPolicy=Never` after `docker build`-ing locally.
- **Pod `Running` but no logs / no snapshots.** Check that the agent has watch permission: `kubectl -n ktm-system describe pod ...` for events, `kubectl auth can-i list deployments --as=system:serviceaccount:ktm-system:ktm-kube-time-machine`.
- **`kubectl exec` returns "no such file or directory"** for `ls`, `cat`, etc. Expected — distroless has no shell or coreutils. Use the `kubectl debug` recipe above.
- **Two installs collide on `ClusterRole`.** Shouldn't happen — the name includes the release namespace. If it does, check that you used `helm install` with `--namespace` set, not just `kubectl apply`-d the rendered chart with a namespace overlay.

## Related docs

- [docs/architecture.md](architecture.md) — the pipeline and data model.
- [docs/comparison.md](comparison.md) — how KTM relates to Velero, ArgoCD/Flux, events, observability.
- [docs/adr/](adr/) — the design decisions, especially [ADR-0005](adr/0005-declarative-state-recorder.md) (what KTM does and does not record) and [ADR-0007](adr/0007-packaging-defaults.md) (why the chart looks the way it does).
