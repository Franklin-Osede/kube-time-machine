# Install

KTM has two operating modes. Both share the same agent and CLI binaries and the same on-disk snapshot format — they differ in **where the storage lives**.

- **Mode A — local-first.** Run `ktm-agent` on your laptop against a kubeconfig, then query the same `--storage-dir` with `ktm`. Recommended for v0.1.1 and for incident-response workflows.
- **Mode B — continuous in-cluster recording.** Install the Helm chart so the agent runs as a Pod and writes to a PersistentVolume. To query that history with `ktm`, you currently have to extract the PVC contents to your laptop (recipe below) and point the CLI at the local copy.

## Prerequisites

- A Kubernetes cluster. Tested on kind with its default node image (Kubernetes v1.36.1 on the test run); the version is not pinned and others are untested.
- `kubectl` configured against the target cluster.
- For Mode B: Helm 3.
- The CLI binary (`ktm`) and the agent binary (`ktm-agent`), installed as below.

## Installing the binaries

Published builds are attached to each [GitHub release](https://github.com/Franklin-Osede/kube-time-machine/releases) with a `checksums.txt`. Until the stable `v0.1.1` is released, the newest verified build is the `v0.1.1-rc.3` prerelease.

```bash
VERSION=v0.1.1-rc.3
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
BASE=https://github.com/Franklin-Osede/kube-time-machine/releases/download/$VERSION

mkdir -p "$HOME/.local/ktm" && cd "$HOME/.local/ktm"
curl -fsSL -O "$BASE/ktm-$OS-$ARCH" -O "$BASE/ktm-agent-$OS-$ARCH" -O "$BASE/checksums.txt"
shasum -a 256 -c --ignore-missing checksums.txt
mv "ktm-$OS-$ARCH" ktm && mv "ktm-agent-$OS-$ARCH" ktm-agent && chmod +x ktm ktm-agent
export PATH="$HOME/.local/ktm:$PATH"
```

Builds exist for Linux and macOS on amd64 and arm64, and for Windows on amd64 (`ktm-windows-amd64.exe`, `ktm-agent-windows-amd64.exe`). On Linux without `shasum`, use `sha256sum -c --ignore-missing checksums.txt`.

To build from source instead, run `make build` from the repository root; it writes `bin/ktm` and `bin/ktm-agent`.

## Mode A — local-first agent

The agent runs as a regular process; `--kubeconfig` selects the cluster.

```bash
# In one terminal — the agent records until you stop it.
STORE="$HOME/ktm-store"
ktm-agent --kubeconfig ~/.kube/config --storage-dir "$STORE" \
  --interval 10s --full-every 3
```

`--interval 10s` and `--full-every 3` are demo-friendly defaults; for longer sessions the production defaults are `5m` and `12`. See [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md) for the cadence trade-offs.

```bash
# In another terminal — the CLI reads the directory the agent writes to.
STORE="$HOME/ktm-store"
ktm --storage-dir "$STORE" snapshot list

# Pick two IDs from the list, oldest first, and a resource to inspect.
FROM=20261108T205130467Z
TO=20261108T205135497Z
ktm --storage-dir "$STORE" diff --from "$FROM" --to "$TO"
ktm --storage-dir "$STORE" blame deployment/default/api
ktm --storage-dir "$STORE" rollback deployment/default/api --to "$FROM"
```

The IDs above are examples; use the ones `snapshot list` prints. The [README quick start](../README.md#quick-start) captures them into variables automatically.

Stop the agent with `Ctrl-C`; it performs a 5-second best-effort final flush before exiting, and skips it if the informer caches never synced, so an empty cache never becomes a snapshot.

A scripted walkthrough with a Deployment and a ConfigMap is in [examples/demo-scenario/](../examples/demo-scenario/).

## Mode B — continuous in-cluster recording (Helm chart)

For longer-running deployments where you want history captured even when no laptop is attached, install the published OCI chart. It pulls the matching multi-arch image from GHCR:

```bash
CHART_VERSION=0.1.1-rc.3
helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine \
  --version "$CHART_VERSION" -n ktm-system --create-namespace
```

To run an image built from source on kind, load it into the cluster and point the chart from the repository at it:

```bash
docker build -t ktm-agent:dev .
kind load docker-image ktm-agent:dev --name <your-kind-cluster>
helm install ktm deploy/helm -n ktm-system --create-namespace \
  --set image.repository=ktm-agent --set image.tag=dev --set image.pullPolicy=Never
```

The Dockerfile uses BuildKit syntax; with a Docker that has no BuildKit, `test/e2e/e2e.sh` shows the fallback. For a remote cluster, push the image to a registry the cluster can reach and set `image.repository` and `image.tag`.

Verify:

```bash
kubectl -n ktm-system get pods,pvc,sa,networkpolicy
kubectl get clusterrole,clusterrolebinding | grep ktm
kubectl -n ktm-system logs deploy/ktm-kube-time-machine
```

The agent logs `informer caches synced`, immediately persists its first full snapshot, and then becomes Ready. From then on it keeps the latest state of every watched Deployment and ConfigMap in memory and writes it to the PVC every `snapshot.intervalSeconds` (default 300 s), and early when `snapshot.burstThreshold` changes (default 50) are pending. The history is sampled at those flushes: a change that is reverted between two flushes does not appear, and a snapshot's timestamp is the flush time, not the time of the change.

### Querying history from Mode B (CLI ↔ PVC)

The CLI reads directly from the local `--storage-dir`; it does **not** speak to the agent or to the API server for the read commands (`snapshot list/show`, `diff`, `blame`). To query a history that lives in an in-cluster PVC, you currently have to copy the contents to your machine.

The base image is `gcr.io/distroless/static-debian12:nonroot` — no shell, no `tar`, no package manager. The supported recipe attaches a named busybox ephemeral container that shares the agent's process namespace, streams the store out with `tar`, and unpacks it into a fresh private directory:

```bash
set -o pipefail
POD=$(kubectl -n ktm-system get pod -l app.kubernetes.io/name=kube-time-machine -o jsonpath='{.items[0].metadata.name}')
EXPORT="ktm-export-$(date +%s)"   # ephemeral container names cannot be reused

kubectl -n ktm-system debug "$POD" --image=busybox:1.36 --target=agent \
  --profile=general --container="$EXPORT" -- sh -c "sleep 600" >/dev/null
kubectl -n ktm-system wait "pod/$POD" --timeout=90s \
  --for=jsonpath="{.status.ephemeralContainerStatuses[?(@.name==\"$EXPORT\")].state.running}"

STORE=$(mktemp -d)                # a new, private (0700) directory per export
kubectl -n ktm-system exec "$POD" -c "$EXPORT" -- \
  tar -C /proc/1/root/var/lib/ktm -cf - . | tar -xf - -C "$STORE"

# The agent may write while tar runs. Drop the copied index so the CLI
# rebuilds it from meta.json files, which the agent writes last: the copy then
# lists only snapshots whose files were complete when they were copied.
rm -f "$STORE/index.json"
ktm --storage-dir "$STORE" snapshot list
```

Use a new directory for every export. Unpacking into a directory that already holds another store mixes the two histories. The ephemeral container stays attached to the Pod until the Pod is replaced; it exits after the `sleep`.

For routine inspection this is operationally awkward; it is good enough for v0.1.1 forensics on a real incident. A `ktm proxy` subcommand that talks to the agent over the API server is a Phase 2 candidate, gated on real-world traction.

### Customising the install

The full schema is in [deploy/helm/values.yaml](../deploy/helm/values.yaml). The most relevant knobs:

| Value | Default | Notes |
|---|---|---|
| `snapshot.intervalSeconds` | `300` | Flush cadence in seconds. Smaller keeps shorter-lived states and timestamps closer to the change, at the cost of more snapshots. |
| `snapshot.fullEvery` | `12` | Every Nth flush is a full reference snapshot. Bounds chain-reconstruction cost (see [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md)). |
| `snapshot.retainDays` | `30` | Deletes history before the safe full-snapshot anchor. Set `0` to retain everything. |
| `storage.size` | `10Gi` | PVC size. Every flush writes a snapshot, even when nothing changed, and every `fullEvery`-th is an uncompressed copy of every recorded object. With the defaults that is 24 full copies a day, so 30 days of retention hold about 720 full copies plus the deltas. Size the volume from the size of one full snapshot (`du -sh` of a `full` entry under `snapshots/`). |
| `storage.storageClassName` | `""` | Empty means cluster default. Use a storage class that encrypts at rest — see [Security](#security-the-storage-is-confidential). |
| `agent.health.*` | enabled on `:8080` | HTTP `/healthz` and `/readyz` endpoints used by Kubernetes liveness/readiness probes. |
| `agent.watchResources` | `[]` | Experimental extra resources. Requires matching read-only `rbac.extraRules`. Refused: Deployments and ConfigMaps (always recorded), Secrets, and any two entries with the same Kind. See [Changing watched resources](#changing-watched-resources). |
| `agent.resources.*` | conservative | The agent's working set is dominated by the informer cache; tune up if you watch large clusters. |
| `networkPolicy.enabled` | `true` | Turn off only on clusters whose CNI does not enforce NetworkPolicy. |

There is no `replicaCount` value. The agent writes to a `ReadWriteOnce` PVC and `replicas` is hardcoded to `1` in the template. Two writers would corrupt the storage (see [ADR-0007](adr/0007-packaging-defaults.md)).

### The 30-second capture gap during `helm upgrade`

The Deployment uses `strategy.type: Recreate`, not the default rolling update. This is deliberate and load-bearing: a rolling update would briefly run two pods sharing the same RWO PVC, both racing to write the same files. `Recreate` guarantees the old pod terminates before the new one starts, at the cost of a ~30-second gap during upgrades where no events are captured.

Changes made during that gap are not recorded as they happen. When the new Pod starts it writes a full snapshot of the state at that moment, so the net result of the gap is captured, but intermediate states are lost, and the snapshot's timestamp is the restart time, not the time of the change. A value that was set and reverted during the gap does not appear at all. Schedule upgrades for quiet windows if that matters.

### RBAC: what the agent can and cannot do

The bundled `ClusterRole` grants `list` and `watch` on `deployments.apps`
and `configmaps` cluster-wide. The default agent has no other permissions — it
cannot write or read Secrets. Experimental dynamic watches are opt-in and
require matching rules in both `agent.watchResources` and `rbac.extraRules`.

### Changing watched resources

Snapshots identify a resource by Kind, namespace and name, without its API
group. At startup the agent asks the API server for the Kind of each
`agent.watchResources` entry and refuses to start if two entries, or an entry
and a built-in watch, share a Kind. It cannot check history recorded under an
earlier list.

**If you change `agent.watchResources`, use a new PVC (a new `--storage-dir`).
Do not reuse history recorded with a different list.** Otherwise a Kind
recorded earlier from one API group, such as cert-manager's `Certificate`, and
the same Kind from another group, such as Knative's, end up in one history.

If you want to scope the agent down further (e.g. to specific namespaces), today the chart does not parameterise the rule subject lists; you would patch the `ClusterRole` after install. Scoping is a Phase 2 enhancement.

### Network exposure of the health endpoints

When `networkPolicy.enabled` is true and the CNI enforces it, ingress is denied
except to the health port — and that rule has **no source restriction**. So
`/healthz`, `/readyz` and `/metrics` are reachable from any pod in the cluster.

This is deliberate. Probes come from the kubelet on the node, and node-originated
traffic is not attributable to a pod, so adding a `podSelector` or
`namespaceSelector` can block probes entirely on CNIs that apply policy to
node→pod traffic (Cilium among them). A restriction that fails this way presents
as a crash-looping agent, which is a worse outcome than the exposure.

None of the three endpoints serve snapshot data. `/metrics` does reveal
operational shape — how many resources are buffered, how many changes are pending, and flush counts —
so treat it as you would any unauthenticated metrics endpoint. A
`networkPolicy.healthIngressFrom` value to narrow the source is a candidate for
a later release, gated on testing against the CNIs we support.

### Security: the storage is confidential

The PVC (Mode B) and the `--storage-dir` (Mode A) hold the full declarative state of every Deployment and ConfigMap in the cluster, accumulated over time. Treat that data as sensitive:

- **ConfigMaps often carry sensitive configuration** even though they are not Secrets — connection strings, internal hostnames, feature flags, tokens that should have been Secrets but weren't. All of it is recorded verbatim in the snapshots.
- **Secrets are *not* captured.** The bundled `ClusterRole` grants no access to `secrets`, so the agent cannot read them and they never reach storage. This is by design (see [ADR-0007](adr/0007-packaging-defaults.md)).
- **Use a StorageClass that encrypts at rest** for the PVC (`storage.storageClassName`). On managed clusters this usually means an encrypted EBS/PD/Disk class.
- **Restrict access to the data.** Anyone who can read the PVC — or the `kubectl debug` + `kubectl exec ... tar` extraction path documented above — can read the recorded state. Limit who can exec into the `ktm-system` namespace, and treat any copy extracted to a laptop as confidential: delete it once the incident is closed.
- **Retention defaults to 30 days.** GC preserves the latest full snapshot at
  or before the cutoff as a reconstruction anchor, so actual retained history
  can be slightly longer. Set `snapshot.retainDays=0` to keep all history.

### Uninstall

```bash
helm uninstall ktm -n ktm-system
```

`helm uninstall` also removes the cluster-scoped `ClusterRole` and `ClusterRoleBinding` that the chart created (their names are suffixed with the release namespace to avoid collisions between installs).

**The PVC survives by default.** `storage.retain` defaults to `true`, which annotates the claim with `helm.sh/resource-policy: keep`, so uninstalling to troubleshoot does not destroy the forensic record you may be about to need. Consequences:

- The PVC is left behind and must be deleted deliberately:
  ```bash
  kubectl -n ktm-system delete pvc ktm-kube-time-machine-data
  ```
- Deleting the *namespace* still deletes the PVC. `resource-policy: keep` protects against Helm, not against namespace deletion.
- To attach a kept PVC to a fresh install:
  ```bash
  helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine \
    --version "$CHART_VERSION" -n ktm-system \
    --set storage.existingClaim=ktm-kube-time-machine-data
  ```
- For ephemeral or CI installs where leftover PVCs are noise, set `--set storage.retain=false` to get the old behaviour.

Retention is not a backup: a single ReadWriteOnce volume is one storage-class failure away from gone. For anything you would miss, extract with the recipe above or use a storage class with VolumeSnapshot support.

## Troubleshooting

- **`ImagePullBackOff`.** Either the chart version has no matching image or your cluster cannot reach GHCR. For an image built from source, load it into the cluster (`kind load docker-image`) and pass `--set image.repository=ktm-agent --set image.tag=dev --set image.pullPolicy=Never`.
- **Pod `Running` but no logs / no snapshots.** Check that the agent has watch permission: `kubectl -n ktm-system describe pod ...` for events, `kubectl auth can-i list deployments --as=system:serviceaccount:ktm-system:ktm-kube-time-machine`.
- **`kubectl exec` returns "no such file or directory"** for `ls`, `cat`, etc. Expected — distroless has no shell or coreutils. Use the `kubectl debug` recipe above.
- **Two installs collide on `ClusterRole`.** Shouldn't happen — the name includes the release namespace. If it does, check that you used `helm install` with `--namespace` set, not just `kubectl apply`-d the rendered chart with a namespace overlay.
- **`ktm snapshot list` returns nothing after a Helm install.** The CLI reads from the local `--storage-dir`, not from the cluster. Either switch to Mode A or follow the PVC extraction recipe above.

## Related docs

- [docs/architecture.md](architecture.md) — the pipeline and data model.
- [docs/comparison.md](comparison.md) — how KTM relates to Velero, ArgoCD/Flux, events, observability.
- [docs/adr/](adr/) — the design decisions, especially [ADR-0005](adr/0005-declarative-state-recorder.md) (what KTM does and does not record) and [ADR-0007](adr/0007-packaging-defaults.md) (why the chart looks the way it does).
