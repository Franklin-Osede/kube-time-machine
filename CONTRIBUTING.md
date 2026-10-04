# Contributing

Focused issues and pull requests are welcome. For security reports, follow
[SECURITY.md](SECURITY.md) instead of opening a public issue.

## Development setup

Prerequisites:

- The Go version declared in `go.mod`.
- Helm 3 for chart changes.
- Access to a disposable Kubernetes cluster for integration checks.

Run the local validation suite:

```bash
make fmt
make vet
make test
make build
helm lint deploy/helm
helm template ktm deploy/helm --namespace ktm-system >/dev/null
```

Race-sensitive changes should also pass:

```bash
go test -race -count=1 -timeout 5m ./...
```

## Comments

A comment earns its place by telling a reviewer something the code cannot.
Keep a comment when it states:

- **Why** — the constraint, trade-off, or rejected alternative behind the code.
  Link the ADR rather than repeating it.
- **An invariant or contract** — ordering, locking, crash behaviour, what a
  caller may assume, what must stay true when the code changes.
- **Non-obvious external behaviour** — a Kubernetes, client-go, filesystem, or
  OS quirk the code depends on.
- **The contract of an exported identifier** — a doc comment, starting with
  its name.

Delete a comment when it:

- Restates what the next line does.
- Narrates history — "previously", "now", "fixed", dates, stage numbers, audit
  finding IDs. That belongs in the commit message and `CHANGELOG.md`.
- Is commented-out code.
- Is a `TODO` with no issue link. Open an issue instead.

A comment that is wrong is a bug: fix it in the same change as the code. Write
comments in English, in full sentences, once, at the place the reader needs
them.

## Pull requests

- Keep each pull request scoped to one coherent change.
- Add or update tests for behavior changes.
- Update documentation and ADRs when a public contract or architectural
  decision changes.
- Do not add Kubernetes permissions without documenting why they are required.
- Do not commit generated binaries, cluster credentials, or captured snapshot
  data.

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).
