# TODO

Open work, carried over from the pre-release audits when they were retired.
Items move to issues or the CHANGELOG as they are picked up.

## Before v0.1.1

- [ ] On the day `v0.1.1` is tagged, in the commit that gets the tag: date
      the `[0.1.1]` CHANGELOG entry with that day, and set `VERSION` /
      `CHART_VERSION` to the stable release in the README, `docs/install.md`
      and `docs/runbook.md`. Change the README status table to "released"
      only after the post-tag `release-gate` checks pass.
- [x] `v0.1.1-rc.3` tagged on `118457c` and verified with `release-gate`
      (2026-11-09). rc.2's release still carries a stray `.dockerbuild`
      asset; cleaning it up can wait.
- [x] Scan the published image: rescanned for rc.4, see
      [docs/security/image-scan-v0.1.1-rc.4.md](docs/security/image-scan-v0.1.1-rc.4.md).
- [x] Existing owner acceptance for the same four image findings (2026-11-09,
      [PR #24](https://github.com/Franklin-Osede/kube-time-machine/pull/24#issuecomment-6738416187)).
- [x] Tag `v0.1.1-rc.4` from the comment trim (PR #25) and the fixes after
      rc.3 (Delete tombstone retry, GC chain under a clock step back,
      `--watch-resources` refusing the typed kinds, `excludeNamespaces: []`,
      rollback exit code, rollback keeping live owners and finalizers,
      `--watch-resources` Kind and Secrets checks, the first snapshot
      waiting for the handlers, the memory defaults), run the full gate and E2E on that exact commit,
      and verify it with `release-gate`. Tagged on `8617653` and verified
      on 2026-11-15; evidence is in the rc.4 scan record above. After rc.4 the
      scope is frozen: only security, data-loss or installation defects reopen it.
- [ ] Rescan the stable image after it is published and before the public
      announcement; add its record under `docs/security/`.
- [x] Launch drafts moved outside the repository; keep them unpublished until
      the stable release and demo link are verified.

## Correctness

- [ ] `Local.List` copies the index slice but not each entry's `Kinds` slice,
      so a caller can mutate stored metadata. No caller does today; clone it.
- [ ] `ktm blame` replays deltas in timestamp order and ignores `PrevID`.
      After a clock step back across a restart it can disagree with
      `ktm snapshot show`, which follows the chain. A full written after restart
      can sort between an older full and its quiet delta, causing blame to omit
      a transition that `snapshot show` can reconstruct.
- [ ] Reconcile committed snapshot directories missing from a valid index. GC
      cannot collect an unindexed delta or protect its base. A later rebuild
      can expose a delta whose base has already expired.
- [ ] If `atomicWriteJSON` fails after its rename (the parent-directory
      fsync), `appendIndex` reports failure although `index.json` already
      lists the new ID, and `PutFull`/`PutDelta` remove the directory. The
      next index write corrects it; if the agent stops first, the entry
      dangles until `Delete` drops it. Needs a failing-fsync test hook.

- [ ] Snapshot IDs have millisecond precision. A collision is detected:
      `writeSnapshot` refuses an existing directory and the flush fails, so
      nothing is overwritten. The next tick retries the same cadence slot and
      includes the unwritten changes. A collision needs two flushes in the
      same millisecond; flushes are serialised and each fsyncs, so in practice
      it needs a clock step back.

- [ ] Test that the agent's shutdown wiring passes combined typed and dynamic
      readiness to the final flush.
- [ ] Define nil and empty snapshot state equality explicitly in the delta
      round-trip contract.

## Operational

- [ ] The typed informers have no initial-sync timeout, while the dynamic ones
      give up after two minutes. With RBAC denied, the agent stays
      Running-but-not-Ready indefinitely instead of failing with a clear
      error. Decide on one behaviour; a timeout must allow for slow initial
      lists on large clusters (the runbook cites 60–90 s).
- [ ] Excluded namespaces are filtered after the informers cache them, so
      their objects still cost memory. A `metadata.namespace!=` field selector
      on the list/watch would skip them at the API server. Measure first.

- [ ] NetworkPolicy egress is IPv4-only (`0.0.0.0/0`): on an IPv6-only cluster
      the agent could not reach the API server. Untested; verify before
      claiming IPv6 support.
- [ ] No warning when the storage volume passes 80% full.
- [ ] A full snapshot allocates about 6.5–7× the recorded state, mostly
      `json.MarshalIndent` buffers in `atomicWriteJSON`, and stores each
      object base64 encoded. Benchmark dropping the indentation first; the
      saving is unmeasured. Verify any format change against rc.3 in both
      directions: rc.3 reads the new store, the new build reads rc.3's.
      Evidence: `docs/install.md` § Sizing memory.
- [ ] Memory was measured for minutes on one kind node: no soak test, and
      nothing above ~50 MB of ConfigMap data.
- [ ] `--watch-resources` collisions are checked only against the current
      list. A guard that walks the stored history's `meta.Kinds` would catch
      a list changed on an existing PVC; until then the docs require a new
      PVC.
- [ ] A `networkPolicy.healthIngressFrom` value to narrow who can reach the
      health port, once tested against the CNIs we support: an untested
      selector can block kubelet probes and look like a crash-looping agent.
- [ ] No signature or SBOM on releases. There are checksums, and the image
      carries BuildKit's unsigned provenance attestation (ADR-0007).
- [ ] Chart has no `icon` (the only `helm lint` finding) and no image digest
      pinning.

## Release pipeline

- [ ] `release.yml` does not check that the tag is on `main`.
- [ ] The image and chart publish in parallel with the binaries: if a binary
      build fails, the image and OCI chart exist under the version with no
      GitHub release. Needs a design, not a one-line `needs:`.
- [ ] The CI `vuln` job uses `go-version: stable` and an unpinned
      `govulncheck@latest`, not the shipped toolchain (go1.26.11), so a
      stdlib advisory fixed in a newer patch can pass CI while the binaries
      carry it.
- [ ] The `ci.yml` jobs have no `timeout-minutes` and no `concurrency` group.

## CLI and chart polish

- [ ] A rollback with no effective changes renders an empty `Modified (1)`
      preview and still sends an Update. Detect the no-op and skip the write.
- [ ] Let `KTM_STORAGE_DIR` supply the CLI storage directory when
      `--storage-dir` is omitted; keep the explicit flag highest priority.
- [ ] Extend kind aliases to dynamically watched resource kinds; currently
      only Deployment and ConfigMap have lowercase aliases.

- [ ] A missing `--storage-dir` prints a stack of wrapped errors; one helper
      could say "no snapshot store at X".
- [ ] `ktm diff` and `ktm snapshot list` accept stray positional arguments
      (`cobra.NoArgs`). No command has an `Example:` in its help, and
      `ktm-agent -help` has no description.
- [ ] Makefile: no `lint`, `vuln` or `cover` targets; `make test` runs
      without `-race`; `make help` does not list `e2e`.
- [ ] Chart: no `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`
      or `priorityClassName`, and the schema rejects them.
- [ ] Dockerfile builds arm64 under QEMU (no `--platform=$BUILDPLATFORM`),
      and base images are not pinned by digest. The e2e kind node image is
      not pinned.
- [ ] Key ordering is implemented three times (`keyLess` in `cli/diff.go`
      and `storage/local.go`, `sortedKeys` in `cli/snapshot.go`).

## Documentation

- [ ] Decisions that shipped without an ADR: retention/GC with anchor, health
      and metrics endpoints, dynamic informers, the writer lock and burst
      flush. Annotation stripping is now described in an ADR-0005 amendment.
      Write them or retire the ADR convention.

## Cleanup

- [ ] Consolidate the typed rollback Update and Create flows while preserving
      the preview, optimistic locking, and lifecycle checks.
- [ ] Wrap both errors when Delete fails to write the index and also fails to
      restore the staged snapshot.
- [ ] Make the buffer snapshot accessor private if its only callers remain tests.
- [ ] Split agent startup and retention GC into smaller units around their
      existing responsibilities.

- [x] Comment pass over every package against `CONTRIBUTING.md` § Comments
      (PR #25).
- [ ] Consider splitting `internal/storage/local.go` (index, delete, atomic
      writes, locking).
- [ ] Decide whether `DynamicInformers` ships in v0.1.1 given the scope-lock.
