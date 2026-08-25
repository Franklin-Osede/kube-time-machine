# Pre-deployment audit — kube-time-machine

**Scope:** release readiness for the first supported tag (v0.1.1).
**State:** `fix/prelaunch-correctness` merged into `main` (FF to `6bf3cb9`), plus the
follow-up work described below. **Date:** 2026-08-25.
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
- **Operability** — `/healthz`, `/readyz`, `/metrics`, liveness/readiness probes, and
  kubelet admitted through the NetworkPolicy's deny-all ingress. Egress is **not**
  narrowed: `networkpolicy.yaml:48` still allows `0.0.0.0/0`, so the agent can still
  reach cloud metadata at 169.254.169.254. Tightening it stays a Phase 2 item.
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

## Open

### Blocking the tag

**Orphaned `0.1.0` packages are still live.** See above. Nothing in the code fixes
this; it needs the packages deleted or deprecated in GHCR.

### Correctness — should fix before a wide launch

1. **`Delete` can leave a permanently stale index entry, and the code comment says
   otherwise.** `Local.Delete` removes the directory before rewriting the index, and
   defends the order by claiming `rebuildIndex` "is called on next open" so "the store
   self-heals on restart". It is not. `NewLocal` calls `loadIndex`, which rebuilds only
   when `index.json` is **missing or corrupt** — and in this crash window `index.json` is
   neither: it still holds valid pre-delete content. So a crash between `RemoveAll` and
   `writeIndexLocked` yields an index advertising a snapshot whose directory is gone, and
   it survives every subsequent restart. `Get` on that ID fails; `reconstruct` across it
   fails. Fix by validating entries on load, or by making the index the commit point as
   the write path already does.

2. **Rollback shows no cluster identity before applying.** No context, API server, or
   user is displayed, kubeconfig is resolved implicitly, and `--yes` removes the last
   prompt. Separately, a 404 silently recreates the resource from the snapshot, so a
   deliberate deletion becomes an accidental resurrection. Recreation should be opt-in
   behind `--allow-create`.

3. **Readiness can be true before anything has been persisted.** Readiness is
   `health.Ready(allReady) && snap.FlushHealthy(3)`, both of which pass immediately after
   informer sync — while the first flush can be up to `intervalSeconds` away (300s by
   default). The pod advertises Ready for five minutes without having proven it can write.

4. **A failed flush loses the burst signal.** `DrainChanges()` runs before `Flush`
   (`snapshot.go:322`). The changes themselves survive — only the counter is reset — so a
   failed flush silently disarms burst detection until the next periodic tick.

### Operational

5. **PVC is destroyed by `helm uninstall`** — no `helm.sh/resource-policy: keep`, no
   `existingClaim`, no VolumeSnapshot support. For a tool whose product *is* the history,
   losing it on uninstall is the sharpest operational edge left.
6. **NetworkPolicy egress is still `0.0.0.0/0`** (`networkpolicy.yaml:48`), deliberately,
   so the agent can always reach the API server. Cloud metadata at 169.254.169.254 stays
   reachable. Narrowing it is a Phase 2 item.
7. **No cosign signing or SBOM.** Checksums ship.
8. **No E2E test.** Nothing has ever verified install → record → change → reconstruct →
   blame → rollback against a real cluster. This is the largest untested surface.
9. **`idFromTime` has millisecond precision and no collision guard.** Safe at the 300s
   default, reachable at the 10s interval the demo docs suggest.
10. **Chart has no `icon`** (the only `helm lint` finding) and no image digest pinning.

### Documentation

11. `docs/roadmap.md:22` scope-locks to "No metrics", but `/metrics` exists and
    `roadmap.md:43` lists it as a stage. The file contradicts itself.
12. `docs/PROGRESS.md` and `docs/audit-preproduction-v0.1.1.md` are Spanish internal
    working logs in an otherwise English public repo.

---

## Remaining sequence

1. Delete or deprecate the orphaned `0.1.0` GHCR packages.
2. Push — 12 commits are still local-only, and this is the first run of the new
   lint/vuln/tidy gates on CI.
3. Fix the `Delete` index-consistency bug and the readiness gate.
4. Add rollback guardrails and put create-on-404 behind `--allow-create`.
5. `v0.1.1-rc.1` — first artefact rehearsal since the withdrawn 0.1.0.
6. Build and scan the RC image; smoke-test against a real cluster.
7. `v0.1.1`, then make the docs public.

Steps 3 and 4 are the honest gate for a *wide* launch. Steps 1, 2, 5 and 6 are the gate
for tagging at all.
