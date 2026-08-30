# Demo scenario

A reproducible breakage-and-rollback walkthrough used in the launch demo.
Five files, ordered. Each step is one `kubectl apply` away from the next.

| File | What it does |
|---|---|
| [00-namespace.yaml](00-namespace.yaml) | Creates the `ktm-demo` namespace. |
| [01-config.yaml](01-config.yaml) | ConfigMap `app-config` with sane defaults (`env=prod`, `log_level=info`). |
| [02-deployment.yaml](02-deployment.yaml) | Deployment `api` pinned to `nginx:1.27-alpine`. Mounts the ConfigMap as env vars. The app is healthy. |
| [03-break.yaml](03-break.yaml) | Updates the Deployment to a tag that does not exist (`nginx:99.99`). The rollout fails; the app goes down. The kind of change someone might `kubectl apply` on a Friday afternoon. |
| [04-also-break-config.yaml](04-also-break-config.yaml) | Optional secondary breakage: edits the ConfigMap to `env=oops`. Useful to demo blame + rollback on a non-Deployment resource. |

## Recommended demo sequence

The demo uses **Mode A** (local-first): the agent runs on your laptop against your kubeconfig, and the CLI queries the same `--storage-dir` from a second terminal. This is the path that works without extracting PVC contents — see [docs/install.md](../../docs/install.md) for why.

Two terminals open against the same cluster. Build the binaries once with `make build` from the repo root.

### Terminal 1 — start the recorder

```bash
./bin/ktm-agent \
  --kubeconfig ~/.kube/config \
  --storage-dir /tmp/ktm \
  --interval 10s \
  --full-every 3
```

Wait for the line `agent: informer caches synced` before continuing.

### Terminal 2 — drive the demo

```bash
# 1. Apply the healthy baseline.
kubectl apply -f examples/demo-scenario/00-namespace.yaml
kubectl apply -f examples/demo-scenario/01-config.yaml
kubectl apply -f examples/demo-scenario/02-deployment.yaml

# Wait long enough for at least one KTM snapshot to capture the healthy state.
# With --interval 10s --full-every 3, fifteen seconds covers it.
sleep 15
GOOD_ID="$(./bin/ktm --storage-dir /tmp/ktm snapshot list | awk 'NR > 1 { id=$1 } END { print id }')"

# 2. Break it. Pods stick in ImagePullBackOff; the previous ReplicaSet
#    keeps serving until its retention kicks in.
kubectl apply -f examples/demo-scenario/03-break.yaml

# Give the agent another tick to record the broken state.
sleep 12
BROKEN_ID="$(./bin/ktm --storage-dir /tmp/ktm snapshot list | awk 'NR > 1 { id=$1 } END { print id }')"

# 3. Investigate. Find when the deployment last looked sane.
./bin/ktm --storage-dir /tmp/ktm snapshot list | tail
./bin/ktm --storage-dir /tmp/ktm blame deployment/ktm-demo/api

# 4. Diff the broken state against the last good one.
./bin/ktm --storage-dir /tmp/ktm diff \
  --from "$GOOD_ID" --to "$BROKEN_ID"

# 5. Roll the Deployment back. ktm fetches the live object, shows a
#    preview, prompts for [y/N], then Updates with the captured RV.
./bin/ktm --storage-dir /tmp/ktm rollback \
  deployment/ktm-demo/api --to "$GOOD_ID"

# 6. Pods recover.
kubectl -n ktm-demo rollout status deployment/api
```

The `--storage-dir /tmp/ktm` flag MUST match the one passed to `ktm-agent` in Terminal 1. The agent writes to that directory; the CLI reads from it. If you forget it on the CLI, you'll be querying `~/.ktm/data` (the per-user default) and see an empty history.

## Cleanup

```bash
kubectl delete namespace ktm-demo
# Stop the agent in Terminal 1 with Ctrl-C; it performs a final flush.
rm -rf /tmp/ktm
```
