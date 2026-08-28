# KTM Operator Runbook

Procedures for diagnosing and recovering from common failure modes of `ktm-agent` in production. Assumes the agent is deployed via the Helm chart in the `ktm-system` namespace.

---

## Quick reference

```
kubectl -n ktm-system get pod -l app.kubernetes.io/name=kube-time-machine
kubectl -n ktm-system logs -l app.kubernetes.io/name=kube-time-machine --tail=100
```

---

## Scenario 1: Agent pod is Running but readyz returns 503

**Symptoms**
- `kubectl -n ktm-system get pod` shows `Running` but `READY 0/1`
- `/readyz` returns `{"status":"not ready"}`
- `/healthz` returns 200 (liveness unaffected)

**Most likely cause — invalid `--watch-resources`**

If `agent.watchResources` in values.yaml names a GVR that doesn't exist in the cluster (CRD not installed, typo, RBAC denied), `WaitForCacheSync` blocks forever. The pod stays `Running` because liveness is independent of sync.

_Diagnosis:_
```bash
kubectl -n ktm-system logs <pod> | grep "dynamic"
# Look for: "agent: dynamic: informer cache sync timed out"
# or sustained absence of: "agent: dynamic informer caches synced"
```

_Resolution:_
```bash
# Check current watchResources
kubectl -n ktm-system get deploy -o jsonpath='{.items[0].spec.template.spec.containers[0].args}'

# Remove the offending GVR or ensure the CRD and RBAC exist:
helm upgrade ktm deploy/helm -n ktm-system --set 'agent.watchResources='

# Or add the missing RBAC rule and redeploy
```

**Alternative cause — no GVR issue but informer slow**

On very large clusters, initial list/watch can take 60-90s. Wait 2 minutes before acting. If still not ready after 5 minutes, check API server latency.

---

## Scenario 2: Agent stopped recording — snapshots freeze

**Symptoms**
- `ktm snapshot list` shows no new entries after the last known good timestamp
- `kubectl -n ktm-system logs <pod> | grep "flush"` shows no recent flush logs

**Most likely cause — PVC full**

```bash
# The agent is distroless, so run df from a temporary debug container that
# enters the agent container's process namespace. /proc/1/root exposes the
# agent's filesystem, including the PVC mounted at /var/lib/ktm.
POD=$(kubectl -n ktm-system get pod \
  -l app.kubernetes.io/name=kube-time-machine \
  -o jsonpath='{.items[0].metadata.name}')
kubectl -n ktm-system debug "$POD" \
  --image=busybox:1.36.1 \
  --target=agent \
  --profile=general \
  -- sh -c 'df -h /proc/1/root/var/lib/ktm'

# If full, check retention config
helm get values ktm -n ktm-system | grep retainDays

# GC runs before every periodic or burst flush. A restart is only needed if the
# process itself is unhealthy; it does not make GC instantaneous.
```

GC runs on the next flush tick even if the subsequent write fails. After a
restart, the first flush is a full snapshot (`flushNum==0 % fullEvery == 0`),
but it still waits for informer sync and the configured interval. If the PVC is
still full after GC, there may be no history older than the safe anchor to
delete; reduce `retainDays` or expand the PVC through a StorageClass that
supports volume expansion.

**Alternative cause — storage write error**

```bash
kubectl -n ktm-system logs <pod> | grep -E "error|ERR"
# Look for: "storage: write payload", "storage: sync snapshots dir", I/O errors
```

If the node's underlying disk is failing, the only safe option is to move the PVC to a healthy node (or provision a new PVC).

---

## Scenario 3: `ktm snapshot list` shows no history (index corrupt or missing)

**Symptoms**
- The PVC has files under `snapshots/` but `ktm snapshot list` returns empty
- `index.json` is missing or contains invalid JSON

**Self-healing behavior**

The store auto-rebuilds `index.json` from `snapshots/*/meta.json` on every `storage.NewLocal()` call (including agent restart and every CLI invocation). This is the designed recovery path (ADR-0004).

_Manual trigger:_
```bash
# Mount the PVC locally (Mode A) or use a debug pod, then:
rm /var/lib/ktm/index.json
ktm --storage-dir=/var/lib/ktm snapshot list
# The CLI calls storage.NewLocal which triggers rebuildIndex automatically.
```

If `rebuildIndex` produces an empty index but `snapshots/` has directories, check whether `meta.json` is present in each snapshot directory — directories without `meta.json` are skipped.

---

## Scenario 4: `ktm rollback` returns 409 Conflict

**Symptoms**
```
Error: rollback failed: 409 Conflict — the resource was modified between preview and apply.
Re-run `ktm rollback` to preview the current live state.
```

**Cause and resolution**

This is expected and safe: the resource changed between when you saw the preview and when you confirmed. The optimistic locking (ADR-0006) caught it. Re-run immediately:

