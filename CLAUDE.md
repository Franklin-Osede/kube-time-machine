# kube-time-machine

In-cluster agent (`cmd/agent`) records Deployment and ConfigMap state to a local
PVC as full snapshots plus deltas; the CLI (`cmd/ktm`) lists, diffs,
reconstructs, blames and rolls back from that store. Architecture:
`docs/architecture.md`. Decisions: `docs/adr/`. Open work:
`TODO.md`.

## Scope-lock (pre-launch)

- Typed support — blame, rollback — is Deployments and ConfigMaps only.
  `--watch-resources` records other GVRs through `DynamicInformers`; that is
  the Phase 2 extension point, not an invitation to widen scope now.
- Local PVC storage only. No web UI. No multi-cluster.
- Anything else goes to `TODO.md` and waits for the Phase 2 gate in
  `docs/roadmap.md`.

## Invariants — a change that breaks one is a defect, whatever the tests say

1. **Delta round-trip.** `Apply(a, Compute(a, b)) == b` for every pair.
   `internal/delta` stays Kubernetes-agnostic and is guarded by
   `TestRoundTrip` and `FuzzRoundTrip`.
2. **Write ordering.** `writeSnapshot` writes the payload before `meta.json`;
   `meta.json` is the commit point `rebuildIndex` trusts. Every file goes
   through `atomicWriteJSON` (temp file, fsync, rename, fsync parent dir).
3. **Crash-safe delete.** Delete renames into a tombstone first; the index
   write is the commit point. Tombstones are evidence — only the writer may
   clear them.
4. **One writer.** Only the agent, holding `AcquireWriterLock`, mutates a
   store. The CLI uses `OpenForRead`, which repairs in memory and returns
   `ErrReadOnly` on any write.
5. **Never persist a bogus snapshot.** The final flush is skipped unless the
   informer caches synced; an empty cache must not become a full snapshot.
6. **Rollback locking (ADR-0006).** Update with the `ResourceVersion` read at
   preview time — no second `Get` after the prompt. `404` falls back to
   `Create` after `stripServerOwned`. `--yes` skips the prompt, never the
   disclosure.
7. **Retention keeps an anchor.** GC preserves the newest full snapshot before
   the cutoff so every delta in the window stays reconstructable.
8. **Snapshots are sanitised.** `internal/agent/marshal.go` strips
   `resourceVersion`, `managedFields` and `.status` (ADR-0002, ADR-0005).

## Gate — what CI runs (`.github/workflows/ci.yml`)

```bash
gofmt -l .                       # must print nothing
go vet ./...
go test -race -count=1 -timeout 5m ./...
make build
go mod tidy && git diff --exit-code -- go.mod go.sum
golangci-lint run --timeout=5m   # pinned v2.12.2
govulncheck ./...
helm lint deploy/helm
helm template ktm deploy/helm --namespace ktm-system >/dev/null
make e2e                         # kind; not in ci.yml, see e2e.yml
```

The full procedure, including post-tag checks, is the `release-gate` skill.

## Conventions

- Comments follow the policy in `CONTRIBUTING.md` § Comments: keep why,
  invariants and external quirks; delete restatement and history. An
  inaccurate comment is a defect: it is what lets bugs survive review.
- Docs claim only what shipped. Verify against the code before writing a
  number, a size, or a "we do X".
- Focused commits; messages say why, not what.
- Protocol or format decisions get an ADR in `docs/adr/`.

## Release

Tags `v*` trigger `release.yml`. A tag containing a hyphen (`v0.1.1-rc.1`) is a
prerelease and does not move `:latest`. `0.1.0` is withdrawn and its GHCR
versions are deleted, so `:latest` resolves to nothing until a stable tag.
