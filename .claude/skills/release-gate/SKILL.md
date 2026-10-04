---
name: release-gate
description: Run kube-time-machine's full release gate — the CI checks locally, the e2e loop on kind, and after a tag, verify the published image, chart, binaries and checksums. Use when asked to "run the gate", before tagging, or after a release workflow finishes.
---

# Release gate

Run every step; report a table of step → pass/fail with the failing output.
Do not stop at the first failure unless a later step depends on it.

## 1. Pre-tag (local)

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -count=1 -timeout 5m ./...
make build
go mod tidy && git diff --exit-code -- go.mod go.sum
golangci-lint run --timeout=5m
govulncheck ./...
helm lint deploy/helm
helm template ktm deploy/helm --namespace ktm-system >/dev/null
```

`govulncheck` stdlib findings are fixed by a toolchain patch, not code; report
them separately from module findings. Module findings fail the gate.

## 2. End-to-end

```bash
make e2e
```

Two known local hazards — check before debugging the product:

- **PVCs stuck Pending / CoreDNS 0/1:** `kubectl -n kube-system get pods`; if
  `kube-proxy` is crash-looping on "too many open files", raise the limit:
  `docker run --rm --privileged alpine sh -c 'sysctl -w fs.inotify.max_user_instances=1024'`.
  Never delete the user's other kind clusters to free instances.
- **buildx missing:** `e2e.sh` already falls back to a non-BuildKit build.
  CI uses its own buildx, so the real Dockerfile is still exercised there.

## 3. Post-tag (after `release.yml` finishes for `vX.Y.Z`)

```bash
V=X.Y.Z
gh run list --workflow=release.yml --limit 3
docker manifest inspect ghcr.io/franklin-osede/ktm-agent:$V   # multi-arch
helm show chart oci://ghcr.io/franklin-osede/charts/kube-time-machine --version $V
gh release view v$V --json assets --jq '.assets[].name'       # binaries + checksums.txt
```

Then confirm:

- `:latest` moved only if the tag has no hyphen.
- `checksums.txt` verifies against at least one downloaded binary
  (`shasum -a 256 -c --ignore-missing`).
- A clean install works:
  `helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine --version $V -n ktm-system --create-namespace`
  on a fresh kind cluster, agent reaches Ready.
- `0.1.0` is no longer pullable (the withdrawn release must stay gone).
