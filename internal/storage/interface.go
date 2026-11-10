// Package storage persists and retrieves snapshots and deltas produced
// by the ktm-agent. Local, a filesystem store, is the only implementation.
package storage

import (
	"context"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// Loaded is the result of a Get call. Exactly one of Full or Delta is
// meaningful; switch on Meta.Kind, because an empty payload is valid.
type Loaded struct {
	Meta  types.SnapshotMeta
	Full  delta.Snapshot
	Delta delta.Delta
}

// Store is the persistence contract for snapshots. A successful Put must be
// visible to the next Get or List; each snapshot is independent, with no
// cross-snapshot transactions.
type Store interface {
	// PutFull persists a full reference snapshot taken at ts. The backend
	// assigns the SnapshotID and returns the resulting metadata.
	PutFull(ctx context.Context, ts time.Time, snap delta.Snapshot) (types.SnapshotMeta, error)

	// PutDelta persists an incremental delta from prevID, taken at ts.
	// prevID must reference an existing snapshot; backends need not verify it.
	PutDelta(ctx context.Context, ts time.Time, prevID types.SnapshotID, d delta.Delta) (types.SnapshotMeta, error)

	// Get loads the payload at id. The returned Loaded has Full populated
	// iff Meta.Kind == KindFull, and Delta populated otherwise.
	Get(ctx context.Context, id types.SnapshotID) (Loaded, error)

	// List returns all known snapshots, ordered by Timestamp ascending.
	// The returned slice is owned by the caller and safe to mutate.
	List(ctx context.Context) ([]types.SnapshotMeta, error)

	// Delete removes the snapshot with the given id, payload and index entry.
	// Callers must ensure no remaining delta has PrevID == id; backends need
	// not enforce it. Returns nil if id does not exist.
	Delete(ctx context.Context, id types.SnapshotID) error
}
