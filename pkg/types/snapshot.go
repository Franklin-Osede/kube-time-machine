// Package types defines the public, serializable metadata used to describe
// snapshots persisted by kube-time-machine.
//
// This package intentionally has no dependency on internal/. Consumers
// outside the project (future CLI plugins, third-party tooling, the
// Phase-2 web visor) can import it to talk *about* snapshots without
// needing access to payload internals.
package types

import "time"

// SnapshotID is an opaque, sortable identifier for a snapshot. The local
// filesystem backend derives it from the snapshot's UTC timestamp at
// millisecond resolution (e.g. "20260518T140530123Z"). Other backends may
// choose other formats; consumers MUST treat the value as opaque.
type SnapshotID string

// SnapshotKind discriminates between a full reference snapshot and an
// incremental delta. A storage backend's Get must consult this field to
// decide which payload type to return.
type SnapshotKind string

const (
	KindFull  SnapshotKind = "full"
	KindDelta SnapshotKind = "delta"
)

// SnapshotMeta is the metadata for a single persisted snapshot. It is
// JSON-serializable and forms both the per-snapshot meta.json file and
// the entries inside the top-level index.json.
//
// PrevID is empty for full snapshots and identifies the immediate
// predecessor for deltas. Reconstruction of any historical state walks
// PrevID backwards until reaching a full snapshot, then applies the
// deltas forward.
//
// Kinds is a sorted list of the Kubernetes resource kinds (e.g.
// "Deployment", "ConfigMap") that appear in this snapshot's payload.
// For full snapshots it covers every kind in the full state; for deltas
// it covers only the kinds that changed. An empty slice means the
// payload has no entries (or the snapshot predates this field — treat
// as unknown). CLI tools and future indexing layers can use Kinds to
// skip loading snapshots that cannot possibly contain the target kind.
type SnapshotMeta struct {
	ID        SnapshotID   `json:"id"`
	Kind      SnapshotKind `json:"kind"`
	Timestamp time.Time    `json:"ts"`
	PrevID    SnapshotID   `json:"prevId,omitempty"`
	Kinds     []string     `json:"kinds,omitempty"`
}
