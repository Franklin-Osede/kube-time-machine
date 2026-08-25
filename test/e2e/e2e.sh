#!/usr/bin/env bash
# End-to-end test against a real Kubernetes cluster (kind).
#
# Covers the full product loop, which no other test in this repo touches:
#
#   install -> record -> mutate -> reconstruct -> blame -> rollback
#
# Everything else is unit-level or chart rendering; this is the only check
# that the agent, the storage format, the chart, and the CLI actually agree
# with each other at runtime.
#
# Usage:  test/e2e/e2e.sh [--keep]
#   --keep   leave the kind cluster running for inspection afterwards
set -euo pipefail

CLUSTER=ktm-e2e
NS=ktm-system
RELEASE=ktm
IMAGE=ktm-agent:e2e
INTERVAL=5           # seconds between flushes; small so the test is quick
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
KEEP=false
[[ "${1:-}" == "--keep" ]] && KEEP=true

log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
ok()   { printf '\033[0;32m    ok: %s\033[0m\n' "$*"; }
fail() { printf '\033[0;31m    FAIL: %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  local code=$?
  if [[ $code -ne 0 ]]; then
    printf '\n\033[0;31m--- agent logs ---\033[0m\n' >&2
    kubectl -n "$NS" logs "deploy/$RELEASE-kube-time-machine" --tail=60 2>&1 >&2 || true
    kubectl -n "$NS" get pods,pvc 2>&1 >&2 || true
  fi
  rm -rf "$WORK"
  if [[ "$KEEP" == "true" ]]; then
    echo "kept cluster '$CLUSTER' (delete with: kind delete cluster --name $CLUSTER)"
  else
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  fi
  exit $code
}
trap cleanup EXIT

# ---------------------------------------------------------------- setup ---
log "Creating kind cluster '$CLUSTER'"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --wait 120s
KUBECONFIG_FILE="$WORK/kubeconfig"
kind get kubeconfig --name "$CLUSTER" > "$KUBECONFIG_FILE"
export KUBECONFIG="$KUBECONFIG_FILE"

log "Building the agent image and loading it into the cluster"
# The release Dockerfile declares a syntax directive and uses
# --mount=type=cache, both of which require BuildKit. release.yml always has
# it (docker/build-push-action brings buildx), but a developer machine may
# not, so fall back to a BuildKit-free variant rather than skipping the whole
# E2E. The fallback changes only the build cache mount — same base images,
# same nonroot user, same binary — so what gets installed is still
# representative. The authoritative Dockerfile check is the release build.
if docker buildx version >/dev/null 2>&1; then
  DOCKER_BUILDKIT=1 docker build -t "$IMAGE" "$ROOT"
else
  echo "    buildx unavailable — building without BuildKit (cache mount dropped)"
  sed -e '/^# syntax=/d' \
      -e 's|^RUN --mount=type=cache,target=/root/.cache/go-build .*|RUN \\|' \
      "$ROOT/Dockerfile" > "$WORK/Dockerfile.nobuildkit"
  DOCKER_BUILDKIT=0 docker build -f "$WORK/Dockerfile.nobuildkit" -t "$IMAGE" "$ROOT"
fi
kind load docker-image "$IMAGE" --name "$CLUSTER"

log "Building the ktm CLI"
( cd "$ROOT" && go build -o "$WORK/ktm" ./cmd/ktm )
KTM="$WORK/ktm"

# ------------------------------------------------------------- install ---
log "Installing the chart"
helm install "$RELEASE" "$ROOT/deploy/helm" \
  --namespace "$NS" --create-namespace \
  --set image.repository=ktm-agent \
  --set image.tag=e2e \
  --set image.pullPolicy=Never \
  --set snapshot.intervalSeconds=$INTERVAL \
  --set snapshot.fullEvery=3 \
  --wait --timeout 180s

# Readiness now means "informers synced AND at least one snapshot persisted",
# so --wait returning is itself the first assertion.
ok "pod reached Ready, which requires a durable first snapshot"

