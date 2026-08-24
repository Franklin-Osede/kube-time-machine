# Pre-deployment audit — kube-time-machine

**Scope:** release readiness for the first supported tag (v0.1.1).
**State:** `fix/prelaunch-correctness` merged into `main` (FF to `6bf3cb9`), plus the
follow-up work described below. **Date:** 2026-08-24.
**Supersedes** `docs/audit-preproduction-v0.1.1.md` (2026-06-25), whose NO-GO verdict was
based on the pre-merge tree.

---

## Verdict

**One blocker remains, and it is not in the code.** `v0.1.0` artefacts are live and
publicly installable, they predate every correctness fix in 0.1.1, and `:latest` points
at them.

Everything else is green. Full gate, run locally on Go 1.26.5 / Helm v4.2.3:

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go vet ./...` | clean |
| `go test -race -count=1 ./...` | all 6 packages pass |
| `golangci-lint run` (v2.12.2) | **0 issues** |
| `govulncheck ./...` | 0 module vulns (5 stdlib, toolchain-fixed — see below) |
| `go mod tidy` | clean, no diff |
| `helm lint deploy/helm` | 0 failed (1 INFO: icon recommended) |
| `helm template`, and with `agent.health.enabled=false` | both render |

---

## The blocker: live v0.1.0 artefacts serve pre-fix code

Querying GHCR anonymously:

```
ghcr.io/franklin-osede/ktm-agent                  → 0.1.0, latest
ghcr.io/franklin-osede/charts/kube-time-machine   → 0.1.0
```

Both are public and pullable **right now**, while `git ls-remote --tags` and the releases
API return empty. The tag and GitHub Release were withdrawn; the packages were not. So:

- `helm install ktm oci://ghcr.io/franklin-osede/charts/kube-time-machine --version 0.1.0`
  — the exact line the old README published — **succeeds today**, and installs an agent
  built before the empty-snapshot fix, before durable writes, before retention, and
  before health probes.
- `ktm-agent:latest` resolves to that same build.
- Neither package corresponds to any commit in this repository, so what they contain is
  not reproducible from source.

This is worse than having no artefacts. A missing package produces an error the user
understands; a stale one produces a working install of the version that can write a full
snapshot claiming the cluster is empty.

**Fix, in order:**
1. Delete the orphaned `0.1.0` packages (image + chart) from GHCR, or mark them
   deprecated. They are unreproducible and actively misleading.
2. Tag `v0.1.1-rc.1` to rehearse. `:latest` is safe — `release.yml:71` gates it on the
   absence of a hyphen, so an RC cannot repoint it.
3. Tag `v0.1.1`. That publishes the image, the OCI chart, binaries for both `ktm` and
   `ktm-agent` across 5 platforms, and `checksums.txt`, and moves `:latest` onto a build
   that corresponds to a real commit.

Good news buried in this: **the release pipeline demonstrably works**. It has already
completed a multi-arch image push *and* an OCI chart push with only `packages: write`.
`docs/PROGRESS.md:200` lists that as an open unknown; it is answered. That removes the
main reason to fear the first tag.

---

## What the merge fixed

All five original blockers and eight of ten should-fixes, each verified in the merged
tree rather than inferred from commit messages:

- **Empty-snapshot corruption** — `cmd/agent/main.go:148-159` now gates the final flush on
  a non-blocking read of `allReady`, logging `skipping final flush; informer caches never
  synced` instead of persisting a bogus full snapshot. This was the most damaging defect:
  under CrashLoopBackOff from bad RBAC, every restart appended an empty reference
  snapshot.
- **Durability** — payload written before metadata (metadata is now the commit point),
  `fsync` on both file and parent directory, and `index.json` rebuilt from `snapshots/`
  when missing or corrupt instead of blocking startup.
- **Retention** — `--retain-days` with GC that preserves the newest full snapshot before
  the cutoff as an anchor, so deltas in the window stay reconstructable.
- **Operability** — `/healthz`, `/readyz`, `/metrics`, liveness/readiness probes, kubelet
  admitted through the NetworkPolicy, egress narrowed from `0.0.0.0/0` to DNS + API
  server.
