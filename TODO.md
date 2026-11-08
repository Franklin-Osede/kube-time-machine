# TODO

Open work, carried over from the pre-release audits when they were retired.
Items move to issues or the CHANGELOG as they are picked up.

## Before v0.1.1

- [ ] At the final tag: date the `[0.1.1]` CHANGELOG entry (now
      "Unreleased"), set `VERSION` / `CHART_VERSION` to the stable release in
      the README, `docs/install.md` and `docs/runbook.md`, and update the
      README status table.
- [ ] Tag `v0.1.1-rc.3` from the fixes made after rc.2 (rollback annotations,
      writer lock, x/net, release assets limited to the binaries) and verify it
      with the `release-gate` skill. rc.2 ran the pipeline end to end and is
      verified; its release still carries a stray `.dockerbuild` asset.
- [ ] Scan the published image, not just the binaries and the image E2E builds.
- [ ] `docs/launch.md` stays marked PENDING until the stable release and the
      demo link exist; move it out of the repository once the posts are out.

## Correctness

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
- [ ] No cosign signature or SBOM on releases; checksums only.
- [ ] Chart has no `icon` (the only `helm lint` finding) and no image digest
      pinning.

## Documentation

- [ ] Decisions that shipped without an ADR: retention/GC with anchor, health
      and metrics endpoints, dynamic informers, the writer lock, burst flush,
      and stripping `last-applied-configuration`. Write them or retire the ADR
      convention.

## Cleanup

- [ ] Comment pass over every package against `CONTRIBUTING.md` § Comments.
- [ ] Consider splitting `internal/storage/local.go` (index, delete, atomic
      writes, locking).
- [ ] Decide whether `DynamicInformers` ships in v0.1.1 given the scope-lock.
