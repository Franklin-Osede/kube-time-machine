package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/internal/kubeclient"
	"github.com/Franklin-Osede/kube-time-machine/internal/storage"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

func newRollbackCmd(opts *Options) *cobra.Command {
	var (
		snapshotID string
		kubeconfig string
		autoYes    bool
	)

	cmd := &cobra.Command{
		Use:   "rollback <kind>/<namespace>/<name>",
		Short: "Roll a single resource back to a previous snapshot",
		Long: "Reconstructs the target snapshot, fetches the current live resource from\n" +
			"the cluster, shows a diff between the two, asks for confirmation, then issues\n" +
			"an Update using the live ResourceVersion captured during the preview. If the\n" +
			"resource has changed in the cluster between preview and apply, Kubernetes\n" +
			"rejects the update (optimistic locking) — re-run ktm rollback to see the new\n" +
			"state and confirm again.\n\n" +
			"Supported kinds: Deployment, ConfigMap.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if snapshotID == "" {
				return errf("--to <snapshot-id> is required")
			}
			target, err := parseKeyFilter(args[0])
			if err != nil {
				return err
			}
			return runRollback(cmd.OutOrStdout(), os.Stdin,
				opts.StorageDir, kubeconfig, target, types.SnapshotID(snapshotID), autoYes)
		},
	}
	cmd.Flags().StringVar(&snapshotID, "to", "", "snapshot ID to roll back to")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (empty = try in-cluster, $KUBECONFIG, ~/.kube/config)")
	cmd.Flags().BoolVar(&autoYes, "yes", false, "skip interactive confirmation (for scripts and CI)")
	return cmd
}

func runRollback(
	out io.Writer,
	in io.Reader,
	storageDir, kubeconfig string,
	target delta.Key,
	snapshotID types.SnapshotID,
	autoYes bool,
) error {
	store, err := storage.NewLocal(storageDir)
	if err != nil {
		return errf("open storage at %s: %w", storageDir, err)
	}
	ctx := context.Background()

	snap, err := reconstruct(ctx, store, snapshotID)
	if err != nil {
		return err
	}
	payload, ok := snap[target]
	if !ok {
		return errf("resource %s is not present in snapshot %s", keyString(target), snapshotID)
	}

	client, err := kubeclient.NewClient(kubeconfig)
	if err != nil {
		return err
	}

	switch target.Kind {
	case agent.KindDeployment:
		return rollbackDeployment(ctx, out, in, client, target, payload, autoYes)
	case agent.KindConfigMap:
		return rollbackConfigMap(ctx, out, in, client, target, payload, autoYes)
	default:
		return errf("rollback supports only Deployment and ConfigMap in MVP (got %q)", target.Kind)
	}
}

