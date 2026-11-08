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
		snapshotID  string
		kubeconfig  string
		kubeContext string
		autoYes     bool
		allowCreate bool
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
			"The target cluster is always printed before anything is applied, including\n" +
			"under --yes.\n\n" +
			"If the resource no longer exists, rollback stops rather than recreating it:\n" +
			"a deliberate deletion must not be silently undone. Pass --allow-create to\n" +
			"opt into recreation.\n\n" +
			"Supported kinds: Deployment, ConfigMap.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if snapshotID == "" {
				return fmt.Errorf("--to <snapshot-id> is required")
			}
			target, err := parseKeyFilter(args[0])
			if err != nil {
				return err
			}
			return runRollback(cmd.OutOrStdout(), os.Stdin,
				opts.StorageDir, kubeconfig, kubeContext, target,
				types.SnapshotID(snapshotID), autoYes, allowCreate)
		},
	}
	cmd.Flags().StringVar(&snapshotID, "to", "", "snapshot ID to roll back to")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (empty = try in-cluster, $KUBECONFIG, ~/.kube/config)")
	cmd.Flags().StringVar(&kubeContext, "context", "", "kubeconfig context to use (default: the current context)")
	cmd.Flags().BoolVar(&autoYes, "yes", false, "skip interactive confirmation (for scripts and CI); the target cluster is still printed")
	cmd.Flags().BoolVar(&allowCreate, "allow-create", false, "recreate the resource if it no longer exists in the cluster")
	return cmd
}

