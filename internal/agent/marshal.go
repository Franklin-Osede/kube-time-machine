package agent

import (
	"encoding/json"
	"fmt"

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

// sanitiseMeta strips fields that mutate without user intent and would
// otherwise produce spurious "modified" deltas:
//
//   - ResourceVersion bumps on every server-side touch, including
//     no-op resyncs by other controllers.
//   - ManagedFields is large and tracks SSA owners, not user intent.
//   - Generation increments on every spec change — but the spec change
//     itself is the signal, so the generation field is redundant noise.
//
// CreationTimestamp, UID, Labels and Annotations are kept: annotations
// in particular can carry operationally meaningful declarative state
// (e.g. last-applied-configuration). The full .status block is removed
// downstream by stripStatus — see ADR-0005.
func sanitiseMeta(m *metav1.ObjectMeta) {
	m.ResourceVersion = ""
	m.ManagedFields = nil
	m.Generation = 0
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
