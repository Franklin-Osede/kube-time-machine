package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// blameOp captures what kind of change happened to a target resource at
// a given snapshot. The set is deliberately small for MVP — the human
// reading the timeline only needs to know "did it appear, change, or go
// away?".
type blameOp string

const (
	opCreated  blameOp = "CREATED"
	opModified blameOp = "MODIFIED"
	opRemoved  blameOp = "REMOVED"
)

// blameEntry is one row of the blame timeline.
type blameEntry struct {
	Time       time.Time
	Op         blameOp
	SnapshotID types.SnapshotID
}

func newBlameCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "blame <kind>/<namespace>/<name>",
		Short: "Show the timeline of changes for one resource",
		Long: "blame walks the snapshot history forward and reports every point in time at\n" +
			"which the target resource was created, modified, or removed. Unlike a naive\n" +
			"scan of delta `removed` entries, this algorithm reconstructs the full snapshot\n" +
			"state at each step and compares against the previous step — which is the only\n" +
			"way to detect deletes that happen to land on a full-snapshot tick.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := parseKeyFilter(args[0])
			if err != nil {
				return err
			}
			return runBlame(cmd.OutOrStdout(), opts.StorageDir, target)
		},
	}
}

func runBlame(out io.Writer, storageDir string, target delta.Key) error {
	store, err := storage.NewLocal(storageDir)
	if err != nil {
		return errf("open storage at %s: %w", storageDir, err)
	}

	entries, err := computeBlame(context.Background(), store, target)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Fprintf(out, "no history found for %s in %s\n", keyString(target), storageDir)
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tOP\tSNAPSHOT")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.Time.Format(time.RFC3339), e.Op, e.SnapshotID)
	}
	return w.Flush()
}

// computeBlame walks the snapshot history in chronological order,
// maintaining a running reconstruction of the cluster state, and emits
// an entry every time the target resource appears, changes, or
// disappears.
//
// Why a running reconstruction instead of N independent reconstruct()
// calls: each delta apply is O(changes-in-this-delta), so the whole pass
// is O(N + total-changes). N independent reconstructs would be
// O(N * fullEvery) reads. For a year of 5-minute snapshots the
// difference is roughly two orders of magnitude.
//
// Why comparing snapshot states (not just delta entries): per the
// 2026-05-20 smoke-test finding, a delete that coincides with a
// full-snapshot tick is represented by absence in the next full, not by
// a `removed` entry. Scanning deltas alone would miss those deletes.
// Comparing the running state catches all transitions uniformly.
func computeBlame(ctx context.Context, store storage.Store, target delta.Key) ([]blameEntry, error) {
	metas, err := store.List(ctx)
	if err != nil {
		return nil, errf("list snapshots: %w", err)
	}

	var (
		entries  []blameEntry
		running  delta.Snapshot
		prevPres bool
		prevSt   delta.State
	)

	for _, meta := range metas {
		loaded, err := store.Get(ctx, meta.ID)
		if err != nil {
			return nil, errf("load snapshot %s: %w", meta.ID, err)
		}
		switch loaded.Meta.Kind {
		case types.KindFull:
			running = loaded.Full
		case types.KindDelta:
			running = delta.Apply(running, loaded.Delta)
		default:
			return nil, errf("snapshot %s has unknown kind %q", meta.ID, loaded.Meta.Kind)
		}

		curSt, curPres := running[target]

		switch {
		case !prevPres && curPres:
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opCreated, SnapshotID: meta.ID})
		case prevPres && !curPres:
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opRemoved, SnapshotID: meta.ID})
		case prevPres && curPres && !bytes.Equal(prevSt, curSt):
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opModified, SnapshotID: meta.ID})
		}

		prevPres = curPres
		prevSt = curSt
	}
	return entries, nil
}
