package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

func newDiffCmd(opts *Options) *cobra.Command {
	var from, to, namespace string

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff between two snapshots",
		Long: "Reconstructs both snapshots, optionally filters by namespace, and prints\n" +
			"a colored git-style diff: green for additions, red for removals, yellow\n" +
			"headers for resources with internal modifications.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return errf("--from and --to are required")
			}
			return runDiff(cmd.OutOrStdout(), opts.StorageDir,
				types.SnapshotID(from), types.SnapshotID(to), namespace)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "snapshot ID to diff from (older)")
	cmd.Flags().StringVar(&to, "to", "", "snapshot ID to diff to (newer)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "limit diff to resources in this namespace")
	return cmd
}

func runDiff(out io.Writer, storageDir string, fromID, toID types.SnapshotID, ns string) error {
	store, err := storage.NewLocal(storageDir)
	if err != nil {
		return errf("open storage at %s: %w", storageDir, err)
	}

	ctx := context.Background()
	prev, err := reconstruct(ctx, store, fromID)
	if err != nil {
		return err
	}
	next, err := reconstruct(ctx, store, toID)
	if err != nil {
		return err
	}

	if ns != "" {
		prev = filterByNamespace(prev, ns)
		next = filterByNamespace(next, ns)
	}

	d := delta.Compute(prev, next)
	printDelta(out, d, prev)
	return nil
}

// filterByNamespace returns a new Snapshot containing only entries whose
// Namespace matches ns. The original Snapshot is not mutated.
func filterByNamespace(s delta.Snapshot, ns string) delta.Snapshot {
	out := delta.Snapshot{}
	for k, v := range s {
		if k.Namespace == ns {
			out[k] = v
		}
	}
	return out
}

// printDelta writes a human-readable diff of d to out. prev is used to
// fetch the "before" payload of Modified entries.
//
// Output layout (per section, only if non-empty):
//
//	==== Added (N) ====
//	+ Kind/Namespace/Name
//	  <pretty JSON, line-prefixed with "+ ">
//
//	==== Modified (N) ====
//	~ Kind/Namespace/Name
//	  <unified diff with "+" / "-" line prefixes>
//
//	==== Removed (N) ====
//	- Kind/Namespace/Name
//	  <pretty JSON, line-prefixed with "- ">
func printDelta(out io.Writer, d delta.Delta, prev delta.Snapshot) {
	green := color.New(color.FgGreen).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()

	if len(d.Added) == 0 && len(d.Modified) == 0 && len(d.Removed) == 0 {
		fmt.Fprintln(out, "no changes between the two snapshots")
		return
	}

	// Added
	if len(d.Added) > 0 {
		fmt.Fprintln(out, bold(fmt.Sprintf("==== Added (%d) ====", len(d.Added))))
		for _, k := range sortedKeysFromMap(d.Added) {
			fmt.Fprintln(out, green("+ "+keyString(k)))
			for _, line := range prettyLines(d.Added[k]) {
				fmt.Fprintln(out, green("  + "+line))
			}
			fmt.Fprintln(out)
		}
	}

	// Modified — proper unified diff per resource
	if len(d.Modified) > 0 {
		fmt.Fprintln(out, bold(fmt.Sprintf("==== Modified (%d) ====", len(d.Modified))))
		for _, k := range sortedKeysFromMap(d.Modified) {
			fmt.Fprintln(out, yellow("~ "+keyString(k)))
			beforeText := strings.Join(prettyLines(prev[k]), "\n")
			afterText := strings.Join(prettyLines(d.Modified[k]), "\n")
			diff := difflib.UnifiedDiff{
				A:        strings.Split(beforeText, "\n"),
				B:        strings.Split(afterText, "\n"),
				FromFile: "before",
				ToFile:   "after",
				Context:  3,
			}
			text, _ := difflib.GetUnifiedDiffString(diff)
			for _, line := range strings.Split(text, "\n") {
				switch {
				case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@"):
					fmt.Fprintln(out, "  "+line)
				case strings.HasPrefix(line, "+"):
					fmt.Fprintln(out, "  "+green(line))
				case strings.HasPrefix(line, "-"):
					fmt.Fprintln(out, "  "+red(line))
				default:
					fmt.Fprintln(out, "  "+line)
				}
			}
		}
	}

	// Removed
	if len(d.Removed) > 0 {
		fmt.Fprintln(out, bold(fmt.Sprintf("==== Removed (%d) ====", len(d.Removed))))
		removed := make([]delta.Key, 0, len(d.Removed))
		for k := range d.Removed {
			removed = append(removed, k)
		}
		sort.Slice(removed, func(i, j int) bool { return keyLess(removed[i], removed[j]) })
		for _, k := range removed {
			fmt.Fprintln(out, red("- "+keyString(k)))
			for _, line := range prettyLines(prev[k]) {
				fmt.Fprintln(out, red("  - "+line))
			}
			fmt.Fprintln(out)
		}
	}
}

// sortedKeysFromMap returns the keys of an Added/Modified-style map,
// sorted deterministically.
func sortedKeysFromMap(m map[delta.Key]delta.State) []delta.Key {
	out := make([]delta.Key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return keyLess(out[i], out[j]) })
	return out
}

func keyLess(a, b delta.Key) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}

func keyString(k delta.Key) string {
	return fmt.Sprintf("%s/%s/%s", k.Kind, k.Namespace, k.Name)
}

// prettyLines pretty-prints raw JSON bytes and returns the lines. Falls
// back to splitting the raw input if it isn't valid JSON.
func prettyLines(raw []byte) []string {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return strings.Split(string(raw), "\n")
	}
	return strings.Split(b.String(), "\n")
}
