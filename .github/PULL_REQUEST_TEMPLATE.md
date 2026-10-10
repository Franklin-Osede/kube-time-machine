## What this changes

<!-- One or two sentences. What behaviour is different after this PR? -->

## Why

<!-- The problem being solved. Link an issue if there is one. -->

## How it was verified

<!-- Delete what does not apply. -->

- [ ] `go test -race ./...`
- [ ] `helm lint deploy/helm` and `helm template ktm deploy/helm -n ktm-system`
- [ ] Exercised against a real cluster (say which: kind, k3s, cloud)

## Checklist

- [ ] `CHANGELOG.md` updated under `[Unreleased]`, if this is user-visible
- [ ] Docs updated, if this changes install steps, flags, or chart values
- [ ] ADR added or updated, if this changes the storage format or a recorded decision
- [ ] No new RBAC permissions — or the new ones are justified below
