package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	Actors     string           // comma-separated SSA manager names from ktm.io/managers
	SnapshotID types.SnapshotID
}

func newBlameCmd(opts *Options) *cobra.Command {
	var namespace string

	cmd := &cobra.Command{
		Use:   "blame <kind>/<namespace>/<name>",
		Short: "Show the timeline of changes for one resource",
		Long: "blame walks the snapshot history forward and reports every point in time at\n" +
			"which the target resource was created, modified, or removed. Unlike a naive\n" +
			"scan of delta `removed` entries, this algorithm reconstructs the full snapshot\n" +
			"state at each step and compares against the previous step — which is the only\n" +
			"way to detect deletes that happen to land on a full-snapshot tick.\n\n" +
			"Use --namespace to restrict the blame scan to a single namespace. When\n" +
			"provided, only events for resources in that namespace are considered,\n" +
			"mirroring the --namespace behaviour of `ktm diff`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := parseKeyFilter(args[0])
			if err != nil {
				return err
			}
			// If --namespace is set and conflicts with the namespace encoded in
			// the positional arg, the positional arg wins (it's more specific).
			// --namespace is most useful when the positional arg uses a wildcard
			// or a bare kind/name form in future CLI extensions.
			return runBlame(cmd.OutOrStdout(), opts.StorageDir, target, namespace)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "limit blame to resources in this namespace (overridden by namespace in positional arg)")
	return cmd
}

func runBlame(out io.Writer, storageDir string, target delta.Key, namespace string) error {
	store, err := storage.NewLocal(storageDir)
	if err != nil {
		return errf("open storage at %s: %w", storageDir, err)
	}

	// If --namespace was given and the target key has no namespace of its
	// own (empty string), apply the flag value. This is a forward-compat
	// hook for future wildcard / bare-name positional args; with the
	// current strict kind/namespace/name format the positional arg always
	// sets the namespace explicitly.
	if namespace != "" && target.Namespace == "" {
		target.Namespace = namespace
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
	fmt.Fprintln(w, "TIME\tOP\tACTORS\tSNAPSHOT")
	for _, e := range entries {
		actors := e.Actors
		if actors == "" {
			actors = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Time.Format(time.RFC3339), e.Op, actors, e.SnapshotID)
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
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opCreated, Actors: actorsFromState(curSt), SnapshotID: meta.ID})
		case prevPres && !curPres:
			// Resource was deleted; use prevSt to recover the last-known actors.
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opRemoved, Actors: actorsFromState(prevSt), SnapshotID: meta.ID})
		case prevPres && curPres && !bytes.Equal(prevSt, curSt):
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opModified, Actors: actorsFromState(curSt), SnapshotID: meta.ID})
		}

		prevPres = curPres
		prevSt = curSt
	}
	return entries, nil
}

// actorsFromState decodes the synthetic "ktm.io/managers" annotation that
// the marshal layer injects into every stored state. Returns an empty string
// when the annotation is absent or the state cannot be parsed (e.g. for
// older snapshots written before Phase 3.1).
func actorsFromState(s delta.State) string {
	if s == nil {
		return ""
	}
	var obj struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(s, &obj); err != nil {
		return ""
	}
	return obj.Metadata.Annotations["ktm.io/managers"]
}
