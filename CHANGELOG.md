# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [0.1.1] - 2026-08-24

First supported release. v0.1.0 was withdrawn (see below); v0.1.1 is the
version to install.

### Added

- Publish `ktm` and `ktm-agent` binaries for Linux, macOS, and Windows
  (amd64 + arm64). Both are required for Mode A: `ktm-agent` records, `ktm`
  queries.
- Publish the multi-architecture agent image and the OCI Helm chart, with
  SHA-256 checksums attached to the release and source/version/revision
  labels on the image.
- Snapshot retention (`--retain-days`) with garbage collection that keeps the
  most-recent full snapshot before the cutoff as an anchor, so every delta in
  the retention window stays reconstructable.
- Health server exposing `/healthz`, `/readyz`, and `/metrics`, wired to
  Kubernetes liveness and readiness probes and toggleable via
  `agent.health.enabled`.
- Opt-in dynamic resource watches, while keeping the default RBAC minimal.
- `excludeNamespaces` to keep high-churn namespaces out of the history.
- Optimistic-concurrency rollback and declarative-state history commands.
- Formatting, race, vet, build, Helm lint, render, and health-toggle contract
  checks in CI.

### Fixed

- The agent no longer persists an empty full snapshot when the informer caches
  never sync. Previously a CrashLoopBackOff under bad RBAC would append an
  empty reference snapshot on every restart, producing a history whose newest
  full snapshot claimed the cluster was empty.
- Snapshot writes are now durable and recoverable: payload is written before
  metadata so the metadata is the commit point, `fsync` covers both the file
  and its parent directory, and a missing or corrupt `index.json` is rebuilt
  from `snapshots/` instead of preventing startup.
- `:latest` is no longer repointed by prerelease tags.
- Removed the unused `resources.watch` chart value, which collided with the
  conventional Helm `resources` key and silently did nothing when set.
- The NetworkPolicy now admits kubelet probe traffic and scopes egress to DNS
  and the API server instead of `0.0.0.0/0`.

### Security

- Documented what the PVC holds: the agent persists full ConfigMap `data`
  cluster-wide as plaintext JSON. Secrets are deliberately not watched and the
  ClusterRole is read-only. See the Security section of `docs/install.md`.

## [0.1.0] - Withdrawn

Initial MVP implementation. The tag and GitHub Release were removed after
publication, leaving unreferenced `0.1.0` packages in GHCR that do not
correspond to any commit in this repository. **Do not install `0.1.0`** — it
predates the correctness and durability fixes listed under 0.1.1.

[Unreleased]: https://github.com/Franklin-Osede/kube-time-machine/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/Franklin-Osede/kube-time-machine/releases/tag/v0.1.1
