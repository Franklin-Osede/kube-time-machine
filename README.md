# kube-time-machine

kube-time-machine (KTM) records the declarative state of Kubernetes Deployments
and ConfigMaps over time, so that during an incident you can see what changed,
when, and roll one resource back to an earlier state. I built it because "what
changed?" is the first question in most incidents, and a change made with
`kubectl edit` or by an operator leaves no trail in Git or CI history.

It has two binaries that share a storage directory: `ktm-agent` records, and
`ktm` lists, diffs, blames and rolls back.

## Status

| | |
|---|---|
| Latest published build | `v0.1.1-rc.3` prerelease: binaries, multi-arch agent image and OCI chart, verified |
| Stable `v0.1.1` | Pending |
| Supported kinds | Deployments and ConfigMaps (record, diff, blame, rollback) |
| Storage | Local directory or a PersistentVolume. No cloud storage |
| Tested on | kind with its default node image, Kubernetes v1.36.1 on the test run. Not pinned; other versions are untested |

What works today: recording both kinds, listing snapshots, reconstructing the
state at any snapshot, diffing two snapshots, a per-resource timeline (`blame`),
and rolling one resource back with a preview and an optimistic-concurrency
check. An experimental `--watch-resources` flag records other kinds for diff
and blame only.

Planned, not built: querying the in-cluster agent without copying its volume,
cloud storage, a web UI, multi-cluster. See [docs/roadmap.md](docs/roadmap.md).

## What it records, and what it does not

The agent watches the API server and keeps the latest declarative state of each
resource in memory: `spec`, labels, user annotations and ConfigMap `data`. It
writes that state as a full snapshot or a delta against the previous one: first
as soon as its caches sync, then every flush interval (default 5 minutes), and
early when 50 or more changes are pending (`--burst-threshold`, checked every
10 seconds).

That makes the history sampled, not a log of every write:

- A snapshot's timestamp is the flush time, not the time of the change.
- If a resource changes and changes back between two flushes, the history
  shows nothing. A bad value that lived for less than an interval can be
  missing.
- While the agent is not running, nothing is recorded. The first snapshot after
  it starts shows the net state, not the steps in between.
- Controller-owned fields are not recorded: `.status`, `resourceVersion`,
  `managedFields` and annotations that tools rewrite on every operation (Helm,
  Argo CD, Flux, `last-applied-configuration`). See
  [ADR-0005](docs/adr/0005-declarative-state-recorder.md).
- `blame` shows which field managers had touched the resource at each
  snapshot. That is evidence of who was involved, not proof of who made a
  given change.

## Prerequisites

- macOS or Linux, amd64 or arm64. A Windows amd64 binary is published, but
  the commands below are POSIX shell.
