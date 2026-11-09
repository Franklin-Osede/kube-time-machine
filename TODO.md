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
- [x] Scan the published image: done for rc.3, see
      [docs/security/image-scan-v0.1.1-rc.3.md](docs/security/image-scan-v0.1.1-rc.3.md).
- [x] Owner sign-off on the assessment in that record (2026-11-09,
      [PR #24](https://github.com/Franklin-Osede/kube-time-machine/pull/24#issuecomment-6738416187)).
- [ ] Tag `v0.1.1-rc.4` from the comment trim (PR #25) and the fixes after
      rc.3 (Delete tombstone retry, GC chain under a clock step back,
      `--watch-resources` refusing the typed kinds, `excludeNamespaces: []`,
      rollback exit code), run the full gate and E2E on that exact commit,
      and verify it with `release-gate`. After rc.4 the scope is frozen:
      only security, data-loss or installation defects reopen it.
- [ ] Rescan the stable image after it is published and before the public
      announcement; add its record under `docs/security/`.
- [ ] `docs/launch.md` stays marked PENDING until the stable release and the
      demo link exist; move it out of the repository once the posts are out.

## Correctness

- [ ] `ktm blame` replays deltas in timestamp order and ignores `PrevID`.
      After a clock step back across a restart it can disagree with
      `ktm snapshot show`, which follows the chain. GC no longer deletes
      anything a kept snapshot depends on, so no data is lost; only the
      timeline order is affected.
- [ ] If `atomicWriteJSON` fails after its rename (the parent-directory
      fsync), `appendIndex` reports failure although `index.json` already
      lists the new ID, and `PutFull`/`PutDelta` remove the directory. The
      next index write corrects it; if the agent stops first, the entry
      dangles until `Delete` drops it. Needs a failing-fsync test hook.
- [ ] The payload-before-`meta.json` order in `writeSnapshot` has no test of
      its own; `rebuildIndex` rejecting a meta without a payload masks a
      swap.

- [ ] Snapshot IDs have millisecond precision. A collision is detected:
      `writeSnapshot` refuses an existing directory and the flush fails, so
      nothing is overwritten. The next tick retries the same cadence slot and
      includes the unwritten changes. A collision needs two flushes in the
      same millisecond; flushes are serialised and each fsyncs, so in practice
      it needs a clock step back.

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
      `govulncheck@latest`, not the shipped toolchain (go1.26.10), so a
      stdlib advisory fixed in a newer patch can pass CI while the binaries
      carry it.
- [ ] The `ci.yml` jobs have no `timeout-minutes` and no `concurrency` group.

## CLI and chart polish

- [ ] A missing `--storage-dir` prints a stack of wrapped errors; one helper
      could say "no snapshot store at X".
- [ ] `ktm diff` and `ktm snapshot list` accept stray positional arguments
      (`cobra.NoArgs`). No command has an `Example:` in its help, and
      `ktm-agent -help` has no description.
- [ ] Makefile: no `lint`, `vuln` or `cover` targets; `make test` runs
      without `-race`; `make help` does not list `e2e`; `make build` does
      not stamp a version.
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

- [x] Comment pass over every package against `CONTRIBUTING.md` § Comments
      (PR #25).
- [ ] Consider splitting `internal/storage/local.go` (index, delete, atomic
      writes, locking).
- [ ] Decide whether `DynamicInformers` ships in v0.1.1 given the scope-lock.