- **Release correctness** — `ktm-agent` now published (Mode A was unreachable from release
  artefacts before), `:latest` gated against prereleases, SHA-256 checksums attached.
- **Honesty** — README no longer claims a shipped v0.1.0; the `install.md` Security
  section the docs linked to now exists and states that the agent persists full ConfigMap
  `data` cluster-wide as plaintext on the PVC.

---

## Follow-up work applied after the merge

- **Dependency vulnerabilities fixed.** `govulncheck` found 7 reachable advisories.
  Bumped `golang.org/x/net` v0.49.0 → v0.55.0 (GO-2026-5026, GO-2026-4918: HTTP/2
  infinite loop) and `golang.org/x/text` v0.33.0 → v0.39.0 (GO-2026-5970). All reachable
  from `internal/cli/rollback.go:116`. Module vulns now zero; tests pass.
- **`go.mod`/`go.sum` made tidy.** `golang.org/x/sys` was marked indirect but is a direct
  dependency of the new file-lock code.
- **CI hardened** — three additions, all verified to pass locally before being made
  blocking: `go mod tidy` drift check, `golangci-lint` (pinned v2.12.2), and
  `govulncheck` (on `stable` Go, since stdlib advisories are fixed by toolchain patches).
- **`.golangci.yml`** — took 20 findings to 0 without changing correct code. Two
  exclusions are deliberate and commented: `SA1019` on `stripServerOwned`'s `SelfLink`
  write (zeroing a deprecated server-owned field is the *point* of that function), and
  `errcheck` in `_test.go` setup.
- **`CHANGELOG.md`** rewritten into Keep-a-Changelog form, with 0.1.1 dated and 0.1.0
  explicitly marked **Withdrawn — do not install**.
- **`.github/dependabot.yml`** — gomod (with `k8s.io/*` grouped, since a partial bump
  compiles but disagrees about API types), github-actions, docker.
- **`.github/PULL_REQUEST_TEMPLATE.md`** added.
- **`Dockerfile:3`** — corrected the "~2 MB" claim to ~41 MB, the real size of a static
  client-go binary.

### Two items I deliberately did not do

- **Lowering the `go 1.26.0` floor.** Not possible: `k8s.io/client-go`, `k8s.io/api`, and
  `k8s.io/apimachinery` v0.36.1 all declare `go 1.26.0` themselves. The floor is inherited,
  not gratuitous. The earlier recommendation to lower it was wrong.
- **Adding `workflow_dispatch` to `release.yml`.** Version is derived from `GITHUB_REF` in
  four places across four jobs; centralising that risks breaking a pipeline now known to
  work, and RC tags already provide a safe rehearsal. Not worth the churn.

---

## Open, none tag-blocking

1. **5 stdlib advisories** (`net/url`, `crypto/tls`, `net/http`, `encoding/asn1`) fixed in
   go1.26.6. This machine runs 1.26.5. CI and the `golang:1.26-alpine` build stage both
   resolve to the latest patch, so they clear on the next build; no code change needed.
2. **`Chart.yaml` has no `icon`** — the only `helm lint` finding. Left alone: there is no
   logo asset in the repo and inventing a URL would be worse than the INFO.
3. **No cosign signing or SBOM.** Checksums cover the minimum for v0.1.1.
4. **`idFromTime` is millisecond-precision with no collision guard**
   (`internal/storage/local.go:372`). Unreachable at the 300 s default, reachable at the
   10 s interval the demo docs suggest. The writer lock makes it much harder to hit.
5. **PVC has no `helm.sh/resource-policy: keep` option** — uninstall destroys snapshots,
   which `install.md` does document.
6. **`docs/PROGRESS.md` and `docs/audit-preproduction-v0.1.1.md` are in Spanish** in an
   otherwise English public repo, and read as internal working logs. Decide whether they
   should ship in a public release.

---

## Remaining sequence

1. Delete or deprecate the orphaned `0.1.0` GHCR packages.
2. Push `v0.1.1-rc.1`; confirm all four artefact types land.
3. Push `v0.1.1`; verify the image pulls, the OCI chart installs, binaries download, and
   `:latest` has moved.
4. Make the docs public.

The repo is safe to show before step 3 — the merged docs are already explicit that
nothing is published yet.
