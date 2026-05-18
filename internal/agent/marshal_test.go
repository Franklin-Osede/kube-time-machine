package agent_test

import (
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Franklin-Osede/kube-time-machine/internal/agent"
)

func newDeployment(image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "api",
			Namespace:       "default",
			ResourceVersion: "1234",
			Generation:      7,
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "kubectl-client-side-apply"},
			},
			Labels: map[string]string{"app": "api"},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "api", Image: image},
					},
				},
			},
		},
	}
}

func newConfigMap(value string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "cfg",
			Namespace:       "default",
			ResourceVersion: "9999",
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "kubectl"},
			},
		},
		Data: map[string]string{"key": value},
	}
}

func TestKeyForDeployment(t *testing.T) {
	d := newDeployment("nginx:1.0")
	got := agent.KeyForDeployment(d)
	if got.Kind != agent.KindDeployment || got.Namespace != "default" || got.Name != "api" {
		t.Errorf("unexpected key: %+v", got)
	}
}

func TestKeyForConfigMap(t *testing.T) {
	cm := newConfigMap("v1")
	got := agent.KeyForConfigMap(cm)
	if got.Kind != agent.KindConfigMap || got.Namespace != "default" || got.Name != "cfg" {
		t.Errorf("unexpected key: %+v", got)
	}
}

func TestMarshalDeployment_NilReturnsError(t *testing.T) {
	if _, _, err := agent.MarshalDeployment(nil); err == nil {
		t.Fatal("expected error on nil Deployment, got nil")
	}
}

func TestMarshalConfigMap_NilReturnsError(t *testing.T) {
	if _, _, err := agent.MarshalConfigMap(nil); err == nil {
		t.Fatal("expected error on nil ConfigMap, got nil")
	}
}

func TestMarshalDeployment_DeterministicAcrossCalls(t *testing.T) {
	d := newDeployment("nginx:1.0")
	_, s1, err := agent.MarshalDeployment(d)
	if err != nil {
		t.Fatalf("marshal #1: %v", err)
	}
	_, s2, err := agent.MarshalDeployment(d)
	if err != nil {
		t.Fatalf("marshal #2: %v", err)
	}
	if string(s1) != string(s2) {
		t.Errorf("non-deterministic marshal:\n#1: %s\n#2: %s", s1, s2)
	}
}

// TestMarshalDeployment_StripsResourceVersionAndManagedFields is the key
// behavioural test: two Deployments that differ ONLY in those fields
// must produce identical State bytes, so the snapshotter doesn't record
// a spurious "modified" delta on every flush.
func TestMarshalDeployment_StripsResourceVersionAndManagedFields(t *testing.T) {
	d1 := newDeployment("nginx:1.0")
	d1.ResourceVersion = "100"
	d1.Generation = 1
	d1.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "a"}}

	d2 := newDeployment("nginx:1.0")
	d2.ResourceVersion = "9999"
	d2.Generation = 42
	d2.ManagedFields = []metav1.ManagedFieldsEntry{
		{Manager: "x"}, {Manager: "y"}, {Manager: "z"},
	}

	_, s1, _ := agent.MarshalDeployment(d1)
	_, s2, _ := agent.MarshalDeployment(d2)
	if string(s1) != string(s2) {
		t.Errorf("noise fields leaked into state:\nd1: %s\nd2: %s", s1, s2)
	}
}

func TestMarshalConfigMap_StripsResourceVersion(t *testing.T) {
	cm1 := newConfigMap("v1")
	cm1.ResourceVersion = "1"
	cm2 := newConfigMap("v1")
	cm2.ResourceVersion = "12345"

	_, s1, _ := agent.MarshalConfigMap(cm1)
	_, s2, _ := agent.MarshalConfigMap(cm2)
	if string(s1) != string(s2) {
		t.Errorf("ResourceVersion leaked into state:\ns1: %s\ns2: %s", s1, s2)
	}
}

// TestMarshalDeployment_RealChangeProducesDifferentBytes is the inverse:
// a meaningful spec change (image tag) MUST produce different State.
// Without this, MarshalDeployment could pass the stripping test by
// outputting a constant — defending the contract from both sides.
func TestMarshalDeployment_RealChangeProducesDifferentBytes(t *testing.T) {
	_, s1, _ := agent.MarshalDeployment(newDeployment("nginx:1.0"))
	_, s2, _ := agent.MarshalDeployment(newDeployment("nginx:2.0"))
	if string(s1) == string(s2) {
		t.Errorf("changing image tag did not change state bytes: %s", s1)
	}
}

// TestMarshalDoesNotMutateInput guards a critical invariant: the
// function must DeepCopy before sanitising, or it would corrupt the
// informer's shared cache.
func TestMarshalDoesNotMutateInput(t *testing.T) {
	d := newDeployment("nginx:1.0")
	originalRV := d.ResourceVersion
	originalGen := d.Generation
	originalMF := len(d.ManagedFields)

	_, _, _ = agent.MarshalDeployment(d)

	if d.ResourceVersion != originalRV {
		t.Errorf("ResourceVersion mutated: was %q, now %q", originalRV, d.ResourceVersion)
	}
	if d.Generation != originalGen {
		t.Errorf("Generation mutated: was %d, now %d", originalGen, d.Generation)
	}
	if len(d.ManagedFields) != originalMF {
		t.Errorf("ManagedFields mutated: was %d entries, now %d", originalMF, len(d.ManagedFields))
	}
}

// TestMarshalDeployment_IsValidJSON sanity-checks that the bytes round-trip
// through encoding/json without errors — important because anything
// downstream may want to decode the State (e.g. ktm diff for a human-
// readable view).
func TestMarshalDeployment_IsValidJSON(t *testing.T) {
	_, state, err := agent.MarshalDeployment(newDeployment("nginx:1.0"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip appsv1.Deployment
	if err := json.Unmarshal(state, &roundtrip); err != nil {
		t.Fatalf("state is not valid Deployment JSON: %v\npayload: %s", err, state)
	}
	if !strings.HasPrefix(roundtrip.Spec.Template.Spec.Containers[0].Image, "nginx:") {
		t.Errorf("roundtrip lost the image field: got %q", roundtrip.Spec.Template.Spec.Containers[0].Image)
	}
}
