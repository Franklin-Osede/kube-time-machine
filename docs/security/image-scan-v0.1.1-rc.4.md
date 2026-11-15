# Image scan: ktm-agent 0.1.1-rc.4

Status: **scan complete; the same four Debian findings remain under the
existing owner acceptance for v0.1.1**, for the supported configuration and
review conditions below. Franklin Osede's
[2026-11-09 sign-off](https://github.com/Franklin-Osede/kube-time-machine/pull/24#issuecomment-6738416187)
accepted these four findings for v0.1.1, not just rc.3. This rescan found no
new advisories, changed severity or package versions; the upstream distroless
index is unchanged. This record applies that existing acceptance, rather than
creating a new one. The stable image must still be scanned again before the
public announcement. Previous record:
[rc.3](image-scan-v0.1.1-rc.3.md).

## What was scanned

| | |
|---|---|
| Image | `ghcr.io/franklin-osede/ktm-agent:0.1.1-rc.4` |
| Index digest | `sha256:a72b1c4295ab126d747f6bfd0f429c58adaa4f3e21c13aca390f70a4e3590b91` |
| linux/amd64 manifest | `sha256:54a6d916ae45a8f4eab674f3ee55eb035483b22f6cb571dc3e98d6f78860f29e` |
| linux/arm64 manifest | `sha256:ec30d8594fd4032d830a3177491b1b9d9ad1fde8174322706dcf8dbf6fa4d9c6` |
| Built from | commit `861765301c3069dda90fd2fad4e0c232d936d432`, release run 43753959279 |
| Base | `gcr.io/distroless/static-debian12:nonroot` (Debian 12.15) |
| Scanner | Trivy 0.73.0, vulnerability DB v2 updated 2026-11-15 18:56 UTC |
| Scanned | 2026-11-15, both architectures |

Reproduce (results change as the DB is updated; compare against the DB date
above):

```bash
trivy --version
for p in linux/amd64 linux/arm64; do
  trivy image --platform "$p" \
    ghcr.io/franklin-osede/ktm-agent@sha256:a72b1c4295ab126d747f6bfd0f429c58adaa4f3e21c13aca390f70a4e3590b91
done
```

The image contains five Debian packages (`base-files`, `ca-certificates`,
`media-types`, `netbase`, `tzdata`) and the `ktm-agent` Go binary.

## Findings

The Go binary has no findings, and `govulncheck -mode=binary` on the
published binaries agrees. Both architectures show the same four Debian LTS
advisories. Trivy reports them as `UNKNOWN` because a DLA carries no CVE and
no severity. The descriptions below come from the advisories themselves.

| Advisory | Package | Installed | Fixed in | What the update does |
|---|---|---|---|---|
| [DLA-4845-1](https://www.debian.org/lts/security/2026/dla-4845-1) (2026-10-20) | `ca-certificates` | `20250419~deb12u1` | `20250419~deb12u2` | Removes the "emSign ECC Root CA - G3" and "Comodo AAA Services root" CAs after compromised eMudhra intermediates (Debian bug 1151413) |
| [DLA-4792-1](https://www.debian.org/lts/security/2026/dla-4792-1) (2026-09-23) | `tzdata` | `2026b-0+deb12u1` | `2026c-0+deb12u1` | tzdata 2026c: Alberta and Morocco rule changes, leap-second list |
| [DLA-4854-1](https://www.debian.org/lts/security/2026/dla-4854-1) (2026-10-23) | `tzdata` | `2026b-0+deb12u1` | `2026e-0+deb12u1` | tzdata 2026e: Northwest Territories and Manitoba rule changes |
| [DLA-4867-1](https://www.debian.org/lts/security/2026/dla-4867-1) (2026-10-29) | `tzdata` | `2026b-0+deb12u1` | `2026f-0+deb12u1` | tzdata 2026f: Cuba rule change |

All four are fixed in Debian. They remain because the upstream distroless
image has not been rebuilt with the fixes: the current
`gcr.io/distroless/static-debian12:nonroot`
(`sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`,
checked 2026-11-15) shows the same four. Rebuilding KTM today would not remove
them.

## Assessment

**DLA-4845-1, `ca-certificates`.** The stale bundle still trusts two roots
Debian has removed. Go reads that bundle only when a TLS client has no CA of
its own. The agent makes one kind of outbound TLS connection, to the
Kubernetes API server. In the cluster, `rest.InClusterConfig`
(`internal/kubeclient/kubeclient.go`) pins the service account's `ca.crt`,
which the kubelet always mounts, so the system bundle is not consulted. The health server is plain HTTP and makes
no outbound calls. The bundle would be used only if the agent ran outside
the chart with a kubeconfig (`--kubeconfig`, `$KUBECONFIG` or
`~/.kube/config`) that has no certificate-authority data. A kubeconfig exec
credential plugin could also open connections of its own. The chart sets
none of these.

**DLA-4792-1, DLA-4854-1, DLA-4867-1, `tzdata`.** These are timezone rule
updates, not code fixes. Persisted timestamps and the retention cutoff use
UTC (`internal/storage/local.go`, `internal/agent/snapshot.go`), nothing
calls `time.LoadLocation`, and the chart sets no `TZ`. Only log-line
timestamps use local time, so stale rules could only shift those, and only
if an operator set `TZ` to one of the affected zones.

**Decision:** the existing owner acceptance for v0.1.1 applies (see Status). Neither package is on a path the
agent uses in its supported configuration, and no fixed base exists to move
to.

Mitigations in place, as the chart ships by default: distroless base with no
shell or package manager; non-root UID 65532 with a read-only root
filesystem; a ClusterRole limited to `list` and `watch`; a NetworkPolicy
that blocks the cloud metadata endpoint.

## Review when

- a distroless `static-debian12` build ships the fixed versions: rebuild and
  rescan, and the findings should clear;
- any of these advisories gains a CVE or a severity;
- KTM adds an outbound TLS client other than the API server, or uses local
  time for anything other than log lines;
- at every release, which rescans the published image.

## Release gate evidence

Tested commit: `861765301c3069dda90fd2fad4e0c232d936d432`, the merge of
[PR #30](https://github.com/Franklin-Osede/kube-time-machine/pull/30), with a
clean working tree before the tag and before these documentation updates.
Tag: `v0.1.1-rc.4`. Local Go: `go1.26.11 darwin/arm64`; local Helm:
`v4.2.3+g43e8b7f`. The
[release workflow](https://github.com/Franklin-Osede/kube-time-machine/actions/runs/43753959279)
passed with Go 1.26.11 and Helm 3.14.0, so Helm 3 rendering is verified by CI
rather than inferred from the local Helm 4 result.

| Step | Result |
|---|---|
| `gofmt -l .` | PASS: no output |
| `go vet ./...` | PASS |
| `go test -race -count=1 -timeout 5m ./...` | PASS: all packages |
| `make build` | PASS |
| `go mod tidy` and module diff check | PASS: no changes |
| `golangci-lint run --timeout=5m` | PASS: 0 issues |
| `govulncheck ./...` with Go 1.26.11 | PASS: no vulnerabilities |
| `helm lint` and `helm template` | PASS locally and in the release workflow |
| `make e2e` | PASS on dedicated kind cluster `ktm-e2e`, Kubernetes v1.36.1, Colima; cluster removed |
| Release workflow | PASS: verify, both image architectures, chart, all five binary platforms, GitHub Release |
| Image index | PASS: linux/amd64 and linux/arm64; OCI revision labels match the tagged commit |
| BuildKit provenance | PASS: one `attestation-manifest` per architecture, each referencing its image manifest |
| Published chart | PASS: `version` and `appVersion` are `0.1.1-rc.4` |
| Release assets | PASS: exactly ten binaries plus `checksums.txt`, no build-record asset |
| Downloaded binary checksums | PASS: all ten match `checksums.txt` |
| Downloaded binary toolchain | PASS: `go version -m` reports Go 1.26.11 for all ten |
| `govulncheck -mode=binary` | PASS: all ten downloaded binaries have no findings |
| Native binary versions | PASS: CLI prints `ktm version 0.1.1-rc.4`; agent prints `0.1.1-rc.4` |
| Published image scan | PASS under existing owner acceptance: only the four Debian findings above, identical on both architectures; no Go findings |
| Fresh published-chart install | PASS: default chart reaches Ready, PVC Bound, published rc.4 image running on `ktm-rc4-verify` |
| Published-binary quick start | PASS: local agent records, CLI lists/blames/diffs, previews and rolls the broken image back to `nginx:1.27`; executed with zsh on `ktm-rc4-verify` |
| Prerelease `:latest` behavior | PASS: `manifest unknown` before and after publication |
| Withdrawn `0.1.0` | PASS: `manifest unknown` before and after publication |
| Cleanup | PASS: both dedicated test clusters removed |

The local E2E used the script's existing fallback without the BuildKit cache
mount because buildx is unavailable locally. The release image itself was
built with BuildKit in CI. The quick-start test used the downloaded macOS
arm64 binaries, an isolated kubeconfig, a private temporary store and a fresh
kind cluster; it did not use `./bin`.

The release workflow strips the tag's `v` before stamping binaries. Thus the
literal native version output above is expected; it identifies release
`v0.1.1-rc.4` without printing the tag prefix.

Published chart digest:
`sha256:d7697009cc522526c1ca2683da24ca9436f60decac9c2546c69faf0b9cc553fd`.
