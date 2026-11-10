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

// blameOp is what happened to the target resource at a snapshot.
type blameOp string

const (
	opCreated  blameOp = "CREATED"
	opModified blameOp = "MODIFIED"
	opRemoved  blameOp = "REMOVED"
)

// blameEntry is one row of the blame timeline.
type blameEntry struct {
	Time time.Time
	Op   blameOp
	// Managers is the comma-separated set of field managers on the resource at
	// this point, from ktm.io/managers. Not "actors": managedFields lists every
	// manager owning any field, so it cannot say which one made this change.
	Managers   string
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
			"mirroring the --namespace behaviour of `ktm diff`.\n\n" +
			"The MANAGERS column lists the Kubernetes field managers observed on the\n" +
			"resource at that point — every manager owning any field, taken from\n" +
			"managedFields. It is a set of candidates, NOT proof that one of them made\n" +
			"the change on that row.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := parseKeyFilter(args[0])
			if err != nil {
				return err
			}
			return runBlame(cmd.OutOrStdout(), opts.StorageDir, target, namespace)
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "limit blame to resources in this namespace (overridden by namespace in positional arg)")
	return cmd
}

func runBlame(out io.Writer, storageDir string, target delta.Key, namespace string) error {
	store, err := storage.OpenForRead(storageDir)
	if err != nil {
		return fmt.Errorf("open storage at %s: %w", storageDir, err)
	}

	// --namespace applies only when the positional arg leaves the namespace
	// empty (Kind//Name); otherwise the positional arg wins.
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
	fmt.Fprintln(w, "TIME\tOP\tMANAGERS\tSNAPSHOT")
	for _, e := range entries {
		managers := e.Managers
		if managers == "" {
			managers = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Time.Format(time.RFC3339), e.Op, managers, e.SnapshotID)
	}
	return w.Flush()
}

// computeBlame walks the history in order, keeping a running reconstruction,
// and emits an entry whenever the target appears, changes or disappears.
//
// One running reconstruction costs O(N + changes), where N independent
// reconstructs would cost O(N * fullEvery) reads. It compares states, not
// delta entries, because a delete that lands on a full-snapshot tick shows up
// only as absence from the full, never as a removed entry.
func computeBlame(ctx context.Context, store storage.Store, target delta.Key) ([]blameEntry, error) {
	metas, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	var (
		entries  []blameEntry
		running  delta.Snapshot
		prevPres bool
		prevSt   delta.State
	)

	for _, meta := range metas {
		// Skip a delta whose Kinds excludes the target kind. Fulls are always
		// loaded because they replace the running state, and an empty Kinds is
		// unknown, so it is loaded too.
		if meta.Kind == types.KindDelta && len(meta.Kinds) > 0 && !kindsContain(meta.Kinds, target.Kind) {
			continue
		}

		loaded, err := store.Get(ctx, meta.ID)
		if err != nil {
			return nil, fmt.Errorf("load snapshot %s: %w", meta.ID, err)
		}
		switch loaded.Meta.Kind {
		case types.KindFull:
			running = loaded.Full
		case types.KindDelta:
			running = delta.Apply(running, loaded.Delta)
		default:
			return nil, fmt.Errorf("snapshot %s has unknown kind %q", meta.ID, loaded.Meta.Kind)
		}

		curSt, curPres := running[target]

		switch {
		case !prevPres && curPres:
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opCreated, Managers: managersFromState(curSt), SnapshotID: meta.ID})
		case prevPres && !curPres:
			// Deleted: report the last-known managers.
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opRemoved, Managers: managersFromState(prevSt), SnapshotID: meta.ID})
		case prevPres && curPres && !bytes.Equal(prevSt, curSt):
			entries = append(entries, blameEntry{Time: meta.Timestamp, Op: opModified, Managers: managersFromState(curSt), SnapshotID: meta.ID})
		}

		prevPres = curPres
		prevSt = curSt
	}
	return entries, nil
}

// kindsContain reports whether kinds contains kind.
func kindsContain(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// managersFromState returns the ktm.io/managers annotation of s, or "" when
// it is absent or s cannot be parsed. See blameEntry.Managers for what it
// does and does not prove.
func managersFromState(s delta.State) string {
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
	return obj.Metadata.Annotations[types.ManagersAnnotation]
}
