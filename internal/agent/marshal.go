package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// This file turns typed Kubernetes objects into delta.Key / delta.State,
// stripping everything that changes without a human changing it (ADR-0005).
// dynamic_informers.go applies the same rules to unstructured objects.

// Kind names used in delta.Keys.
const (
	KindDeployment = "Deployment"
	KindConfigMap  = "ConfigMap"
)

// KeyForDeployment returns the delta.Key identifying d.
func KeyForDeployment(d *appsv1.Deployment) delta.Key {
	return delta.Key{Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name}
}

// KeyForConfigMap returns the delta.Key identifying cm.
func KeyForConfigMap(cm *corev1.ConfigMap) delta.Key {
	return delta.Key{Kind: KindConfigMap, Namespace: cm.Namespace, Name: cm.Name}
}

// MarshalDeployment converts d into a (Key, State) pair suitable for
// Buffer.Upsert. The object is DeepCopied before sanitisation, so the
// informer's shared cache is never mutated.
func MarshalDeployment(d *appsv1.Deployment) (delta.Key, delta.State, error) {
	if d == nil {
		return delta.Key{}, nil, fmt.Errorf("agent: MarshalDeployment: nil object")
	}
	clean := d.DeepCopy()
	sanitiseMeta(&clean.ObjectMeta)
	payload, err := json.Marshal(clean)
	if err != nil {
		return delta.Key{}, nil, fmt.Errorf("agent: marshal Deployment %s/%s: %w", d.Namespace, d.Name, err)
	}
	payload, err = stripStatus(payload)
	if err != nil {
		return delta.Key{}, nil, fmt.Errorf("agent: strip status from Deployment %s/%s: %w", d.Namespace, d.Name, err)
	}
	return KeyForDeployment(d), delta.State(payload), nil
}

// MarshalConfigMap converts cm into a (Key, State) pair suitable for
// Buffer.Upsert. The object is DeepCopied before sanitisation, so the
// informer's shared cache is never mutated.
func MarshalConfigMap(cm *corev1.ConfigMap) (delta.Key, delta.State, error) {
	if cm == nil {
		return delta.Key{}, nil, fmt.Errorf("agent: MarshalConfigMap: nil object")
	}
	clean := cm.DeepCopy()
	sanitiseMeta(&clean.ObjectMeta)
	payload, err := json.Marshal(clean)
	if err != nil {
		return delta.Key{}, nil, fmt.Errorf("agent: marshal ConfigMap %s/%s: %w", cm.Namespace, cm.Name, err)
	}
	// ConfigMaps have no .status; the round-trip still normalises key order
	// to match Deployments.
	payload, err = stripStatus(payload)
	if err != nil {
		return delta.Key{}, nil, fmt.Errorf("agent: strip status from ConfigMap %s/%s: %w", cm.Namespace, cm.Name, err)
	}
	return KeyForConfigMap(cm), delta.State(payload), nil
}

// noiseAnnotationKeys is the set of annotation keys that are owned by
// controllers or tooling and change without user intent. Keeping them
// produces spurious MODIFIED deltas in the blame timeline for every
// automated touch (e.g. every Helm upgrade bumps
// "meta.helm.sh/release-revision"; every kubectl apply rewrites
// "kubectl.kubernetes.io/last-applied-configuration").
//
// Rule for adding a key here: the annotation's value changes as a side-
// effect of a tool operation, not because a human set it. If the content
// itself is meaningful (e.g. an annotation recording the last deploy
// actor), it should NOT be in this list.
var noiseAnnotationKeys = map[string]struct{}{
	// kubectl: full object JSON, regenerated on every `kubectl apply`.
	"kubectl.kubernetes.io/last-applied-configuration": {},
	// Deployment controller: monotonically increments on each rollout.
	"deployment.kubernetes.io/revision": {},
	// DaemonSet controller: increments on template changes.
	"deprecated.daemonset.template.generation": {},
}

