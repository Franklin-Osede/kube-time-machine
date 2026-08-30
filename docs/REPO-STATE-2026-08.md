# kube-time-machine — state of the repo, and what's left

**Date:** 2026-08-28.
**Analysed:** `origin/main` @ `2d33053` (2026-06-02) and `origin/fix/prelaunch-correctness` @ `6bf3cb9` (2026-07-03), cloned fresh from GitHub.
**Remote re-verified** 2026-08-28 09:13 UTC: two branches, no tags, both tips unchanged. `6bf3cb9` is the newest commit in the repo.
**Supersedes:** `docs/PRE-DEPLOY-AUDIT.md`, which audited `main` in isolation and did not have the branch's contents. Roughly half of its blockers are already fixed on the branch.

---

## The one-paragraph answer

The engineering is in good shape and is **well past what the roadmap calls Phase 1**. The project is stuck on something else entirely: **the real work lives on an unmerged branch, and the project has never published a single artifact.** `git ls-remote --tags` is empty; there is no GitHub Release, no `ktm-agent` image at `0.1.1`, no OCI chart. `main` is 18 commits and seven weeks behind the branch. The last commit anywhere is 2026-07-03 — about eight weeks ago. What's left is not mostly code. It is a merge, a release dry-run, and a decision about scope.

---

## Two repos, and only one of them is real

| | `main` | `fix/prelaunch-correctness` |
|---|---|---|
| Tip | `2d33053`, 2026-06-02 | `6bf3cb9`, 2026-07-03 |
| Go code | 2,098 non-test / 2,399 test | 3,577 non-test / 3,902 test |
| Packages | 5 | 6 (`internal/health` added) |
| Test files | 13 | 15 |
| Repo furniture | LICENSE only | + CHANGELOG, SECURITY, CONTRIBUTING, CODE_OF_CONDUCT, issue templates |
| Docs | 7 ADRs + 5 docs | + `runbook.md`, `launch.md`, `audit-preproduction-v0.1.1.md` |

The branch is `main` plus 18 commits, fast-forwardable (`main...branch` = `0 18`). It is **+4,599 / −204 across 53 files** — it roughly doubles the project. `gofmt` is clean on both.

Everything below describes the branch, because that is the actual state of the work. Treating `main` as the project's state would be misleading.

---

## State by area

### Delta engine — solid

`internal/delta` is 87 lines with a stated round-trip invariant (`Apply(prev, Compute(prev, next)) == next`), a fuzz target, and 100% coverage per the roadmap. Kubernetes-agnostic by design. This is the part that most needed to be right and is.

### Storage — hardened well beyond where `main` left it

`internal/storage/local.go` went from 300 to ~615 lines. Since `main` it gained: fsync on file and parent directory, payload-written-before-meta so meta is the commit point, index rebuild from `snapshots/` when `index.json` is missing *or corrupt*, payload validation during rebuild, orphan cleanup when the index append fails, same-millisecond ID collision rejected rather than silently overwriting, and a cross-platform writer lock (`flock` on Unix, `LockFileEx` on Windows) so a second agent cannot corrupt the store.

That is a genuinely good response to the failure modes. Remaining, from your own audit: every method still ignores its `context.Context`, there is no ENOSPC classification or low-watermark, and — the one I'd actually fix — `Get`/`Delete` accept a `SnapshotID` straight into a path join with no traversal validation (R-14).

### Agent — the scope-lock is gone

Since `main` the agent gained: a readiness gate so the first (always-full) snapshot cannot capture a half-synced cluster, chain state that only advances after a successful `Put`, a 2-minute dynamic-informer sync timeout, namespace exclusions defaulting to `kube-system`/`kube-public`/`kube-node-lease`, GC/retention that preserves a reconstructable full anchor, flush-health tracking that degrades `readyz` after 3 consecutive failures, `/healthz`, `/readyz` and `/metrics`, and **dynamic informers** for arbitrary GVRs.

Three more that are easy to miss reading the tree, and that I under-reported on first pass:

- **Reactive burst flush.** `Snapshotter` runs a dual ticker: alongside the normal `interval`, it polls `Buffer.pendingChanges` every 10s and flushes early once 50 changes accumulate (`snapshot.go:29-96`, `WithBurstFlush`). The cadence is no longer purely fixed-interval — during a deployment storm it tightens automatically. Good idea, and it materially improves the forensic resolution the whole pitch rests on.
- **Controller-annotation stripping.** `sanitiseMeta` now drops `kubectl.kubernetes.io/last-applied-configuration` plus Helm/Argo CD/Flux annotations, so routine tool operations stop producing spurious MODIFIED deltas. Note this *reverses* the reasoning in `main`'s `marshal.go` comment, which explicitly kept `last-applied-configuration` as "operationally meaningful". That's a real decision reversal with no ADR.
- **`SnapshotMeta.Kinds`** is populated on every Put and lets `blame` skip loading deltas that can't contain the target kind (`blame.go:137-148`), with a documented fallback for snapshots written before the field existed.

