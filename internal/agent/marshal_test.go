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

// TestMarshalDeployment_StripsResourceVersionAndManagedFields: Deployments
// differing only in ResourceVersion, Generation and ManagedFields produce
// identical State. Both use the same managers, which survive as
// ktm.io/managers.
func TestMarshalDeployment_StripsResourceVersionAndManagedFields(t *testing.T) {
	managers := []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}

	d1 := newDeployment("nginx:1.0")
	d1.ResourceVersion = "100"
	d1.Generation = 1
	d1.ManagedFields = managers

	d2 := newDeployment("nginx:1.0")
	d2.ResourceVersion = "9999"
	d2.Generation = 42
	d2.ManagedFields = managers

	_, s1, _ := agent.MarshalDeployment(d1)
	_, s2, _ := agent.MarshalDeployment(d2)
	if string(s1) != string(s2) {
		t.Errorf("noise fields leaked into state:\nd1: %s\nd2: %s", s1, s2)
	}
}

// TestMarshalDeployment_ManagersAnnotation: manager names from ManagedFields
// become ktm.io/managers.
func TestMarshalDeployment_ManagersAnnotation(t *testing.T) {
	d := newDeployment("nginx:1.0")
	d.ManagedFields = []metav1.ManagedFieldsEntry{
		{Manager: "kubectl"}, {Manager: "helm"}, {Manager: "kubectl"},
	}
	_, s, _ := agent.MarshalDeployment(d)
	if !strings.Contains(string(s), `"ktm.io/managers":"helm,kubectl"`) {
		t.Errorf("managers annotation missing or wrong in state: %s", s)
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

// TestMarshalDeployment_RealChangeProducesDifferentBytes: a spec change must
// change State, so the stripping tests cannot pass on a constant output.
func TestMarshalDeployment_RealChangeProducesDifferentBytes(t *testing.T) {
	_, s1, _ := agent.MarshalDeployment(newDeployment("nginx:1.0"))
	_, s2, _ := agent.MarshalDeployment(newDeployment("nginx:2.0"))
	if string(s1) == string(s2) {
		t.Errorf("changing image tag did not change state bytes: %s", s1)
	}
}

// TestMarshalDoesNotMutateInput: marshalling must not mutate the informer's
// shared cache.
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

// TestMarshalDeployment_StripsTopLevelStatusKey: State has no top-level
// "status" key (ADR-0005).
func TestMarshalDeployment_StripsTopLevelStatusKey(t *testing.T) {
	d := newDeployment("nginx:1.0")
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 42,
		Replicas:           3,
		ReadyReplicas:      3,
		UpdatedReplicas:    3,
		AvailableReplicas:  3,
	}

	_, state, err := agent.MarshalDeployment(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(state, &asMap); err != nil {
		t.Fatalf("state is not a valid JSON object: %v\npayload: %s", err, state)
	}
	if _, present := asMap["status"]; present {
		t.Errorf("top-level 'status' key not stripped from output:\n%s", state)
	}
}

// TestMarshalDeployment_StatusOnlyDifferenceProducesIdenticalBytes: a
// status-only difference produces identical State (ADR-0005).
func TestMarshalDeployment_StatusOnlyDifferenceProducesIdenticalBytes(t *testing.T) {
	d1 := newDeployment("nginx:1.0")
	d1.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		Replicas:           1,
	}
	d2 := newDeployment("nginx:1.0")
	d2.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 9999,
		Replicas:           42,
		Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse},
		},
	}

	_, s1, _ := agent.MarshalDeployment(d1)
	_, s2, _ := agent.MarshalDeployment(d2)
	if string(s1) != string(s2) {
		t.Errorf("status differences leaked into state:\ns1: %s\ns2: %s", s1, s2)
	}
}

// TestMarshalDeployment_StripsNoiseAnnotations: controller-owned annotations
// are removed and user annotations survive.
func TestMarshalDeployment_StripsNoiseAnnotations(t *testing.T) {
	cases := []struct {
		name string
		ann  map[string]string
	}{
		{"kubectl", map[string]string{
			"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"apps/v1"}`,
			"app": "api", // user annotation — must survive
		}},
		{"deployment-revision", map[string]string{
			"deployment.kubernetes.io/revision": "42",
			"team":                              "platform",
		}},
		{"helm-prefix", map[string]string{
			"meta.helm.sh/release-name":      "my-release",
			"meta.helm.sh/release-namespace": "prod",
			"version":                        "1.0",
		}},
		{"argocd-prefix", map[string]string{
			"argocd.argoproj.io/sync-wave": "1",
			"env":                          "prod",
		}},
		{"flux-prefix", map[string]string{
			"fluxcd.io/sync-checksum": "abc123",
			"owner":                   "platform-team",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newDeployment("nginx:1.0")
			base.Annotations = nil
			_, baseState, err := agent.MarshalDeployment(base)
			if err != nil {
				t.Fatalf("marshal base: %v", err)
			}

			noisy := newDeployment("nginx:1.0")
			noisy.Annotations = tc.ann
			_, noisyState, err := agent.MarshalDeployment(noisy)
			if err != nil {
				t.Fatalf("marshal noisy: %v", err)
			}

			// States are equal iff every annotation in tc.ann is noise.
			hasUserAnn := false
			for k := range tc.ann {
				switch k {
				case "kubectl.kubernetes.io/last-applied-configuration",
					"deployment.kubernetes.io/revision",
					"deprecated.daemonset.template.generation":
				default:
					isPrefix := false
					for _, p := range []string{"argocd.argoproj.io/", "fluxcd.io/", "kustomize.toolkit.fluxcd.io/", "meta.helm.sh/"} {
						if len(k) >= len(p) && k[:len(p)] == p {
							isPrefix = true
							break
						}
					}
					if !isPrefix {
						hasUserAnn = true
					}
				}
			}

			if !hasUserAnn {
				if string(noisyState) != string(baseState) {
					t.Errorf("noise annotations leaked into state:\nnoisy: %s\nbase:  %s", noisyState, baseState)
				}
			} else {
				if string(noisyState) == string(baseState) {
					t.Errorf("user annotation was incorrectly stripped: noisy state equals baseline")
				}
			}
		})
	}
}

// TestMarshalDeployment_IsValidJSON: State decodes as JSON.
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