// noiseAnnotationPrefixes lists annotation key prefixes that are wholly
// controller-owned. Any annotation whose key starts with one of these
// prefixes is considered noise and stripped.
var noiseAnnotationPrefixes = []string{
	// Argo CD: sync status, health, revision, managed-by markers — all
	// set by the controller, not by a human.
	"argocd.argoproj.io/",
	// Flux CD: revision, checksum, last-applied markers.
	"fluxcd.io/",
	"kustomize.toolkit.fluxcd.io/",
	// Helm: release name, namespace, revision — change on every upgrade.
	"meta.helm.sh/",
}

// sanitiseMeta strips fields that mutate without user intent and would
// otherwise produce spurious "modified" deltas:
//
//   - ResourceVersion bumps on every server-side touch, including
//     no-op resyncs by other controllers.
//   - ManagedFields is large and tracks SSA owners, not user intent.
//     The manager names are extracted first and preserved as
//     types.ManagersAnnotation, so blame can show which tools touched it.
//   - Generation increments on every spec change — but the spec change
//     itself is the signal, so the generation field is redundant noise.
//   - Known controller-owned annotations (see noiseAnnotationKeys and
//     noiseAnnotationPrefixes) change as side-effects of tool operations
//     and would produce spurious MODIFIED blame entries.
//
// CreationTimestamp, UID, Labels, and user-intent annotations are kept.
// The full .status block is removed downstream by stripStatus — see ADR-0005.
func sanitiseMeta(m *metav1.ObjectMeta) {
	managers := extractManagers(m.ManagedFields)
	m.ResourceVersion = ""
	m.ManagedFields = nil
	m.Generation = 0
	stripNoiseAnnotations(m)
	// After stripping, so the synthetic annotation can never be removed.
	if len(managers) > 0 {
		if m.Annotations == nil {
			m.Annotations = make(map[string]string)
		}
		m.Annotations[types.ManagersAnnotation] = strings.Join(managers, ",")
	}
}

// extractManagers returns the sorted, de-duplicated non-empty manager names.
func extractManagers(mf []metav1.ManagedFieldsEntry) []string {
	if len(mf) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(mf))
	for _, entry := range mf {
		if entry.Manager != "" {
			seen[entry.Manager] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// IsUnrecordedAnnotation reports whether the recorder drops annotation key
// from snapshots. A snapshot therefore says nothing about such a key, and
// rollback must keep the live value instead of deleting it.
func IsUnrecordedAnnotation(key string) bool { return isNoiseAnnotationKey(key) }

// isNoiseAnnotationKey reports whether key is controller-owned noise. The
// typed and dynamic paths both use it, so they filter identically.
func isNoiseAnnotationKey(key string) bool {
	if _, exact := noiseAnnotationKeys[key]; exact {
		return true
	}
	for _, prefix := range noiseAnnotationPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// stripNoiseAnnotations removes controller-owned annotations in-place.
// Called only on a DeepCopy, so the informer cache is never mutated.
func stripNoiseAnnotations(m *metav1.ObjectMeta) {
	if len(m.Annotations) == 0 {
		return
	}
	for key := range m.Annotations {
		if isNoiseAnnotationKey(key) {
			delete(m.Annotations, key)
		}
	}
	// Normalise: if stripping left an empty map, nil it so two objects
	// that originally had only noise annotations produce identical JSON
	// (encoding/json encodes nil map as null / omits with omitempty,
	// while an empty map encodes as "{}").
	if len(m.Annotations) == 0 {
		m.Annotations = nil
	}
}

// stripStatus removes the top-level "status" key from the JSON encoding
// of any Kubernetes object. See ADR-0005: KTM records the declarative
// surface of supported resources; controller-owned status is not part
// of the recorded contract.
//
// Implemented via map round-trip because Go's encoding/json `omitempty`
// tag does not apply to non-pointer struct values: setting Status to
// its zero value would still serialise as `"status":{}`. Re-marshalling
// from a map sorts keys alphabetically (a documented guarantee of
// encoding/json), so the output is deterministic for a given input.
func stripStatus(payload []byte) ([]byte, error) {
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(payload, &asMap); err != nil {
		return nil, err
	}
	delete(asMap, "status")
	return json.Marshal(asMap)
}