func runRollback(
	out io.Writer,
	in io.Reader,
	storageDir, kubeconfig, kubeContext string,
	target delta.Key,
	snapshotID types.SnapshotID,
	autoYes, allowCreate bool,
) error {
	store, err := storage.OpenForRead(storageDir)
	if err != nil {
		return fmt.Errorf("open storage at %s: %w", storageDir, err)
	}
	ctx := context.Background()

	snap, err := reconstruct(ctx, store, snapshotID)
	if err != nil {
		return err
	}
	payload, ok := snap[target]
	if !ok {
		return fmt.Errorf("resource %s is not present in snapshot %s", keyString(target), snapshotID)
	}

	client, clusterTarget, err := kubeclient.NewClientForContext(kubeconfig, kubeContext)
	if err != nil {
		return err
	}

	// Print the destination before anything is applied, unconditionally —
	// --yes suppresses the prompt, not the disclosure. Rolling the right
	// resource back in the wrong cluster is the failure this prevents, and a
	// confirmation prompt that does not name the cluster cannot prevent it.
	fmt.Fprintf(out, "cluster:  %s\n", clusterTarget)
	if clusterTarget.User != "" {
		fmt.Fprintf(out, "user:     %s\n", clusterTarget.User)
	}
	fmt.Fprintf(out, "target:   %s\n", keyString(target))
	fmt.Fprintf(out, "snapshot: %s\n\n", snapshotID)

	switch target.Kind {
	case agent.KindDeployment:
		return rollbackDeployment(ctx, out, in, client, target, payload, autoYes, allowCreate)
	case agent.KindConfigMap:
		return rollbackConfigMap(ctx, out, in, client, target, payload, autoYes, allowCreate)
	default:
		return fmt.Errorf("rollback supports only Deployment and ConfigMap in MVP (got %q)", target.Kind)
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
	autoYes, allowCreate bool,
) error {
	api := client.AppsV1().Deployments(target.Namespace)

	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		if !allowCreate {
			return fmt.Errorf("%s no longer exists in the cluster. Rollback will not recreate it, because a deliberate deletion must not be silently undone. Re-run with --allow-create to recreate it from snapshot", keyString(target))
		}
		return createDeployment(ctx, out, in, api, target, payload, autoYes)
	}
	if err != nil {
		return fmt.Errorf("get Deployment %s: %w", keyString(target), err)
	}

	// Capture the ResourceVersion observed at preview time. We will reuse
	// THIS value for the subsequent Update. We must NOT re-Get after the
	// user confirms — see ADR-0006: a second Get would absorb changes
	// that happened between preview and apply, undermining the user's
	// informed consent.
	previewRV := live.ResourceVersion

	var targetDep appsv1.Deployment
	if err := json.Unmarshal(payload, &targetDep); err != nil {
		return fmt.Errorf("unmarshal target Deployment payload: %w", err)
	}
	prepareForWrite(&targetDep.ObjectMeta, &live.ObjectMeta)

	// The preview renders the object that will be written, not the raw
	// payload, so what the user confirms is what the Update sends. Both
	// sides go through the recorder's marshaller (ADR-0005) so server-owned
	// churn does not bury the one hunk that matters.
	liveView := live.DeepCopy()
	stripServerOwned(&liveView.ObjectMeta)
	_, beforeBytes, err := agent.MarshalDeployment(liveView)
	if err != nil {
		return fmt.Errorf("render preview for %s: %w", keyString(target), err)
	}
	_, afterBytes, err := agent.MarshalDeployment(&targetDep)
	if err != nil {
		return fmt.Errorf("render preview for %s: %w", keyString(target), err)
	}
	if !showPreviewAndConfirm(out, in, target, beforeBytes, afterBytes, autoYes) {
		return nil
	}

	// Re-inject the RV captured at preview time (stripServerOwned cleared
	// it along with the rest). The Kubernetes API server will reject the
	// Update with 409 Conflict if the cluster-side value has moved since
	// previewRV was read.
	targetDep.ResourceVersion = previewRV

	if _, err := api.Update(ctx, &targetDep, metav1.UpdateOptions{}); err != nil {
		if k8serrors.IsConflict(err) {
			return fmt.Errorf("rollback rejected: %s changed in the cluster between preview and apply. Re-run `ktm rollback` to see the new state and confirm again", keyString(target))
		}
		return fmt.Errorf("update Deployment %s: %w", keyString(target), err)
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
	autoYes, allowCreate bool,
) error {
	api := client.CoreV1().ConfigMaps(target.Namespace)

	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		if !allowCreate {
			return fmt.Errorf("%s no longer exists in the cluster. Rollback will not recreate it, because a deliberate deletion must not be silently undone. Re-run with --allow-create to recreate it from snapshot", keyString(target))
		}
		return createConfigMap(ctx, out, in, api, target, payload, autoYes)
	}
	if err != nil {
		return fmt.Errorf("get ConfigMap %s: %w", keyString(target), err)
	}

	previewRV := live.ResourceVersion

	var targetCM corev1.ConfigMap
	if err := json.Unmarshal(payload, &targetCM); err != nil {
		return fmt.Errorf("unmarshal target ConfigMap payload: %w", err)
	}
	prepareForWrite(&targetCM.ObjectMeta, &live.ObjectMeta)

	// Preview the object that will be written; see rollbackDeployment.
	liveView := live.DeepCopy()
	stripServerOwned(&liveView.ObjectMeta)
	_, beforeBytes, err := agent.MarshalConfigMap(liveView)
	if err != nil {
		return fmt.Errorf("render preview for %s: %w", keyString(target), err)
	}
	_, afterBytes, err := agent.MarshalConfigMap(&targetCM)
	if err != nil {
		return fmt.Errorf("render preview for %s: %w", keyString(target), err)
	}
	if !showPreviewAndConfirm(out, in, target, beforeBytes, afterBytes, autoYes) {
		return nil
	}

	targetCM.ResourceVersion = previewRV

	if _, err := api.Update(ctx, &targetCM, metav1.UpdateOptions{}); err != nil {
		if k8serrors.IsConflict(err) {
			return fmt.Errorf("rollback rejected: %s changed in the cluster between preview and apply. Re-run `ktm rollback` to see the new state and confirm again", keyString(target))
		}
		return fmt.Errorf("update ConfigMap %s: %w", keyString(target), err)
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
	var dep appsv1.Deployment
	if err := json.Unmarshal(payload, &dep); err != nil {
		return fmt.Errorf("unmarshal Deployment payload: %w", err)
	}
	prepareForWrite(&dep.ObjectMeta, nil)
	_, view, err := agent.MarshalDeployment(&dep)
	if err != nil {
		return fmt.Errorf("render preview for %s: %w", keyString(target), err)
	}
	if !showCreatePreviewAndConfirm(out, in, target, view, autoYes) {
		return nil
	}
	if _, err := api.Create(ctx, &dep, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create Deployment %s: %w", keyString(target), err)
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
	var cm corev1.ConfigMap
	if err := json.Unmarshal(payload, &cm); err != nil {
		return fmt.Errorf("unmarshal ConfigMap payload: %w", err)
	}
	prepareForWrite(&cm.ObjectMeta, nil)
	_, view, err := agent.MarshalConfigMap(&cm)
	if err != nil {
		return fmt.Errorf("render preview for %s: %w", keyString(target), err)
	}
	if !showCreatePreviewAndConfirm(out, in, target, view, autoYes) {
		return nil
	}
	if _, err := api.Create(ctx, &cm, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create ConfigMap %s: %w", keyString(target), err)
	}
	fmt.Fprintf(out, "rollback applied: %s created from snapshot\n", keyString(target))
	return nil
}

// showPreviewAndConfirm renders a unified diff between the sanitised live
// state (beforeBytes) and the sanitised object about to be written (payload)
// using the printDelta machinery, then prompts the user. Both sides are
// sanitised the same way (see callers), so the diff shows only the
// declarative fields that actually differ. Returns true if the apply should
// proceed.
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

// prepareForWrite turns snapshot metadata into metadata safe to send to the
// API server. live is the current object's metadata, or nil on create.
//
//   - Server-owned fields go; see stripServerOwned.
//   - KTM's own annotations go: they describe the recording, not the
//     resource.
//   - Annotations the recorder never stores take their live value, or are
//     absent if live has none. The snapshot is silent about them, so a full
//     Update would otherwise delete them, including Helm and Argo CD
//     ownership and sync settings. Any such key already in the payload is
//     dropped first: the preview never shows these keys, so writing one
//     from a payload recorded under an older noise list would be a change
//     the user did not see.
func prepareForWrite(m, live *metav1.ObjectMeta) {
	stripServerOwned(m)
	for k := range m.Annotations {
		if strings.HasPrefix(k, types.AnnotationPrefix) || agent.IsUnrecordedAnnotation(k) {
			delete(m.Annotations, k)
		}
	}
	if live != nil {
		for k, v := range live.Annotations {
			if agent.IsUnrecordedAnnotation(k) {
				if m.Annotations == nil {
					m.Annotations = make(map[string]string)
				}
				m.Annotations[k] = v
			}
		}
	}
	if len(m.Annotations) == 0 {
		m.Annotations = nil
	}
}

// stripServerOwned clears the metadata the API server owns. A write ignores
// or rejects these fields, so leaving them on either side of the preview
// would show a change the write does not make: UID and CreationTimestamp
// survive the recorder's sanitisation, and DeletionTimestamp is set on a
// live object held by a finalizer. On Update a stale UID is rejected as an
// immutable-field change; on Create all of them must be empty.
func stripServerOwned(m *metav1.ObjectMeta) {
	m.DeletionTimestamp = nil
	m.DeletionGracePeriodSeconds = nil
	m.ResourceVersion = ""
	m.UID = ""
	m.CreationTimestamp = metav1.Time{}
	m.Generation = 0
	m.ManagedFields = nil
	m.SelfLink = ""
}