Which is where the strategic problem shows up — see the next section.

### CLI and rollback — correct, but not production-grade

**Actor attribution is the headline feature and I missed it first time round.** `extractManagers` pulls `managedFields[*].manager` before the field is cleared and re-injects it as a synthetic `ktm.io/managers` annotation (`marshal.go:129-157`); `blame` decodes it into an `ACTORS` column (`blame.go:35, 96, 197-213`). That is the actual "git blame" promise — *who* changed it, not just what and when — and it isn't on the roadmap at all. It's arguably the most differentiating thing in the repo and it appears nowhere in the README, the comparison table, or `launch.md`. If you launch without leading on this, you're burying the lede.

`reconstruct` also gained cycle detection in the delta-chain walk (`reconstruct.go:38`), and `storage.Delete` now exists to support GC.

ADR-0006 is implemented properly: the preview RV is captured once and reused, no re-Get before apply, 409 produces an actionable message, and the preview now sanitises the live object with the agent's own marshaller so `status`/`managedFields` don't leak into the consent moment.

The gaps your audit rates High are still open in committed code, and I confirmed all three:

- **R-07 — no cluster guardrail.** `kubeclient.BuildConfig` resolves kubeconfig implicitly, and there is no `--context` or `--expected-cluster` flag. `--yes` removes the only human pause. `ktm rollback ... --yes` against the wrong current-context writes to the wrong cluster.
- **R-08 — create-on-404 is silent and default-on.** A resource deliberately deleted gets recreated with no distinct opt-in. No `--allow-create` flag exists.
- **R-10 — asymmetric test coverage.** There are 12 rollback tests, but no ConfigMap Conflict case, no ConfigMap 404 case, and `TestRunRollback_UnknownKindErrors` only exercises the dispatch error, not `runRollback` end-to-end.

### Helm chart — good security defaults, some operational gaps

Right: `replicas: 1` + `Recreate` with the reasoning documented, non-root/65532, `readOnlyRootFilesystem`, `drop: ["ALL"]`, `seccompProfile: RuntimeDefault`, cluster-scoped **read-only** RBAC, namespace-suffixed ClusterRole names, NetworkPolicy ingress deny-all with a hole opened only for the health port when health is enabled, a `values-prod.yaml`, and `NOTES.txt`.

Still open: no `image.digest` support (R-16), no `helm.sh/resource-policy: keep` on the PVC so uninstall destroys history (R-17), `storage.create=false` has no `existingClaim` so the BYO-PVC comment is misleading (R-18), and egress is still `0.0.0.0/0`.

### CI/CD — good gates, no supply chain

CI runs gofmt, vet, `go test -race`, build, `helm lint`, `helm template`, and a health-toggle contract assertion. That last one is a nice touch — it asserts rendered output, not just that rendering succeeds.

`release.yml` now publishes **both** binaries across 5 platforms, gates `:latest` to stable tags only, marks hyphenated tags as prereleases, emits `checksums.txt`, and stamps OCI source/version/revision labels. That closes most of what I flagged against `main`.

What CI still has none of: `docker build` of the actual Dockerfile, image scanning (Trivy/Grype), `govulncheck`, cosign signing, SBOM/provenance, `kubeconform`, and any smoke install against an ephemeral cluster. For a tool that asks for cluster-wide read on ConfigMaps, unsigned artifacts are a real adoption barrier.

### Docs — unusually good, and now drifting

The doc set is better than most projects at this stage: 7 ADRs, an architecture doc with a Mermaid pipeline, a Velero/ArgoCD/events comparison, an operational runbook, a launch-post draft, and a 425-line self-audit. The README's framing is now honest ("launch preparation is in progress… not published yet, build from source"), and the security section that `main`'s install.md linked to but never had now exists.

The drift is the cost of moving fast on the branch. Your own audit lists 12 doc/code discrepancies; the ones that matter are that `roadmap.md` still says "no metrics" while `/metrics` is live, `PROGRESS.md` still says "no fsync" and lists retention as pending, and ADR-0007 still says the release workflow is deferred.

