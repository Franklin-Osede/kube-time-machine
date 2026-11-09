// Package types defines the public, serializable metadata used to describe
// snapshots persisted by kube-time-machine. It must not import internal/.
package types

import "time"

// SnapshotID is an opaque, sortable identifier for a snapshot, such as
// "20260518T140530123Z". Consumers must treat the value as opaque.
type SnapshotID string

// ManagersAnnotation is the synthetic annotation the agent writes into every
// recorded object: the sorted, comma-separated field managers from its
// stripped managedFields. It is part of the on-disk format; renaming it hides
// the managers of every snapshot already written.
const ManagersAnnotation = "ktm.io/managers"

// AnnotationPrefix marks annotations KTM synthesises into recorded state.
// They describe the recording, not the resource, so rollback never writes
// them back to the cluster.
const AnnotationPrefix = "ktm.io/"

// SnapshotKind discriminates between a full reference snapshot and an
// incremental delta.
type SnapshotKind string

const (
	KindFull  SnapshotKind = "full"
	KindDelta SnapshotKind = "delta"
)

// SnapshotMeta is the metadata for a single persisted snapshot: the content
// of meta.json and of each index.json entry.
//
// PrevID is empty for full snapshots and names the immediate predecessor for
// deltas.
//
// Kinds lists, sorted, the resource kinds in the payload: every kind for a
// full snapshot, the changed kinds for a delta. Empty means no entries or
// unknown. `ktm blame` uses it to skip deltas that cannot contain the target.
type SnapshotMeta struct {
	ID        SnapshotID   `json:"id"`
	Kind      SnapshotKind `json:"kind"`
	Timestamp time.Time    `json:"ts"`
	PrevID    SnapshotID   `json:"prevId,omitempty"`
	Kinds     []string     `json:"kinds,omitempty"`
}
