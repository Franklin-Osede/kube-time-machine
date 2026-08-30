# Launch draft

This document is a working copy for the v0.1.1 public launch. Do not publish any
post below until all of these are true:

- The `v0.1.1-rc.1` release workflow has completed successfully.
- The agent image, OCI Helm chart, and CLI binaries have been verified.
- A real install from the published chart has completed successfully.
- The `v0.1.1` final release is green.
- The demo video is uploaded and its link is available.
- README and install instructions point to artifacts that actually exist.

## Positioning

KTM is a declarative-state recorder for incident response. It is not a backup
tool, a GitOps source of truth, an observability stack, or an automatic rollback
system.

Lead with the product problem and the 3 AM incident-response workflow. Mention
the learning-in-public story near the end, after the technical value is clear.

Recommended channel order:

1. LinkedIn after the v0.1.1 release is verified.
2. Show HN a few days later, after early feedback is incorporated.
3. Reddit `r/kubernetes` with a technical, non-promotional framing.

## LinkedIn short draft

It's 3 AM. Your API is down. The first question is always: "what changed?"

Today that means stitching together ArgoCD history, CI logs, and cluster events
that expired an hour ago. And if someone ran `kubectl edit` by hand? Invisible.
No trail.

So I built kube-time-machine: git blame and time-travel for your Kubernetes
cluster.

It records the declarative state of your Deployments and ConfigMaps over time,
no matter who changed them: GitOps, `kubectl`, or an operator. Then you can diff,
blame, and roll back a single resource in seconds.

It's not a backup tool; that's Velero. It's not GitOps history; that's ArgoCD.
It fills the gap between them.

v0.1.1 is out: agent and CLI, local-first, MIT licensed, built in Go, and
developed in public.

5-minute demo and repo in the comments. Direct technical feedback is welcome.

`#Kubernetes #SRE #PlatformEngineering #OpenSource #DevOps`

## LinkedIn long draft

3 AM. The API is down. "What changed?"

Every SRE knows this moment. Answering it means stitching together ArgoCD
history, if you use it, commits across repositories, CI logs, and cluster events
that expire after about an hour. Thirty minutes can pass before you even have a
hypothesis. If someone changed something with `kubectl edit`, that change is
invisible in centralized GitOps history. There is no git blame for your cluster.

So I built one.

kube-time-machine (KTM) records the declarative state that lived in your
Kubernetes API server over time: Deployment specs and ConfigMap data, regardless
of whether the change came from GitOps, `kubectl`, or an operator. Then it lets
you:

- `ktm diff`: see what changed between any two points in time.
- `ktm blame`: inspect the full timeline of one resource.
- `ktm rollback`: undo a single resource, with a preview and native optimistic
  locking.

What it is not matters. KTM is not a backup tool; Velero owns disaster recovery.
It is not GitOps history; ArgoCD and Flux own that. It is not observability;
Prometheus and kube-state-metrics own runtime status. KTM deliberately records
only the declarative surface, stripping `.status`, and fills the gap those tools
leave open: a queryable, per-resource, ground-truth change log.

The demo is simple: install the agent, break a Deployment with a bad image tag,
use `ktm blame` to find when it last looked healthy, use `ktm diff` to show the
one line that changed, then use `ktm rollback` and watch the Pods recover.

v0.1.1 ships an agent (`ktm-agent`) and a CLI (`ktm`). It is local-first, MIT
licensed, and built in Go.

Full disclosure: I am a backend engineer transitioning into Platform
Engineering and learning Go in public. The architecture decisions, including
incremental deltas, declarative-state framing, and optimistic-locking rollback,
are documented as ADRs in the repository. I would value critique from people
who have run this kind of system at scale.

Demo: `[video link]`

Repository: `github.com/Franklin-Osede/kube-time-machine`

What would you want before trusting this during a real incident? Tell me where
it breaks.

`#Kubernetes #SRE #PlatformEngineering #OpenSource #DevOps #Golang`

## Required assets

- Screenshot of `ktm diff` showing one clean image-change hunk without status
  noise.
- GIF of `ktm rollback` followed by `kubectl rollout status` recovering.
- Link to the five-minute demo video.
- Optional green release badge in the README, only after the final release is
  verified.

## Prepared responses

### Why not use ArgoCD history?

ArgoCD only sees what ArgoCD manages, and its source of truth is Git. If someone
uses `kubectl edit` or an operator mutates the resource, that change is invisible
to GitOps history. KTM observes the API server, so it captures the change
regardless of who made it. The tools are complementary; see
[comparison.md](comparison.md).

### Is this not Velero?

Velero handles disaster recovery for clusters and persistent volumes, usually
with object storage. KTM handles per-resource forensics and selective rollback.
It does not restore your cluster; it shows which declarative line changed and
can help undo that specific change.

### Why does KTM not capture `.status`?

That is a product decision documented in
[ADR-0005](adr/0005-declarative-state-recorder.md): KTM records declarative
intent, not observed state. Controller-owned status is noise for the question
"what did we change?" Use Prometheus or kube-state-metrics for rollout health
and runtime signals.

## Claims to avoid

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
