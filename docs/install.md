# Install

KTM has two operating modes. Both share the same agent and CLI binaries and the same on-disk snapshot format — they differ in **where the storage lives**.

- **Mode A — local-first.** Run `ktm-agent` on your laptop against a kubeconfig, then query the same `--storage-dir` with `ktm`. Recommended for v0.1.1 and for incident-response workflows.
- **Mode B — continuous in-cluster recording.** Install the Helm chart so the agent runs as a Pod and writes to a PersistentVolume. To query that history with `ktm`, you currently have to extract the PVC contents to your laptop (recipe below) and point the CLI at the local copy.

## Prerequisites

- A Kubernetes cluster (≥ 1.27 tested; OrbStack K8s 1.33 is the development target).
- `kubectl` configured against the target cluster.
- For Mode B: `helm` v3.x.
- The CLI binary (`ktm`) and the agent binary (`ktm-agent`). Release binaries are not published yet; build them from source with `make build` from the repo root.

## Mode A — local-first agent

The agent runs as a regular process; `--kubeconfig` selects the cluster.

```bash
# In one terminal — agent records continuously.
./bin/ktm-agent \
  --kubeconfig ~/.kube/config \
  --storage-dir /tmp/ktm \
  --interval 10s \
  --full-every 3
```

`--interval 10s` and `--full-every 3` are demo-friendly defaults; for longer sessions the production defaults are `5m` and `12`. See [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md) for the cadence trade-offs.

```bash
# In another terminal — CLI queries the same dir the agent writes to.
./bin/ktm --storage-dir /tmp/ktm snapshot list
./bin/ktm --storage-dir /tmp/ktm diff --from <id> --to <id>
./bin/ktm --storage-dir /tmp/ktm blame deployment/<namespace>/<name>
./bin/ktm --storage-dir /tmp/ktm rollback deployment/<namespace>/<name> --to <id>
```

Stop the agent with `Ctrl-C`; it performs a 5-second best-effort final flush before exiting.

This is the path the launch demo uses — see [examples/demo-scenario/](../examples/demo-scenario/).

## Mode B — continuous in-cluster recording (Helm chart)

For longer-running deployments where you want history captured even when no laptop is attached, install the chart from the repository. The OCI chart and agent image are not published yet, so first build the image and make it available to your cluster:

```bash
docker build -t ktm-agent:dev .

helm install ktm deploy/helm \
  --namespace ktm-system --create-namespace \
  --set image.repository=ktm-agent \
  --set image.tag=dev \
  --set image.pullPolicy=Never
```

`image.pullPolicy=Never` works for local clusters whose container runtime can see the locally built image. For a remote cluster, push the image to a registry the cluster can access and set `image.repository`, `image.tag`, and `image.pullPolicy` accordingly.

Verify:

```bash
kubectl -n ktm-system get pods,pvc,sa,networkpolicy
kubectl get clusterrole,clusterrolebinding | grep ktm
kubectl -n ktm-system logs deploy/ktm-kube-time-machine
```

The agent logs `informer caches synced` once it is ready to record. From that moment, every Add/Update/Delete on a watched Deployment or ConfigMap reaches the local buffer and is flushed to the PVC at the configured cadence (`snapshot.intervalSeconds`, default 300 s).

### Querying history from Mode B (CLI ↔ PVC)

The CLI reads directly from the local `--storage-dir`; it does **not** speak to the agent or to the API server for the read commands (`snapshot list/show`, `diff`, `blame`). To query a history that lives in an in-cluster PVC, you currently have to copy the contents to your machine.

The base image is `gcr.io/distroless/static-debian12:nonroot` — no shell, no `tar`, no package manager. The supported recipe is `kubectl debug` with a busybox ephemeral container that shares the agent's mount namespace, plus `kubectl cp` to pull the directory:

```bash
POD=$(kubectl -n ktm-system get pod -l app.kubernetes.io/name=kube-time-machine -o jsonpath='{.items[0].metadata.name}')

# Attach an ephemeral container that can see the agent's PVC mount.
kubectl -n ktm-system debug "$POD" \
  --image=busybox:latest \
  --target=agent \
  --profile=general \
  -- sh -c "sleep 600" >/dev/null 2>&1 &

DEBUG=$(kubectl -n ktm-system get pod "$POD" -o jsonpath='{.spec.ephemeralContainers[*].name}' | tr ' ' '\n' | tail -1)

# Tar the snapshots directory into a local file via `kubectl exec`.
mkdir -p /tmp/ktm
kubectl -n ktm-system exec "$POD" -c "$DEBUG" -- \
  tar -C /proc/1/root/var/lib/ktm -cf - . | tar -xf - -C /tmp/ktm

# Query with the local CLI.
./bin/ktm --storage-dir /tmp/ktm snapshot list
```

For routine inspection this is operationally awkward; it is good enough for v0.1.1 forensics on a real incident. A `ktm proxy` subcommand that talks to the agent over the API server is a Phase 2 candidate, gated on real-world traction.

### Customising the install

The full schema is in [deploy/helm/values.yaml](../deploy/helm/values.yaml). The most relevant knobs:

