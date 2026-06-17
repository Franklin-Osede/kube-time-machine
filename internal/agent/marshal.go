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
)

// This file is the boundary between Kubernetes typed objects and the
// internal delta model. Every place upstream of here speaks
// *appsv1.Deployment / *corev1.ConfigMap; every place downstream speaks
// delta.Key / delta.State. If we ever swap typed informers for dynamic
// informers (Phase 2), only this file changes.
//
// MarshalDeployment / MarshalConfigMap are used by Add/Update handlers.
// KeyForDeployment / KeyForConfigMap are used by Delete handlers, where
// we only need to identify the resource to remove from the buffer.

// Kind names used in delta.Keys. Centralised so the rest of the codebase
// never sees stringly-typed kind constants.
const (
	KindDeployment = "Deployment"
	KindConfigMap  = "ConfigMap"
)

// KeyForDeployment returns the delta.Key identifying d. Safe to call
// with the live informer object; does not mutate or copy.
func KeyForDeployment(d *appsv1.Deployment) delta.Key {
	return delta.Key{Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name}
}

// KeyForConfigMap returns the delta.Key identifying cm. Safe to call
// with the live informer object; does not mutate or copy.
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
	// ConfigMaps have no .status, so stripStatus is a no-op here; we
	// still route through it for contract symmetry and to keep the
	// post-marshal key order consistent across kinds.
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
//     The manager names are extracted first and preserved as the
//     synthetic annotation "ktm.io/managers" so blame output can show
//     which controller or kubectl-user last touched the resource.
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
	// Inject the extracted manager names AFTER noise stripping so they
	// are never accidentally removed by stripNoiseAnnotations.
	if len(managers) > 0 {
		if m.Annotations == nil {
			m.Annotations = make(map[string]string)
		}
		m.Annotations["ktm.io/managers"] = strings.Join(managers, ",")
	}
}

// extractManagers returns a sorted, de-duplicated list of the manager
// field values found in managedFields entries. An empty slice is
// returned when no entries exist or all managers are empty strings.
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

// stripNoiseAnnotations removes controller-owned annotations in-place.
// Called only on a DeepCopy, so the informer cache is never mutated.
func stripNoiseAnnotations(m *metav1.ObjectMeta) {
	if len(m.Annotations) == 0 {
		return
	}
	for key := range m.Annotations {
		if _, exact := noiseAnnotationKeys[key]; exact {
			delete(m.Annotations, key)
			continue
		}
		for _, prefix := range noiseAnnotationPrefixes {
			if strings.HasPrefix(key, prefix) {
				delete(m.Annotations, key)
				break
			}
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