// rollbackDeployment implements the Update path for Deployments and the
// 404-to-Create fallback. The flow is identical for ConfigMaps below
// modulo the typed API surface; the duplication is deliberate — sharing
// via generics or interfaces would obscure which kinds are supported
// and would land before we've earned the abstraction with a third kind.
func rollbackDeployment(
	ctx context.Context,
	out io.Writer,
	in io.Reader,
	client kubernetes.Interface,
	target delta.Key,
	payload delta.State,
	autoYes bool,
) error {
	api := client.AppsV1().Deployments(target.Namespace)

	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return createDeployment(ctx, out, in, api, target, payload, autoYes)
	}
	if err != nil {
		return errf("get Deployment %s: %w", keyString(target), err)
	}

	// Capture the ResourceVersion observed at preview time. We will reuse
	// THIS value for the subsequent Update. We must NOT re-Get after the
	// user confirms — see ADR-0006: a second Get would absorb changes
	// that happened between preview and apply, undermining the user's
	// informed consent.
	previewRV := live.ResourceVersion

	// Sanitise the live object through the SAME marshaller the agent uses
	// to record snapshots (ADR-0005: strip .status, resourceVersion,
	// managedFields, generation). Without this the preview diffs the raw
	// live object — full of server-owned churn — against the sanitised
	// snapshot payload, drowning the one hunk the user actually cares
	// about in noise at the exact moment they give informed consent.
	_, beforeBytes, err := agent.MarshalDeployment(live)
	if err != nil {
		return errf("render preview for %s: %w", keyString(target), err)
	}
	if !showPreviewAndConfirm(out, in, target, beforeBytes, payload, autoYes) {
		return nil
	}

	var targetDep appsv1.Deployment
	if err := json.Unmarshal(payload, &targetDep); err != nil {
		return errf("unmarshal target Deployment payload: %w", err)
	}
	// Strip server-owned metadata that the snapshot inherited. sanitiseMeta
	// in the agent (ADR-0005) deliberately keeps UID and CreationTimestamp
	// because they carry no per-flush noise; but on the Update path we MUST
	// drop them. Concretely: if the user deleted and recreated this resource
	// between snapshot time and now, the live UID is different, and an
	// Update carrying the snapshot's old UID is rejected by the API server
	// as an immutable-field violation. Stripping is symmetric with the
	// create-on-404 path below.
	stripServerOwned(&targetDep.ObjectMeta)
	// Re-inject the RV captured at preview time (stripServerOwned cleared
	// it along with the rest). The Kubernetes API server will reject the
	// Update with 409 Conflict if the cluster-side value has moved since
	// previewRV was read.
	targetDep.ResourceVersion = previewRV

	if _, err := api.Update(ctx, &targetDep, metav1.UpdateOptions{}); err != nil {
		if k8serrors.IsConflict(err) {
			return errf("rollback rejected: %s changed in the cluster between preview and apply. Re-run `ktm rollback` to see the new state and confirm again.", keyString(target))
		}
		return errf("update Deployment %s: %w", keyString(target), err)
	}
	fmt.Fprintf(out, "rollback applied: %s now matches snapshot\n", keyString(target))
	return nil
}

func rollbackConfigMap(
	ctx context.Context,
	out io.Writer,
	in io.Reader,
	client kubernetes.Interface,
	target delta.Key,
	payload delta.State,
	autoYes bool,
) error {
	api := client.CoreV1().ConfigMaps(target.Namespace)

	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return createConfigMap(ctx, out, in, api, target, payload, autoYes)
	}
	if err != nil {
		return errf("get ConfigMap %s: %w", keyString(target), err)
	}

	previewRV := live.ResourceVersion

	// Same sanitisation as in rollbackDeployment — diff the snapshot
	// payload against a like-for-like sanitised view of the live object,
	// not the raw server representation. See the comment there.
	_, beforeBytes, err := agent.MarshalConfigMap(live)
	if err != nil {
		return errf("render preview for %s: %w", keyString(target), err)
	}
	if !showPreviewAndConfirm(out, in, target, beforeBytes, payload, autoYes) {
		return nil
	}

	var targetCM corev1.ConfigMap
	if err := json.Unmarshal(payload, &targetCM); err != nil {
		return errf("unmarshal target ConfigMap payload: %w", err)
	}
	// Same UID-stripping invariant as in rollbackDeployment — see the
	// comment there for why this is load-bearing on delete+recreate.
	stripServerOwned(&targetCM.ObjectMeta)
	targetCM.ResourceVersion = previewRV

	if _, err := api.Update(ctx, &targetCM, metav1.UpdateOptions{}); err != nil {
		if k8serrors.IsConflict(err) {
			return errf("rollback rejected: %s changed in the cluster between preview and apply. Re-run `ktm rollback` to see the new state and confirm again.", keyString(target))
		}
		return errf("update ConfigMap %s: %w", keyString(target), err)
	}
	fmt.Fprintf(out, "rollback applied: %s now matches snapshot\n", keyString(target))
	return nil
}

