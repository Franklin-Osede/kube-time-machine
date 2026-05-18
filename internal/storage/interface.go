// Package storage persists and retrieves snapshots and deltas produced
// by the ktm-agent.
//
// The Store interface is backend-agnostic. The MVP ships a local
// filesystem implementation in local.go; cloud backends (S3, GCS, Azure
// Blob) are Phase 2.
package storage

import (
	"context"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// Loaded is the result of a Get call. Exactly one of Full or Delta is
// meaningful, determined by Meta.Kind. Callers should switch on the kind
// rather than testing field zero-values, because an empty snapshot or
// delta is a perfectly valid payload.
type Loaded struct {
	Meta  types.SnapshotMeta
	Full  delta.Snapshot
	Delta delta.Delta
}

// Store is the persistence contract for snapshots.
//
// Implementations must guarantee that a successful Put is durable enough
// for the next Get/List to observe it. They are not required to provide
// cross-snapshot transactional semantics — each snapshot is independent.
type Store interface {
	// PutFull persists a full reference snapshot taken at ts. The backend
	// assigns the SnapshotID and returns the resulting metadata.
	PutFull(ctx context.Context, ts time.Time, snap delta.Snapshot) (types.SnapshotMeta, error)

	// PutDelta persists an incremental delta from prevID, taken at ts.
	// prevID must reference an existing snapshot; backends are not
	// required to verify this for performance reasons.
	PutDelta(ctx context.Context, ts time.Time, prevID types.SnapshotID, d delta.Delta) (types.SnapshotMeta, error)

	// Get loads the payload at id. The returned Loaded has Full populated
	// iff Meta.Kind == KindFull, and Delta populated otherwise.
	Get(ctx context.Context, id types.SnapshotID) (Loaded, error)

	// List returns all known snapshots, ordered by Timestamp ascending.
	// The returned slice is owned by the caller and safe to mutate.
	List(ctx context.Context) ([]types.SnapshotMeta, error)
}
