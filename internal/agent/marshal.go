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

// MarshalDeployment converts d into a sanitised (Key, State) pair. It works
// on a DeepCopy, so the informer's shared cache is never mutated.
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

// MarshalConfigMap converts cm into a sanitised (Key, State) pair. It works
// on a DeepCopy, so the informer's shared cache is never mutated.
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

// noiseAnnotationKeys are annotations whose value changes as a side effect
// of a tool operation, not because a human set it. Recording them would make
// every automated touch a spurious MODIFIED delta.
var noiseAnnotationKeys = map[string]struct{}{
	// Rewritten by every `kubectl apply`.
	"kubectl.kubernetes.io/last-applied-configuration": {},
	// Bumped by the Deployment controller on each rollout.
	"deployment.kubernetes.io/revision": {},
	// Bumped by the DaemonSet controller on template changes.
	"deprecated.daemonset.template.generation": {},
}

// noiseAnnotationPrefixes are annotation key prefixes that are wholly
// controller-owned (Argo CD, Flux, Helm).
var noiseAnnotationPrefixes = []string{
	"argocd.argoproj.io/",
	"fluxcd.io/",
	"kustomize.toolkit.fluxcd.io/",
	"meta.helm.sh/",
}

// sanitiseMeta strips metadata that changes without user intent:
// ResourceVersion, ManagedFields (its manager names are kept as
// types.ManagersAnnotation), Generation, and noise annotations. stripStatus
// removes .status. See ADR-0002 and ADR-0005.
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

// stripNoiseAnnotations removes controller-owned annotations in place. Call
// it only on a DeepCopy.
func stripNoiseAnnotations(m *metav1.ObjectMeta) {
	if len(m.Annotations) == 0 {
		return
	}
	for key := range m.Annotations {
		if isNoiseAnnotationKey(key) {
			delete(m.Annotations, key)
		}
	}
	// nil, not empty, so the JSON matches an object that had no annotations.
	if len(m.Annotations) == 0 {
		m.Annotations = nil
	}
}

// stripStatus removes the top-level "status" key from a JSON object (see
// ADR-0005). It round-trips through a map because omitempty does not apply
// to struct values, so a zeroed Status would still encode as "status":{}.
// encoding/json sorts map keys, so the output is deterministic.
func stripStatus(payload []byte) ([]byte, error) {
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(payload, &asMap); err != nil {
		return nil, err
	}
	delete(asMap, "status")
	return json.Marshal(asMap)
}
