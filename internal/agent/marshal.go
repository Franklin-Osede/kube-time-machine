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
// We deliberately keep CreationTimestamp, UID, Labels, Annotations, and
// (for now) the full .status block. Annotations can carry operationally
// meaningful changes (e.g. last-applied-configuration). Status is kept
// pending real-world feedback; if it proves too noisy we can revisit
// with a dedicated ADR and field-level diff in Phase 2.
func sanitiseMeta(m *metav1.ObjectMeta) {
	m.ResourceVersion = ""
	m.ManagedFields = nil
	m.Generation = 0
}
