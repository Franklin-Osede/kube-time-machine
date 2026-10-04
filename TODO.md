# TODO

Open work, carried over from the pre-release audits when they were retired.
Items move to issues or the CHANGELOG as they are picked up.

## Before v0.1.1

- [ ] README says artefacts are unpublished and CHANGELOG dates 0.1.1 as
      released, while only `v0.1.1-rc.1` exists. Reconcile at release time.
- [ ] Triage the open Dependabot PRs. The `golang:1.27.1-alpine` bump conflicts
      with the toolchain pin from `5d9842a`.
- [ ] Scan the published image, not just the one E2E builds.
- [ ] Move `docs/launch.md` (post drafts) out of the repository.

## Correctness

- [ ] A failed flush loses the burst signal: `DrainChanges()` runs before
      `Flush` in `internal/agent/snapshot.go`, so burst detection is disarmed
      until the next periodic tick. Degrades responsiveness, not data.
- [ ] `idFromTime` has millisecond precision and no collision guard. Safe at
      the 300 s default; reachable at short intervals.

## Operational

- [ ] NetworkPolicy egress allows `0.0.0.0/0`, so cloud metadata at
      169.254.169.254 is reachable.
- [ ] No warning when the storage volume passes 80% full.
- [ ] No cosign signature or SBOM on releases; checksums only.
- [ ] Chart has no `icon` (the only `helm lint` finding) and no image digest
      pinning.

## Documentation

- [ ] ADR-0007 still describes `release.yml` as deferred; it shipped. Add a
      status note rather than rewriting the record.
- [ ] Check the rollback output quoted in `docs/runbook.md` against
      `internal/cli/rollback.go`.
- [ ] Decisions that shipped without an ADR: retention/GC with anchor, health
      and metrics endpoints, dynamic informers, the writer lock, burst flush,
      and stripping `last-applied-configuration`. Write them or retire the ADR
      convention.

## Cleanup

- [ ] Comment pass over every package against `CONTRIBUTING.md` § Comments.
- [ ] Decide whether `pkg/types` should be `internal/types` — `pkg/` promises
      a stable import path.
- [ ] Consider splitting `internal/storage/local.go` (index, delete, atomic
      writes, locking).
- [ ] Decide whether `DynamicInformers` ships in v0.1.1 given the scope-lock.