| Value | Default | Notes |
|---|---|---|
| `snapshot.intervalSeconds` | `300` | Flush cadence in seconds. Smaller = lower MTTR for forensic queries, more storage. |
| `snapshot.fullEvery` | `12` | Every Nth flush is a full reference snapshot. Bounds chain-reconstruction cost (see [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md)). |
| `storage.size` | `10Gi` | PVC size. Storage scales with change rate, not snapshot rate. |
| `storage.storageClassName` | `""` | Empty means cluster default. Use a storage class that encrypts at rest — see [Security](#security-the-storage-is-confidential). |
| `agent.resources.*` | conservative | The agent's working set is dominated by the informer cache; tune up if you watch large clusters. |
| `networkPolicy.enabled` | `true` | Turn off only on clusters whose CNI does not enforce NetworkPolicy. |

There is no `replicaCount` value. The agent writes to a `ReadWriteOnce` PVC and `replicas` is hardcoded to `1` in the template. Two writers would corrupt the storage (see [ADR-0007](adr/0007-packaging-defaults.md)).

### The 30-second capture gap during `helm upgrade`

The Deployment uses `strategy.type: Recreate`, not the default rolling update. This is deliberate and load-bearing: a rolling update would briefly run two pods sharing the same RWO PVC, both racing to write the same files. `Recreate` guarantees the old pod terminates before the new one starts, at the cost of a ~30-second gap during upgrades where no events are captured.

If a change happens to the cluster during that gap it will surface in the next full reference snapshot regardless — KTM detects deletions and additions by comparing the set of keys across consecutive full snapshots, so the data is not lost, only the precise moment of the change is. For routine production use this is acceptable; for incident response while debugging KTM itself, schedule upgrades during quiet windows.

### RBAC: what the agent can and cannot do

The bundled `ClusterRole` grants `get`, `list`, `watch` on `deployments.apps` and `configmaps` cluster-wide. The agent has no other permissions — it cannot write, cannot read Secrets, cannot watch any other kind.

If you want to scope the agent down further (e.g. to specific namespaces), today the chart does not parameterise the rule subject lists; you would patch the `ClusterRole` after install. Scoping is a Phase 2 enhancement.

### Security: the storage is confidential

The PVC (Mode B) and the `--storage-dir` (Mode A) hold the full declarative state of every Deployment and ConfigMap in the cluster, accumulated over time. Treat that data as sensitive:

- **ConfigMaps often carry sensitive configuration** even though they are not Secrets — connection strings, internal hostnames, feature flags, tokens that should have been Secrets but weren't. All of it is recorded verbatim in the snapshots.
- **Secrets are *not* captured.** The bundled `ClusterRole` grants no access to `secrets`, so the agent cannot read them and they never reach storage. This is by design (see [ADR-0007](adr/0007-packaging-defaults.md)).
- **Use a StorageClass that encrypts at rest** for the PVC (`storage.storageClassName`). On managed clusters this usually means an encrypted EBS/PD/Disk class.
- **Restrict access to the data.** Anyone who can read the PVC — or the `kubectl debug` + `kubectl cp` extraction path documented above — can read the recorded state. Limit who can exec into the `ktm-system` namespace, and treat any copy extracted to a laptop as confidential: delete it once the incident is closed.
- **There is no retention or expiry in v0.1.x.** Snapshots accumulate until the volume fills; the confidential window is the entire lifetime of the volume. Automatic retention is a Phase 2 item (roadmap P7).

### Uninstall

```bash
helm uninstall ktm -n ktm-system
```

`helm uninstall` also removes the cluster-scoped `ClusterRole` and `ClusterRoleBinding` that the chart created (their names are suffixed with the release namespace to avoid collisions between installs). The PVC is removed along with the namespace; **the snapshots stored on it are deleted**. To preserve them, extract them first using the recipe above, or set up a backup of the storage class.

## Troubleshooting

- **`ImagePullBackOff`.** Either the image tag is not published yet or your cluster cannot reach GHCR. For local development pass `--set image.repository=ktm-agent --set image.tag=dev --set image.pullPolicy=Never` after `docker build`-ing locally.
- **Pod `Running` but no logs / no snapshots.** Check that the agent has watch permission: `kubectl -n ktm-system describe pod ...` for events, `kubectl auth can-i list deployments --as=system:serviceaccount:ktm-system:ktm-kube-time-machine`.
- **`kubectl exec` returns "no such file or directory"** for `ls`, `cat`, etc. Expected — distroless has no shell or coreutils. Use the `kubectl debug` recipe above.
- **Two installs collide on `ClusterRole`.** Shouldn't happen — the name includes the release namespace. If it does, check that you used `helm install` with `--namespace` set, not just `kubectl apply`-d the rendered chart with a namespace overlay.
- **`ktm snapshot list` returns nothing after a Helm install.** The CLI reads from the local `--storage-dir`, not from the cluster. Either switch to Mode A or follow the PVC extraction recipe above.

## Related docs

- [docs/architecture.md](architecture.md) — the pipeline and data model.
- [docs/comparison.md](comparison.md) — how KTM relates to Velero, ArgoCD/Flux, events, observability.
- [docs/adr/](adr/) — the design decisions, especially [ADR-0005](adr/0005-declarative-state-recorder.md) (what KTM does and does not record) and [ADR-0007](adr/0007-packaging-defaults.md) (why the chart looks the way it does).
