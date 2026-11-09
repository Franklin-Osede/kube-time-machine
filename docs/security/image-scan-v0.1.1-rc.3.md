# Image scan: ktm-agent 0.1.1-rc.3

Status: **scan done; acceptance proposed, pending owner sign-off.** Nothing
below is accepted until the release owner approves it in review.

## What was scanned

| | |
|---|---|
| Image | `ghcr.io/franklin-osede/ktm-agent:0.1.1-rc.3` |
| Index digest | `sha256:35ec21758c73d1576ef6ca8faa8f9bb5d20aa2dd8d50053647972a5ad92b77cd` |
| linux/amd64 manifest | `sha256:a4b656d058b1c81a2e3714209ef61d55a0ec00eef52a49bb8a077864c1d76fa2` |
| linux/arm64 manifest | `sha256:b6815a791f60978431289272065a5e37129cd81dba3e21cb2b75f1368052f8b5` |
| Built from | commit `118457c0bbda416c02d3eac6addbd4790643c36d`, release run 42598456017 |
| Base | `gcr.io/distroless/static-debian12:nonroot` (Debian 12.15) |
| Scanner | Trivy 0.73.0, vulnerability DB v2 updated 2026-11-09 06:57 UTC |
| Scanned | 2026-11-09, both architectures |

Reproduce (results change as the DB is updated; compare against the DB date
above):

```bash
trivy --version
for p in linux/amd64 linux/arm64; do
  trivy image --platform "$p" \
    ghcr.io/franklin-osede/ktm-agent@sha256:35ec21758c73d1576ef6ca8faa8f9bb5d20aa2dd8d50053647972a5ad92b77cd
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
checked 2026-11-09) shows the same four. Rebuilding KTM today would not remove
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

**Proposed decision:** accept for v0.1.1. Neither package is on a path the
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
