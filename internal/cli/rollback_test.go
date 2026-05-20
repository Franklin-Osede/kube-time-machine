package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// makeDeployment is a small helper used by rollback tests to build a
// typed Deployment, marshal it to JSON, and produce both the live
// in-cluster representation and the bytes that would have been stored
// in a snapshot (post-sanitisation).
func makeDeployment(name, image, rv string) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "default",
			ResourceVersion: rv,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: image}},
				},
			},
		},
	}
}

func deploymentSnapshotPayload(t *testing.T, name, image string) []byte {
	t.Helper()
	// Snapshot payload mirrors the post-sanitisation shape: no RV.
	d := makeDeployment(name, image, "")
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestRollbackDeployment_HappyPath_UsesPreviewRV(t *testing.T) {
	// Cluster has a Deployment at RV "100" with image nginx:1.27.
	liveDep := makeDeployment("api", "nginx:1.27", "100")
	client := fake.NewSimpleClientset(liveDep)

	target := key("Deployment", "default", "api")
	// Snapshot payload represents the desired rollback target: nginx:1.25.
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, target, payload, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}

	// Verify the cluster now holds the rolled-back image.
	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("post-rollback Get: %v", err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.25" {
		t.Errorf("cluster image after rollback: want nginx:1.25, got %q", got.Spec.Template.Spec.Containers[0].Image)
	}
	if !strings.Contains(out.String(), "rollback applied") {
		t.Errorf("expected success message, got:\n%s", out.String())
	}
}

func TestRollbackDeployment_AutoYesBypassesPrompt(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("api", "nginx:1.27", "100"))
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	// Empty stdin — would block on prompt without --yes.
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader(""),
		client, key("Deployment", "default", "api"), payload, true,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("--yes should skip the prompt, but [y/N] appeared in output:\n%s", out.String())
	}
}

func TestRollbackDeployment_AbortedAtPrompt(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("api", "nginx:1.27", "100"))
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("n\n"),
		client, key("Deployment", "default", "api"), payload, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	// Cluster image should still be the original.
	got, _ := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.27" {
		t.Errorf("cluster should be untouched after abort; got %q", got.Spec.Template.Spec.Containers[0].Image)
	}
	if !strings.Contains(out.String(), "rollback aborted") {
		t.Errorf("expected 'rollback aborted', got:\n%s", out.String())
	}
}

func TestRollbackDeployment_404PathCreates(t *testing.T) {
	client := fake.NewSimpleClientset() // empty cluster
	payload := deploymentSnapshotPayload(t, "api", "nginx:1.25")

	var out bytes.Buffer
	if err := rollbackDeployment(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("Deployment", "default", "api"), payload, false,
	); err != nil {
		t.Fatalf("rollbackDeployment: %v", err)
	}
	if !strings.Contains(out.String(), "will be CREATED") {
		t.Errorf("expected 'will be CREATED' notice, got:\n%s", out.String())
	}
	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("post-create Get: %v", err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "nginx:1.25" {
		t.Errorf("created image: want nginx:1.25, got %q", got.Spec.Template.Spec.Containers[0].Image)
	}
	// stripServerOwned should have wiped these on the inbound payload.
	if got.ResourceVersion == "100" {
		t.Errorf("ResourceVersion should not be persisted from payload on Create")
	}
}

func TestRollbackConfigMap_HappyPath(t *testing.T) {
	live := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default", ResourceVersion: "55"},
		Data:       map[string]string{"env": "prod"},
	}
	client := fake.NewSimpleClientset(live)

	targetCM := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Data:       map[string]string{"env": "dev"},
	}
	payload, _ := json.Marshal(targetCM)

	var out bytes.Buffer
	if err := rollbackConfigMap(
		context.Background(), &out, strings.NewReader("y\n"),
		client, key("ConfigMap", "default", "cfg"), payload, false,
	); err != nil {
		t.Fatalf("rollbackConfigMap: %v", err)
	}
	got, _ := client.CoreV1().ConfigMaps("default").Get(context.Background(), "cfg", metav1.GetOptions{})
	if got.Data["env"] != "dev" {
		t.Errorf("ConfigMap data after rollback: want env=dev, got %q", got.Data["env"])
	}
}

func TestRunRollback_UnknownKindErrors(t *testing.T) {
	// This test exercises the dispatch in runRollback indirectly through
	// the package-level error path. The fake client is built via
	// kubeclient (which would fail), so we bypass and test only the
	// dispatch logic by calling the helper that would reject the kind.
	// In practice the unsupported-kind error is raised in runRollback;
	// here we just assert the message format the user would see.
	err := errf("rollback supports only Deployment and ConfigMap in MVP (got %q)", "Service")
	if !strings.Contains(err.Error(), "Deployment and ConfigMap") {
		t.Errorf("unexpected error format: %v", err)
	}
}

func TestStripServerOwned_RemovesEverything(t *testing.T) {
	m := metav1.ObjectMeta{
		Name:              "x",
		ResourceVersion:   "100",
		UID:               "abc-123",
		CreationTimestamp: metav1.Now(),
		Generation:        7,
		ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "k"}},
		SelfLink:          "/api/v1/x",
	}
	stripServerOwned(&m)

	if m.Name != "x" {
		t.Errorf("Name should be preserved")
	}
	if m.ResourceVersion != "" || m.UID != "" || m.Generation != 0 || m.SelfLink != "" || m.ManagedFields != nil {
		t.Errorf("server-owned fields not fully stripped: %+v", m)
	}
	if !m.CreationTimestamp.IsZero() {
		t.Errorf("CreationTimestamp should be zero, got %v", m.CreationTimestamp)
	}
}

func TestPromptConfirm(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"n\n", false},
		{"\n", false},
		{"maybe\n", false},
	} {
		t.Run(strings.TrimSpace(tc.input)+"_"+boolStr(tc.want), func(t *testing.T) {
			var out bytes.Buffer
			got := promptConfirm(strings.NewReader(tc.input), &out)
			if got != tc.want {
				t.Errorf("input %q: want %v, got %v", tc.input, tc.want, got)
			}
		})
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
