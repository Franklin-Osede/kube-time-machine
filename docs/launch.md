# Launch draft

> **Status: PENDING. Do not post.** The text below is prepared, not
> published. It waits for the stable `v0.1.1` release and a real demo video
> link; every `[...]` placeholder must be replaced first.

Do not publish any post below until all of these are true:

- `v0.1.1-rc.3` and then the stable `v0.1.1` release workflows have completed,
  and their image, OCI chart, binaries and checksums have been verified with the
  `release-gate` skill.
- A clean install from the published stable chart has reached Ready.
- The README quick start has been run as written against the stable version,
  and `VERSION` in the README and `docs/install.md` points at it.
- The demo video is uploaded and its link replaces `[demo URL]` below.

## Positioning

KTM records sampled declarative state of Deployments and ConfigMaps for
incident response. It is not a backup tool, a GitOps source of truth, an
observability stack, or an automatic rollback system.

Lead with the concrete workflow: a rollout is stuck, find what changed and the
last known-good image, roll that one resource back, and confirm the rollout with
`kubectl`. Mention the learning-in-public story near the end, after the
technical value is clear.

Recommended channel order:

1. LinkedIn after the v0.1.1 release is verified.
2. Show HN a few days later, after early feedback is incorporated.
3. Reddit `r/kubernetes` with a technical, non-promotional framing.

## Demo

Record the README quick start against the stable release, from a disposable
kind cluster. Narration, in order:

1. "A rollout is stuck. What changed?" `kubectl get pods` shows the new Pod in
   `ErrImagePull` while the old Pod keeps serving. Do not call it an outage.
2. `ktm blame` gives the resource timeline: when the change appeared in a
   snapshot and which field managers were involved. It does not prove who made
   the change.
3. `ktm diff` shows the image line that changed between the last known-good
   snapshot and the current one.
4. `ktm rollback` names the target cluster, previews the exact object it will
   write, and updates with the `resourceVersion` it read.
5. `kubectl rollout status` confirms the rollout completes. KTM does not
   record health; `kubectl` verifies it.

## LinkedIn short draft

I built kube-time-machine to investigate Deployment and ConfigMap changes
during Kubernetes incidents.

It records sampled declarative state, shows diffs and a per-resource timeline,
and rolls one resource back with a preview and an optimistic-concurrency check.

v0.1.1 keeps the scope small: an agent, a CLI and local storage. Querying
in-cluster history currently means copying the volume to your machine.

The most useful engineering lessons were defining filesystem commit points,
keeping history reconstructable under retention, and treating the rollback
preview as a contract: what you confirm is what gets written.

Demo: [demo URL]

Repository: https://github.com/Franklin-Osede/kube-time-machine

Technical feedback is welcome.

`#Kubernetes #SRE #PlatformEngineering #OpenSource #Golang`

## LinkedIn long draft

When a rollout gets stuck, the first question is "what changed?" If the change
came through Git, the history is there. If someone ran `kubectl edit`, or an
operator rewrote a field, there may be no record of the earlier value.

kube-time-machine (KTM) keeps one. An agent watches Deployments and ConfigMaps
through the API server and writes their declarative state to local storage at a
fixed interval, as full snapshots plus deltas. A CLI then lets you:

- `ktm diff`: compare the state at two snapshots.
- `ktm blame`: see one resource's timeline and the field managers involved.
- `ktm rollback`: put one resource back to an earlier snapshot, after a preview
  of the exact object it will write, with the API server rejecting the update
  if the resource changed since the preview.

What it does not do matters as much. The history is sampled: a change reverted
within one interval does not appear, and timestamps are flush times. It records
no `.status`, so it does not know whether a rollout was healthy; Prometheus and
kube-state-metrics answer that. It is not a backup tool like Velero, and it does
not replace Argo CD or Flux, which detect drift from Git.

The decisions I learned most from are written up as ADRs in the repository:
atomic writes with `meta.json` as the commit point, a versioned on-disk format,
and a rollback that preserves annotations it never recorded instead of deleting
them.

I am a backend engineer moving into platform engineering and learning Go in
public. I would value critique from people who have run this kind of tool in
production.

Demo: [demo URL]

Repository: https://github.com/Franklin-Osede/kube-time-machine

`#Kubernetes #SRE #PlatformEngineering #OpenSource #DevOps #Golang`

## Required assets

- Screenshot of `ktm diff` showing the image-change hunk.
- Recording of `ktm rollback` followed by `kubectl rollout status` completing.
- The demo video link, replacing every `[demo URL]`.

## Prepared responses

### Why not use ArgoCD history?

Argo CD detects drift on the resources it manages: a `kubectl edit` marks the
Application OutOfSync and shows the live-versus-desired diff. It does not keep
the sequence of live states, and it does not track resources outside an
Application. KTM samples the live state of every watched resource, whoever
changed it. The tools are complementary; see [comparison.md](comparison.md).

### Is this not Velero?

Velero backs up and restores clusters, namespaces or selected resources,
including volumes, usually to object storage. KTM keeps a sampled history of
declarative state for diffing, a per-resource timeline, and putting one
resource back. It is not a backup: its storage is a single local volume.

### Why does KTM not capture `.status`?

That is a product decision documented in
[ADR-0005](adr/0005-declarative-state-recorder.md): KTM records declarative
intent, not observed state. Controller-owned status is noise for the question
"what did we change?" Use Prometheus or kube-state-metrics for rollout health
and runtime signals.

## Claims to avoid

- That KTM records every change, or records it as it happens. It samples at
  the flush interval.
- That `blame` shows who made a change. It shows field managers involved.
- That KTM knows when a resource "last looked healthy". It records no status.
- That the demo's bad image takes the service down. The old Pod keeps serving.
- Timing claims such as "in seconds" or "half an hour saved" that were not
  measured.

- Web UI, multi-cluster support, cloud storage, or Slack alerts.
- Automatic rollback or unqualified production-safety claims.
- Support for resources beyond Deployments and ConfigMaps.
- Querying Mode B without extracting the PVC.
- Disaster-recovery or backup positioning.

## Success signals

The primary signals are GitHub stars, substantive issues or Discussions,
forks, GHCR downloads, and reports from people describing their own
incident-response workflow. LinkedIn likes and impressions are secondary unless
they cross the documented Phase 2 gate.