# -------------------------------------------------------------- record ---
log "Creating a Deployment for the agent to record"
kubectl create namespace demo
kubectl -n demo create deployment api --image=nginx:1.27 --replicas=1
kubectl -n demo rollout status deployment/api --timeout=120s

sleep $((INTERVAL * 3))

log "Mutating the Deployment"
kubectl -n demo set image deployment/api nginx=nginx:1.29
kubectl -n demo rollout status deployment/api --timeout=120s

sleep $((INTERVAL * 3))

# ------------------------------------------------------------- extract ---
# Uses the documented recipe from docs/install.md, so the docs are tested too.
log "Extracting the snapshot store from the PVC"
POD=$(kubectl -n "$NS" get pod -l app.kubernetes.io/name=kube-time-machine -o jsonpath='{.items[0].metadata.name}')
kubectl -n "$NS" debug "$POD" --image=busybox:1.36 --target=agent --profile=general \
  --container=extract -- sh -c "sleep 300" >/dev/null
kubectl -n "$NS" wait --for=jsonpath='{.status.ephemeralContainerStatuses[0].state.running}' \
  --timeout=90s "pod/$POD" >/dev/null 2>&1 || sleep 10

STORE="$WORK/store"
mkdir -p "$STORE"
kubectl -n "$NS" exec "$POD" -c extract -- \
  tar -C /proc/1/root/var/lib/ktm -cf - . | tar -xf - -C "$STORE"
ok "extracted $(find "$STORE/snapshots" -maxdepth 1 -mindepth 1 -type d | wc -l | tr -d ' ') snapshot dirs"

# ----------------------------------------------------------- inspect ---
log "snapshot list"
"$KTM" --storage-dir "$STORE" snapshot list | tee "$WORK/list.txt"
COUNT=$(grep -cE '^[0-9]{8}T[0-9]{6}' "$WORK/list.txt" || true)
[[ "$COUNT" -ge 2 ]] || fail "expected >= 2 snapshots, got $COUNT"
ok "$COUNT snapshots recorded"

FIRST=$(grep -oE '^[0-9]{8}T[0-9]{6}[0-9]{3}Z' "$WORK/list.txt" | head -1)
LAST=$(grep -oE '^[0-9]{8}T[0-9]{6}[0-9]{3}Z' "$WORK/list.txt" | tail -1)
[[ -n "$FIRST" && -n "$LAST" ]] || fail "could not parse snapshot IDs from list output"

log "reconstruct (snapshot show) at the newest snapshot"
# Without --key, show prints a KIND/NAMESPACE/NAME/SIZE summary table.
"$KTM" --storage-dir "$STORE" snapshot show "$LAST" > "$WORK/show.txt"
cat "$WORK/show.txt"
awk '$1=="Deployment" && $2=="demo" && $3=="api" {found=1} END{exit !found}' "$WORK/show.txt" \
  || fail "reconstructed state is missing Deployment demo/api"

# --key prints the full reconstructed payload for one resource, which is where
# the container image actually lives.
"$KTM" --storage-dir "$STORE" snapshot show "$LAST" --key Deployment/demo/api > "$WORK/payload.json"
grep -q 'nginx:1.29' "$WORK/payload.json" \
  || fail "reconstructed payload does not show the mutated image (nginx:1.29)"
ok "reconstructed payload carries the mutated image"

log "diff between first and last snapshot"
"$KTM" --storage-dir "$STORE" diff --from "$FIRST" --to "$LAST" > "$WORK/diff.txt"
cat "$WORK/diff.txt"
grep -q 'demo' "$WORK/diff.txt" || fail "diff does not mention the demo namespace"
ok "diff reports changes in the demo namespace"

