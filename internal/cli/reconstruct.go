// Package cli implements the ktm command-line interface. It depends on
// internal/storage for persisted snapshots and internal/delta for the
// in-memory data model; everything user-facing lives here.
package cli

import (
	"context"
	"fmt"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// reconstruct rebuilds the full Snapshot at the point identified by id.
//
// Algorithm:
//  1. Walk backwards via PrevID until we hit a full snapshot.
//  2. Apply the deltas forward from that full to reach id.
//
// Because the agent guarantees a full snapshot every `fullEvery` flushes,
// step 1 reads at most `fullEvery` records, and step 2 applies the same
// number of deltas. Cost is bounded by the cadence configuration, not by
// the age of the snapshot.
func reconstruct(ctx context.Context, store storage.Store, id types.SnapshotID) (delta.Snapshot, error) {
	if id == "" {
		return nil, fmt.Errorf("reconstruct: empty snapshot id")
	}

	// chain holds Loaded records ordered from `id` back to the anchoring
	// full snapshot. chain[0] is `id`; chain[len-1] is the full.
	var chain []storage.Loaded

	cur := id
	for {
		loaded, err := store.Get(ctx, cur)
		if err != nil {
			return nil, fmt.Errorf("reconstruct: load %s: %w", cur, err)
		}
		chain = append(chain, loaded)

		if loaded.Meta.Kind == types.KindFull {
			break
		}
		if loaded.Meta.PrevID == "" {
			return nil, fmt.Errorf("reconstruct: delta %s has no PrevID but is not a full snapshot", cur)
		}
		cur = loaded.Meta.PrevID
	}

	// Start from the full snapshot at the tail of the chain.
	out := chain[len(chain)-1].Full

	// Apply deltas forward (from oldest after the full to `id`).
	for i := len(chain) - 2; i >= 0; i-- {
		out = delta.Apply(out, chain[i].Delta)
	}
	return out, nil
}