func createDeployment(
	ctx context.Context,
	out io.Writer,
	in io.Reader,
	api interface {
		Create(context.Context, *appsv1.Deployment, metav1.CreateOptions) (*appsv1.Deployment, error)
	},
	target delta.Key,
	payload delta.State,
	autoYes bool,
) error {
	fmt.Fprintf(out, "Deployment %s does not exist in the cluster; will be CREATED from snapshot.\n", keyString(target))
	if !showCreatePreviewAndConfirm(out, in, target, payload, autoYes) {
		return nil
	}
	var dep appsv1.Deployment
	if err := json.Unmarshal(payload, &dep); err != nil {
		return errf("unmarshal Deployment payload: %w", err)
	}
	stripServerOwned(&dep.ObjectMeta)
	if _, err := api.Create(ctx, &dep, metav1.CreateOptions{}); err != nil {
		return errf("create Deployment %s: %w", keyString(target), err)
	}
	fmt.Fprintf(out, "rollback applied: %s created from snapshot\n", keyString(target))
	return nil
}

func createConfigMap(
	ctx context.Context,
	out io.Writer,
	in io.Reader,
	api interface {
		Create(context.Context, *corev1.ConfigMap, metav1.CreateOptions) (*corev1.ConfigMap, error)
	},
	target delta.Key,
	payload delta.State,
	autoYes bool,
) error {
	fmt.Fprintf(out, "ConfigMap %s does not exist in the cluster; will be CREATED from snapshot.\n", keyString(target))
	if !showCreatePreviewAndConfirm(out, in, target, payload, autoYes) {
		return nil
	}
	var cm corev1.ConfigMap
	if err := json.Unmarshal(payload, &cm); err != nil {
		return errf("unmarshal ConfigMap payload: %w", err)
	}
	stripServerOwned(&cm.ObjectMeta)
	if _, err := api.Create(ctx, &cm, metav1.CreateOptions{}); err != nil {
		return errf("create ConfigMap %s: %w", keyString(target), err)
	}
	fmt.Fprintf(out, "rollback applied: %s created from snapshot\n", keyString(target))
	return nil
}

// showPreviewAndConfirm renders a unified diff between the sanitised live
// state (beforeBytes) and the target snapshot payload using the existing
// printDelta machinery, then prompts the user. Both sides are sanitised
// the same way (see callers), so the diff shows only the declarative
// fields that actually differ. Returns true if the apply should proceed.
func showPreviewAndConfirm(
	out io.Writer,
	in io.Reader,
	target delta.Key,
	beforeBytes delta.State,
	payload delta.State,
	autoYes bool,
) bool {
	d := delta.Delta{
		Modified: map[delta.Key]delta.State{target: payload},
	}
	prev := delta.Snapshot{target: beforeBytes}
	fmt.Fprintln(out, "--- preview (apply will Update with the ResourceVersion read above) ---")
	printDelta(out, d, prev)
	return autoYes || promptConfirm(in, out)
}

func showCreatePreviewAndConfirm(out io.Writer, in io.Reader, target delta.Key, payload delta.State, autoYes bool) bool {
	fmt.Fprintln(out, "--- preview of resource to be created ---")
	for _, line := range prettyLines(payload) {
		fmt.Fprintln(out, "  + "+line)
	}
	return autoYes || promptConfirm(in, out)
}

// promptConfirm prints a [y/N] prompt and reads one line from in.
// Default (empty line) is no. Anything starting with 'y' or 'Y' is yes.
func promptConfirm(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "Apply rollback? [y/N]: ")
	reader := bufio.NewReader(in)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return true
	}
	fmt.Fprintln(out, "rollback aborted")
	return false
}

// stripServerOwned removes metadata fields the API server assigns and
// that would either be ignored or cause Create to fail if echoed back.
// Most are already absent because the agent's marshaller sanitises
// them, but stripping defensively keeps the Create path robust against
// any future change to marshal.go.
func stripServerOwned(m *metav1.ObjectMeta) {
	m.ResourceVersion = ""
	m.UID = ""
	m.CreationTimestamp = metav1.Time{}
	m.Generation = 0
	m.ManagedFields = nil
	m.SelfLink = ""
}