log "blame Deployment/demo/api"
"$KTM" --storage-dir "$STORE" blame Deployment/demo/api > "$WORK/blame.txt"
cat "$WORK/blame.txt"
# blame renders an attribution table: TIME / OP / ACTORS / SNAPSHOT. The
# interesting assertion is not that the image string appears, but that the
# mutation was attributed to the actor that actually performed it —
# `kubectl set image` shows up as the kubectl-set field manager.
grep -q 'CREATED' "$WORK/blame.txt"  || fail "blame is missing the CREATED event"
grep -q 'MODIFIED' "$WORK/blame.txt" || fail "blame is missing the MODIFIED event"
awk '/MODIFIED/ && /kubectl-set/ {found=1} END{exit !found}' "$WORK/blame.txt" \
  || fail "blame did not attribute the modification to the kubectl-set field manager"
ok "blame attributes the change to kubectl-set"

# ------------------------------------------------------------ rollback ---
# Roll back to a snapshot that still has nginx:1.27, then verify the live
# cluster actually changed.
log "Finding a snapshot that predates the mutation"
TARGET=""
for id in $(grep -oE '^[0-9]{8}T[0-9]{6}[0-9]{3}Z' "$WORK/list.txt"); do
  if "$KTM" --storage-dir "$STORE" snapshot show "$id" --key Deployment/demo/api 2>/dev/null | grep -q 'nginx:1.27'; then
    TARGET="$id"; break
  fi
done
[[ -n "$TARGET" ]] || fail "no snapshot contains the pre-mutation image"
ok "rolling back to $TARGET"

log "rollback (dry check: refuses without confirmation)"
echo "n" | "$KTM" --storage-dir "$STORE" rollback Deployment/demo/api \
  --to "$TARGET" --kubeconfig "$KUBECONFIG_FILE" > "$WORK/abort.txt" 2>&1 || true
grep -q 'cluster:' "$WORK/abort.txt" || fail "rollback did not print the target cluster"
grep -q 'rollback aborted' "$WORK/abort.txt" || fail "answering 'n' did not abort"
ok "target cluster displayed, and 'n' aborts"

log "rollback --yes"
"$KTM" --storage-dir "$STORE" rollback Deployment/demo/api \
  --to "$TARGET" --kubeconfig "$KUBECONFIG_FILE" --yes | tee "$WORK/rollback.txt"
grep -q 'cluster:' "$WORK/rollback.txt" || fail "--yes suppressed the cluster disclosure"

kubectl -n demo rollout status deployment/api --timeout=120s
LIVE=$(kubectl -n demo get deployment api -o jsonpath='{.spec.template.spec.containers[0].image}')
[[ "$LIVE" == "nginx:1.27" ]] || fail "rollback did not take effect: live image is $LIVE"
ok "live Deployment rolled back to $LIVE"

# --------------------------------------------------- create-on-404 gate ---
log "rollback refuses to recreate a deleted resource without --allow-create"
kubectl -n demo delete deployment api --wait=true
set +e
OUT=$("$KTM" --storage-dir "$STORE" rollback Deployment/demo/api \
  --to "$TARGET" --kubeconfig "$KUBECONFIG_FILE" --yes 2>&1)
RC=$?
set -e
[[ $RC -ne 0 ]] || fail "rollback recreated a deleted resource without --allow-create"
grep -q -- '--allow-create' <<<"$OUT" || fail "refusal did not mention --allow-create: $OUT"
ok "refused, and named the opt-in flag"

log "rollback --allow-create recreates it"
"$KTM" --storage-dir "$STORE" rollback Deployment/demo/api \
  --to "$TARGET" --kubeconfig "$KUBECONFIG_FILE" --yes --allow-create >/dev/null
kubectl -n demo get deployment api >/dev/null || fail "--allow-create did not recreate the Deployment"
ok "recreated"

# --------------------------------------------------- PVC retention gate ---
log "helm uninstall preserves the PVC"
PVC=$(kubectl -n "$NS" get pvc -o jsonpath='{.items[0].metadata.name}')
helm uninstall "$RELEASE" -n "$NS" >/dev/null
sleep 5
kubectl -n "$NS" get pvc "$PVC" >/dev/null 2>&1 || fail "PVC $PVC was destroyed by helm uninstall"
ok "PVC $PVC survived uninstall"

printf '\n\033[1;32m==> E2E PASSED\033[0m\n'
