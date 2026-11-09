// Package cli implements the ktm command-line interface.
package cli

import (
	"context"
	"fmt"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// reconstruct rebuilds the Snapshot at id by walking PrevID back to a full
// snapshot and applying the deltas forward. The cost is bounded by the
// agent's full-snapshot cadence, not by the snapshot's age.
func reconstruct(ctx context.Context, store storage.Store, id types.SnapshotID) (delta.Snapshot, error) {
	if id == "" {
		return nil, fmt.Errorf("reconstruct: empty snapshot id")
	}

	// chain[0] is id; chain[len-1] is the anchoring full snapshot.
	var chain []storage.Loaded
	visited := make(map[types.SnapshotID]bool)

	cur := id
	for {
		if visited[cur] {
			return nil, fmt.Errorf("reconstruct: cycle detected in delta chain at snapshot %s", cur)
		}
		visited[cur] = true

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

	out := chain[len(chain)-1].Full

	for i := len(chain) - 2; i >= 0; i-- {
		out = delta.Apply(out, chain[i].Delta)
	}
	return out, nil
}
