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
