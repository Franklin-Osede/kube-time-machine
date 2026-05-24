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

Assumes KTM is already installed in the cluster (see [docs/install.md](../../docs/install.md)) and `ktm` is on your PATH.

```bash
# 1. Set up a clean demo cluster state.
kubectl apply -f examples/demo-scenario/00-namespace.yaml
kubectl apply -f examples/demo-scenario/01-config.yaml
kubectl apply -f examples/demo-scenario/02-deployment.yaml

# Wait long enough for at least one KTM snapshot to capture the healthy state
# (the default cadence is 5 min; for the demo you'll want to install with
# --set snapshot.intervalSeconds=10 so the wait is short).
sleep 15

# 2. Break it. The deployment will fail to roll out — pods stuck in
#    ImagePullBackOff while the ReplicaSet from step 2 keeps serving.
kubectl apply -f examples/demo-scenario/03-break.yaml

# 3. Investigate. Find when it last looked sane.
ktm snapshot list | tail
ktm blame deployment/ktm-demo/api

# 4. Diff the broken state against the last good one.
ktm diff --from <last-good-id> --to <latest-id>

# 5. Roll it back.
ktm rollback deployment/ktm-demo/api --to <last-good-id>

# 6. Pods recover.
kubectl -n ktm-demo rollout status deployment/api
```

## Cleanup

```bash
kubectl delete namespace ktm-demo
```
