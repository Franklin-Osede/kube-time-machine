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
  checks in CI, plus `go mod tidy` drift, `golangci-lint`, and `govulncheck`.
- `ktm rollback` now prints the destination cluster — context, API server, and
  kubeconfig user — before applying anything, including under `--yes`, and
  accepts `--context` to select a kubeconfig context explicitly.
- `storage.retain` (default `true`) annotates the PVC with
  `helm.sh/resource-policy: keep` so `helm uninstall` no longer destroys the
  recorded history, and `storage.existingClaim` binds a fresh install to a PVC
  kept from a previous release.
- `values.schema.json`, so invalid chart values fail at `helm install` rather
  than as a CrashLoopBackOff. It also rejects non-read-only verbs in
  `rbac.extraRules`, which would otherwise silently widen a cluster-scoped role.

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
- Invalid agent flags are rejected at startup instead of panicking.
  `--interval=0` previously reached `time.NewTicker` and panicked, after the
  Kubernetes clients were built, storage was opened, and the writer lock taken.
- `--watch-resources` now requires an explicit version (`resource[.group]/version`).
  The previously documented short forms never worked: the dynamic client performs
  no discovery, so a version-less GVR left the pod Running with
  `WaitForCacheSync` blocked forever.
- Snapshot IDs are validated before being resolved to a path. A manipulated
  `index.json` could previously have directed the retention pass's `RemoveAll`
  outside the snapshots directory.
- `ktm rollback` no longer recreates a resource that has been deleted from the
  cluster. A 404 previously fell through to Create, so rolling back could
  silently resurrect a deliberate deletion; recreation is now opt-in behind
  `--allow-create`.
- The health server sets read, read-header, write, and idle timeouts; it
  previously had none while the NetworkPolicy admits kubelet to its port.
- Released binaries are built with a pinned Go 1.26.6 toolchain. The release
  workflow inferred its version from `go.mod`, whose `go 1.26.0` directive names
  an exact patch, so published binaries would have shipped the vulnerable
  standard library.
- Bumped `golang.org/x/net` to v0.55.0 and `golang.org/x/text` to v0.39.0 for
  advisories reachable from `ktm rollback`.
- Removed the unused `resources.watch` chart value, which collided with the
  conventional Helm `resources` key and silently did nothing when set.
- The NetworkPolicy now admits kubelet probe traffic, so liveness and readiness
  probes are not blocked by the deny-all ingress rule. Egress remains open
  (`0.0.0.0/0`) with DNS called out explicitly; narrowing it is a Phase 2
  follow-up tracked in `deploy/helm/templates/networkpolicy.yaml`.

### Security

- Snapshot directories and files are now created `0700`/`0600` rather than
  `0755`/`0644`.

  **Migration:** existing data is not relabelled. `index.json` picks up the
  tighter mode on its next write, but snapshot `meta.json` and payload files are
  write-once and keep `0644`. To relabel an existing store in place:

  ```bash
  # In-cluster (Mode B), against the agent's PVC mount:
  chmod -R go-rwx /var/lib/ktm
  # Local (Mode A):
  chmod -R go-rwx <your --storage-dir>
  ```

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