- `curl` and `shasum`.
- `kubectl`, and a cluster you can change. The quick start creates one with
  [kind](https://kind.sigs.k8s.io/), which needs Docker. `kind create cluster`
  switches your current kubectl context to the new cluster.
- For the in-cluster mode: Helm 3.

## Quick start

These commands were run as written from a clean zsh against the published
rc.3 build. Set `VERSION` to the release you want; until the stable release,
the newest verified build is `v0.1.1-rc.3`.

Install the two binaries and check them against the published checksums:

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
ktm --version
```

Create a disposable cluster with one Deployment:

```bash
kind create cluster --name ktm-demo
kubectl create namespace ktm-demo
kubectl -n ktm-demo create deployment api --image=nginx:1.27
kubectl -n ktm-demo rollout status deployment/api --timeout=120s
```

Start recording, with a short interval so the demo is quick, and wait for the
first full snapshot:

```bash
STORE=$(mktemp -d)
ktm-agent --storage-dir "$STORE" --interval 5s --full-every 3 --health-addr= \
  > "$STORE.log" 2>&1 &
AGENT_PID=$!
until ktm --storage-dir "$STORE" snapshot list 2>/dev/null | grep -q full; do sleep 1; done
GOOD=$(ktm --storage-dir "$STORE" snapshot list | awk 'NR > 1 { id = $1 } END { print id }')
```

Break the Deployment with an image tag that does not exist, and wait for the
next flush. The rollout stalls: the new Pod cannot pull the image, and the old
Pod keeps running.

```bash
kubectl -n ktm-demo set image deployment/api nginx=nginx:99.99
sleep 7
BAD=$(ktm --storage-dir "$STORE" snapshot list | awk 'NR > 1 { id = $1 } END { print id }')
kubectl -n ktm-demo get pods
```

Find what changed:

```bash
ktm --storage-dir "$STORE" snapshot list
ktm --storage-dir "$STORE" blame deployment/ktm-demo/api
ktm --storage-dir "$STORE" diff --from "$GOOD" --to "$BAD" --namespace ktm-demo
```

Roll back to the last known-good snapshot. The command prints the target
cluster and a preview of the change, then asks for confirmation; answer `y`.

```bash
ktm --storage-dir "$STORE" rollback deployment/ktm-demo/api --to "$GOOD"
kubectl -n ktm-demo rollout status deployment/api --timeout=120s
kubectl -n ktm-demo get deployment api -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

Clean up:

```bash
kill "$AGENT_PID"
kind delete cluster --name ktm-demo
```

## Running the agent in the cluster

The Helm chart runs the agent as a single Pod that writes to a PersistentVolume:

```bash
helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine \
  --version 0.1.1-rc.3 -n ktm-system --create-namespace
```

`ktm` reads a local directory and does not talk to the agent, so to query this
history you copy the volume's contents to your machine first.
[docs/install.md](docs/install.md) has the copy procedure, the chart values,
building from source, and what the recorded data exposes.

## Commands

| Command | Reads | Touches the cluster |
|---|---|---|
| `ktm snapshot list` | storage directory | no |
| `ktm snapshot show <id> [--key Kind/ns/name]` | storage directory | no |
| `ktm diff --from <id> --to <id> [--namespace ns]` | storage directory | no |
| `ktm blame <kind>/<ns>/<name>` | storage directory | no |
| `ktm rollback <kind>/<ns>/<name> --to <id>` | storage directory | yes: one Get, then Update or, with `--allow-create`, Create |

Rollback safety checks:

| Check | Behaviour |
|---|---|
| Target disclosure | Prints the cluster, user, resource and snapshot before anything else, also with `--yes` |
| Preview | Shows the diff between the live object and the exact object to be written |
| Concurrent change | Updates with the `resourceVersion` read for the preview; if the resource changed since, the API server rejects it and nothing is written |
| Deleted resource | Refuses unless `--allow-create` is passed |
| Declined or unanswered prompt | Writes nothing and exits non-zero; `--yes` is required without a terminal |
| Unrecorded annotations | Keeps their live values instead of deleting them; never writes KTM's own `ktm.io/` annotations |

## How it compares

| Question | Tool that answers it best |
|---|---|
| Restore a cluster, namespace or set of resources after a failure | Velero, which also restores single resources by filter |
| Which Git revision is deployed, and has the cluster drifted from it | Argo CD or Flux |
| Who called the API, including rejected requests | Kubernetes audit logs |
| Did the rollout finish, and what was its status over time | Prometheus with kube-state-metrics |
| What declarative state did this resource have at each sampled point, regardless of who changed it, and how do I put one resource back | KTM |

[docs/comparison.md](docs/comparison.md) has the longer discussion, including
where these tools overlap.

## Architecture

The agent uses client-go informers, keeps the latest sanitised state per
resource in memory, and writes snapshots with atomic file writes; `meta.json` is
written last and is the commit point. Every Nth snapshot is a full one, so
reconstructing any point replays at most N-1 deltas. A single writer lock
prevents two agents from writing the same directory.
[docs/architecture.md](docs/architecture.md) describes the pipeline and on-disk
layout, and [docs/adr/](docs/adr/) records the decisions and their trade-offs.

## Contributing

Issues and focused pull requests are welcome. Read
[CONTRIBUTING.md](CONTRIBUTING.md), the [security policy](SECURITY.md) and the
[code of conduct](CODE_OF_CONDUCT.md) first.

## Author

Built by Franklin Osede, backend and platform engineer.
[LinkedIn](https://www.linkedin.com/in/franklinosede/) ·
[GitHub](https://github.com/Franklin-Osede)

## License

MIT. See [LICENSE](LICENSE).
