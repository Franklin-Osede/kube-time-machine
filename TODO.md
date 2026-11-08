# TODO

Open work, carried over from the pre-release audits when they were retired.
Items move to issues or the CHANGELOG as they are picked up.

## Before v0.1.1

- [ ] At the final tag: date the `[0.1.1]` CHANGELOG entry (now
      "Unreleased") and drop the README "not published yet" notice.
- [ ] Tag `v0.1.1-rc.2`: first run of the release `verify` job and of the
      bumped release-only actions (buildx, build-push, upload/download-artifact).
- [ ] Scan the published image, not just the one E2E builds.
- [ ] Move `docs/launch.md` (post drafts) out of the repository.

## Correctness

- [ ] `idFromTime` has millisecond precision and no collision guard. Safe at
      the 300 s default; reachable at short intervals.

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