And a structural one your audit doesn't call out: **the last four ADRs are numbered 0004–0007 and all predate this branch.** Retention/GC, the health server, dynamic informers, and the writer lock are four load-bearing architectural decisions with no ADR. The project's own convention (ADR-0001) has quietly lapsed exactly where the decisions got interesting.

### Tests — good ratio, thin at the edges

3,902 test lines against 3,577 non-test — better than 1:1, with a fuzz target and race-clean runs. `cmd/` and `pkg/types` are untested, which is fine for their size. The real gaps are the rollback asymmetry (R-10) and no end-to-end test that exercises `runRollback` against a fake transport.

---

## The thing I'd actually push back on

`docs/roadmap.md` says, in bold: *"Strict scope-lock: Deployments and ConfigMaps only. Local PVC storage only. No web UI. **No metrics.** No multi-cluster."* And the Phase 2 gate is explicit — that work starts **only** after 50+ stars, 500+ post likes, or real user requests.

The branch reaches into Phase 2 three times before Phase 1 has launched:

| Roadmap | Feature | Status on branch | On by default? |
|--:|---|---|---|
| P0 | Extended resource support | dynamic informers for arbitrary GVRs | **No** — `watchResources: []`, Phase 2 RBAC rules removed from `role.yaml`, opt-in via `rbac.extraRules` |
| P3 | Metrics | `/metrics` in `internal/health/server.go` | Yes |
| P7 | Automatic retention | GC with full-snapshot anchor, `retainDays: 30` | Yes |

Credit where it's due on P0: `dc810a6` originally shipped a default watch set of StatefulSets/Services/Ingresses/HPAs *with* matching ClusterRole rules, and `6bf3cb9` deliberately walked that back to an empty default with the extra RBAC removed — self-corrected before anyone asked. So P0 is a dormant mechanism, not a granted capability, and the MVP's minimal-RBAC promise still holds.

Metrics and retention are genuinely on by default, though, and retention in particular is load-bearing enough that it needs its own ADR before it ships.

The pattern is still worth naming. The gate exists to stop a solo project spending its budget on features nobody has asked for, and the budget went there anyway — `dc810a6` and `6bf3cb9` are +1,011 and +2,563 lines. Meanwhile the one thing the gate was protecting, *ship it and see if anyone cares*, has slipped from May to August with zero artifacts published and eight weeks of silence since.

One caveat on the self-assessment: `docs/audit-preproduction-v0.1.1.md` was **added by `6bf3cb9`, the same commit whose message claims to close R-03, R-04, R-06 and R-21**. The audit and the fixes it grades landed in one pass (co-authored with Claude Sonnet 4.6, per the trailer). I independently confirmed the mechanisms are present — GC before flush, the writer lock wired in `main.go` with `defer`, `FlushHealthy(3)` on readyz, the emptied watch set — so the claims hold up structurally. But a grade written by the same pass that wrote the fix isn't an independent one, and R-21 doesn't appear in the audit's own risk table at all. Treat the NO-GO/closed markers as a good checklist, not as verification.

The cheapest path to knowing whether this project deserves more of your time is a tag, not another feature. Everything in the "later" tiers below is genuinely optional until someone other than you has installed it.

---

## What's left

### Tier 0 — unblock the release (this is the whole job)

1. **Open and merge the PR.** The branch is fast-forwardable. Squash or keep the history, but get `main` to represent reality — right now `main` is what visitors read, and it's the version with the false "v0.1.0 shipped" claim.
2. **Freeze a commit.** Your audit's R-01 notes the audited tree had uncommitted changes; that's since been committed as `6bf3cb9`, but re-verify `git status` is clean before tagging.
3. **Run the gate locally** — `make test`, `go test -race ./...`, `helm lint deploy/helm`, `helm template`. I could not run these (see below).
4. **Tag `v0.1.1-rc.1`.** This is the first time `release.yml` will have run in its current form. Verify: multi-arch digest for amd64+arm64, ten binaries, `checksums.txt`, and the OCI chart push succeeding with only `packages: write`. `PROGRESS.md` has flagged that last unknown since May — find out on an RC, not on the real tag.
5. **Install the RC from OCI into a throwaway cluster.** Not from `deploy/helm` — from `oci://ghcr.io/franklin-osede/charts/kube-time-machine`. Confirm the pod reaches Ready, `/healthz` and `/readyz` answer, and the first snapshot lands on the PVC.
6. **Walk the runbook on that cluster**, particularly whether `kubectl debug --target=agent` and `/proc/1/root` work on your target runtime. Have a helper-Pod fallback if not.
7. **Tag `v0.1.1`,** verify all three artifacts resolve, then update README/install.md to point at the published artifacts instead of build-from-source.
8. **Record the demo and publish the post.** `docs/launch.md` is drafted. Enable Discussions before you send traffic.

