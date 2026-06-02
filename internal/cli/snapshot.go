package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

func newSnapshotCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "List and inspect persisted snapshots",
	}
	cmd.AddCommand(newSnapshotListCmd(opts))
	cmd.AddCommand(newSnapshotShowCmd(opts))
	return cmd
}

func newSnapshotListCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all snapshots in storage, oldest first",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotList(cmd.OutOrStdout(), opts.StorageDir)
		},
	}
}

func runSnapshotList(out io.Writer, storageDir string) error {
	store, err := storage.NewLocal(storageDir)
	if err != nil {
		return errf("open storage at %s: %w", storageDir, err)
	}
	metas, err := store.List(context.Background())
	if err != nil {
		return errf("list snapshots: %w", err)
	}
	if len(metas) == 0 {
		fmt.Fprintf(out, "no snapshots in %s\n", storageDir)
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tTIMESTAMP\tPREV")
	for _, m := range metas {
		prev := string(m.PrevID)
		if prev == "" {
			prev = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.ID, m.Kind, m.Timestamp.Format("2006-01-02T15:04:05Z"), prev)
	}
	return w.Flush()
}

func newSnapshotShowCmd(opts *Options) *cobra.Command {
	var keyFilter string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show the reconstructed cluster state at a snapshot",
		Long: "By default prints a summary table of every resource in the snapshot.\n" +
			"Pass --key Kind/Namespace/Name to print the full JSON payload of one resource.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotShow(cmd.OutOrStdout(), opts.StorageDir, types.SnapshotID(args[0]), keyFilter)
		},
	}
	cmd.Flags().StringVar(&keyFilter, "key", "", "show payload of a single resource (format: Kind/Namespace/Name)")
	return cmd
}

func runSnapshotShow(out io.Writer, storageDir string, id types.SnapshotID, keyFilter string) error {
	store, err := storage.NewLocal(storageDir)
	if err != nil {
		return errf("open storage at %s: %w", storageDir, err)
	}
	snap, err := reconstruct(context.Background(), store, id)
	if err != nil {
		return err
	}

	if keyFilter != "" {
		k, err := parseKeyFilter(keyFilter)
		if err != nil {
			return err
		}
		state, ok := snap[k]
		if !ok {
			return errf("resource %s not found in snapshot %s", keyFilter, id)
		}
		return printPrettyJSON(out, state)
	}

	keys := sortedKeys(snap)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tNAMESPACE\tNAME\tSIZE")
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", k.Kind, k.Namespace, k.Name, len(snap[k]))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d resources total\n", len(keys))
	return nil
}

// parseKeyFilter parses a "Kind/Namespace/Name" string into a delta.Key.
// Used by --key on snapshot show, by blame, and by rollback. The Kind
// segment is normalised via kubectl-style aliases so users can type
// `deployment`, `deploy`, `Deployment`, or `DEPLOYMENT` interchangeably
// — matching kubectl's own behaviour.
func parseKeyFilter(s string) (delta.Key, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return delta.Key{}, errf("--key must be Kind/Namespace/Name (got %q)", s)
	}
	return delta.Key{Kind: normalizeKind(parts[0]), Namespace: parts[1], Name: parts[2]}, nil
}

// kindAliases maps kubectl-style aliases (case-insensitive) to KTM's
// canonical Kind names as they appear in delta.Key. Mirrors kubectl's
// short names so the mental model is consistent.
var kindAliases = map[string]string{
	"deployment":  "Deployment",
	"deployments": "Deployment",
	"deploy":      "Deployment",
	"configmap":   "ConfigMap",
	"configmaps":  "ConfigMap",
	"cm":          "ConfigMap",
}

// normalizeKind resolves a user-supplied kind string to its canonical
// form. Unknown inputs pass through unchanged so users who already type
// the canonical form (and future kinds we may add before aliasing them)
// keep working.
func normalizeKind(k string) string {
	if canonical, ok := kindAliases[strings.ToLower(k)]; ok {
		return canonical
	}
	return k
}

// sortedKeys returns the keys of m sorted by Kind, then Namespace, then Name.
// Used to make `show` and `diff` output deterministic.
func sortedKeys(m delta.Snapshot) []delta.Key {
	out := make([]delta.Key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// printPrettyJSON re-indents raw JSON bytes and writes them to out.
// If the bytes are not valid JSON, prints them as-is rather than failing
// — better to show something the user can read than to error out.
func printPrettyJSON(out io.Writer, raw []byte) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		_, werr := out.Write(raw)
		return werr
	}
	if _, err := indented.WriteTo(out); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out)
	return err
}