```bash
ktm rollback Deployment/default/api --to <snapshot-id>
```

If you're in an automated rollback scenario with frequent conflicts, the resource is under active reconciliation (ArgoCD, Flux, HPA). Coordinate with the controller or set `--force` (not implemented in v0.1.x — manual re-run is the designed flow).

---

## Scenario 5: `ktm rollback` returns 404 — resource no longer exists

```
Deployment/default/api no longer exists in the cluster. Rollback will not
recreate it, because a deliberate deletion must not be silently undone.
Re-run with --allow-create to recreate it from snapshot.
```

Rollback stops rather than recreating. Deleting something is usually deliberate,
and undoing a deletion is a different operation from rolling a resource back —
so recreation is opt-in:

```bash
ktm rollback Deployment/default/api --to <snapshot-id> --allow-create
```

Review the preview carefully: the resource is created without `resourceVersion`,
`uid` or `creationTimestamp` (stripped by `stripServerOwned`), so it is a new
object that merely looks like the old one — anything referencing the previous
UID, such as an ownerReference, will not reattach. If the namespace no longer
exists, create it first.

---

## Scenario 6: Agent crash loop (CrashLoopBackOff)

```bash
kubectl -n ktm-system describe pod <pod>
kubectl -n ktm-system logs <pod> --previous
```

Common causes:

| Log message | Cause | Fix |
|---|---|---|
| `build kube client: ...` | Invalid kubeconfig or in-cluster credentials | Check ServiceAccount binding |
| `open storage: ...` | PVC not mounted / permissions | Check PVC status, fsGroup |
| `parse --watch-resources: malformed GVR` | Syntax error in watchResources | Fix values.yaml GVR format |
| `register handler for <resource>: ...` | Programming error (should not happen in released builds) | Check image tag / file a bug |

---

## Scenario 7: `ktm` CLI hangs on `blame` or `diff`

**Most likely cause** — corrupt delta chain (cycle in PrevID). Run with a timeout:

```bash
timeout 30s ktm --storage-dir=/var/lib/ktm blame Deployment/default/api
```

If it hangs, the chain is corrupt. Identify the problematic snapshot:

```bash
ls /var/lib/ktm/snapshots/
# Find the timestamp range where history stops being readable
ktm --storage-dir=/var/lib/ktm snapshot list | tail -20
```

Deleting an *interior* snapshot directory does not heal the chain — it strands
every delta that follows it, because each delta reconstructs from its `PrevID`
and that link is now dangling. The next full snapshot starts a new chain rather
than bridging the gap, so history before the corruption stays readable and the
deltas between the deleted snapshot and the next full one do not.

Prefer, in order:

1. Reconstruct from the newest full snapshot *after* the corruption — use
   `ktm snapshot list` to find one with `KIND=full`.
2. If you must remove the corrupt directory, expect to lose the deltas between
   it and the next full snapshot, and copy the store first.

Note that `ktm` opens storage read-only and will not rewrite `index.json`; the
agent repairs the index on its next start.

---

## Scenario 8: ConfigMap contains sensitive data — restrict namespace

The agent records all ConfigMaps in watched namespaces by design (Secrets are never captured). If a namespace contains ConfigMaps with sensitive values (database passwords, API keys stored in plaintext configs), exclude it:

```bash
helm upgrade ktm deploy/helm -n ktm-system \
  --set 'agent.excludeNamespaces={kube-system,kube-public,kube-node-lease,sensitive-ns}'
```

The agent must restart for this to take effect. Existing snapshots containing the sensitive data remain on the PVC — delete them manually or wait for GC to expire them past `retainDays`.

---

## Recovery checklist after any incident

1. `kubectl -n ktm-system get pod` — pod should be `READY 1/1`
2. Run the `kubectl debug ... df -h /proc/1/root/var/lib/ktm` command from Scenario 2 — PVC should have headroom
3. `ktm snapshot list | tail -5` — verify recent entries exist and timestamps are current
4. `ktm blame Deployment/<ns>/<name>` on a known active resource — verify output renders
5. Check `/metrics`: `ktm_flushes_total{kind="full"}` and `ktm_flushes_total{kind="delta"}` should be incrementing

---

## Emergency: delete and rebuild history

If the PVC is unrecoverable and you need a clean start:

```bash
# Scale down first to avoid concurrent writes
kubectl -n ktm-system scale deploy/ktm-kube-time-machine --replicas=0

# Delete the PVC (DESTRUCTIVE — all history lost)
kubectl -n ktm-system delete pvc/ktm-kube-time-machine-data

# Re-deploy (chart will create a new PVC)
helm upgrade ktm deploy/helm -n ktm-system

# The agent will capture a fresh full snapshot on first startup
```