Realistically 1–2 focused sessions. Nothing here is hard; it's all the kind of work that's easy to keep deferring in favour of another fix.

### Tier 1 — before you tell anyone to run Mode B in production

These are your own audit's open risks, and I confirmed each is still open in committed code:

- **R-14** path traversal — validate `SnapshotID` is a single well-formed segment and that the resolved path stays under root. Small fix, real bug.
- **R-17** PVC has no `keep` policy; `helm uninstall` destroys the history. Add a values flag.
- **R-18** `storage.existingClaim` — the BYO-PVC path advertised in `values.yaml` doesn't exist.
- **R-16** `image.digest` for strict-pinning environments.
- **R-12** memory sizing is unvalidated above ~500–1000 resources against a 256Mi limit. One benchmark run would either close it or change the defaults.
- **R-11** `DrainChanges` runs before the flush result is known.
- **R-20** the health port is open to anything the CNI selects, and `/metrics` shares it.

### Tier 2 — before rollback becomes a documented production procedure

Do not put `ktm rollback` in a team runbook until R-07, R-08 and R-10 are closed. Concretely: a `--context`/`--expected-cluster` guardrail that prints server and context and requires confirmation; `--allow-create` gating the 404 path with abort as the default; `IsNotFound` on Update treated like Conflict (R-09); and the missing symmetric ConfigMap tests plus one real `runRollback` test.

For read-only forensics none of this blocks you — which is worth saying explicitly in the README, since "roll it back in seconds" is the headline.

### Tier 3 — supply chain

`govulncheck`, `docker build` in CI, Trivy or Grype before push, cosign keyless signing for image + chart + checksums, SBOM and provenance, `kubeconform` over `helm template`. Roughly a day, and it's what a platform team looks for before installing a third-party agent with cluster-wide read.

### Tier 4 — doc/code drift

Sweep the 12 discrepancies in `docs/audit-preproduction-v0.1.1.md` §"Discrepancias documentación/código". Then write the missing ADRs — retention/GC, health+metrics, dynamic informers, the writer lock, burst flush, and the annotation-stripping reversal (ADR-0005's marshaller comment on `main` says `last-applied-configuration` is kept as meaningful; the branch strips it) — or explicitly retire the ADR convention. Half-followed conventions are worse than none.

Also fix `docs/roadmap.md`: it claims a scope-lock the code no longer honours.

And put actor attribution in the README, the comparison table and `launch.md`. "Who ran `kubectl edit`" is the thing none of Velero, ArgoCD history or expired events can answer, it's implemented, and right now it's invisible to anyone reading the repo.

### Tier 5 — Phase 2, still gated

The backlog in `roadmap.md` is well-ordered and I wouldn't change it. P0/P3/P7 are now partly done ahead of schedule. Leave the rest closed until the gate opens.

---

## Scorecard

| Area | State |
|---|---|
| Delta engine | Strong — invariant-tested, fuzzed, 100% |
| Storage durability | Strong on branch; traversal validation missing |
| Agent lifecycle | Strong — sync gate, chain integrity, health, writer lock |
| Rollback safety | Correct concurrency; missing operator guardrails |
| Helm chart | Strong security defaults; operational gaps |
| CI | Good correctness gates; no supply chain |
| Release pipeline | Complete on paper; **never executed** |
| Docs | Excellent breadth; drifting, ADRs lapsed |
| Tests | Good ratio; asymmetric at rollback |
| **Shipped** | **Nothing. No tag, no release, no artifact.** |

---

## What I could not verify

- **No build or test run.** `go.mod` requires `go 1.26.0`; the sandbox has 1.24.7 and no egress to `proxy.golang.org`, so the toolchain won't download. `helm` isn't installed either. `gofmt -l` is clean on both branches — that's the only gate I could actually execute. Everything I say about correctness comes from reading the code and from your own audit, not from a green test run.
- **GitHub-side state.** The API returned 403 through the tooling available here, so I could not check open PRs, Actions run history, stars, or whether Discussions is enabled. Git-protocol access works: `git ls-remote` confirms two branches and zero tags as of 2026-08-28 09:13 UTC. `6bf3cb9` set CI to run on all branches, so there should be a run against the branch head — worth a 30-second look at whether it's green.
- **Branch behaviour under load.** R-12 (memory at scale) can only be closed with a real cluster.
