# Changelog

All notable changes to this project are documented here.

## [Unreleased]

- Restore Go formatting, race, vet, build, Helm lint, render, and health-toggle
  checks in CI.
- Add release checksums and OCI image source/version/revision labels.
- Add snapshot retention with a reconstructable full-snapshot anchor.
- Add opt-in dynamic resource watches while keeping MVP RBAC minimal by default.

## [0.1.1] - Unreleased

- Publish `ktm` and `ktm-agent` binaries for Linux, macOS, and Windows.
- Publish the multi-architecture agent image and OCI Helm chart.
- Add optimistic-concurrency rollback and declarative-state history commands.

## [0.1.0]

- Initial MVP implementation.

[Unreleased]: https://github.com/Franklin-Osede/kube-time-machine/compare/v0.1.0...HEAD
[0.1.1]: https://github.com/Franklin-Osede/kube-time-machine/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Franklin-Osede/kube-time-machine/releases/tag/v0.1.0
